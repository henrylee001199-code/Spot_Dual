package safety

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"

	"spot-dual/internal/alert"
	"spot-dual/internal/core"
)

var ErrCircuitOpen = errors.New("circuit breaker open")

type circuitState string

const (
	circuitClosed   circuitState = "closed"
	circuitOpen     circuitState = "open"
	circuitHalfOpen circuitState = "half_open"
)

const (
	defaultReconnectCooldown          = 30 * time.Second
	defaultReconnectHalfOpenSuccesses = 1
)

type circuit struct {
	maxFailures     int
	failures        int
	state           circuitState
	openedAt        time.Time
	openErr         error
	halfOpenSuccess int
}

type Breaker struct {
	enabled bool

	mu        sync.Mutex
	place     circuit
	cancel    circuit
	reconnect circuit

	reconnectCooldown          time.Duration
	reconnectHalfOpenSuccesses int

	alerter alert.Alerter
}

func NewBreaker(enabled bool, maxPlaceFailures, maxCancelFailures, maxReconnectFailures int) *Breaker {
	return &Breaker{
		enabled: enabled,
		place: circuit{
			maxFailures: maxPlaceFailures,
			state:       circuitClosed,
		},
		cancel: circuit{
			maxFailures: maxCancelFailures,
			state:       circuitClosed,
		},
		reconnect: circuit{
			maxFailures: maxReconnectFailures,
			state:       circuitClosed,
		},
		reconnectCooldown:          defaultReconnectCooldown,
		reconnectHalfOpenSuccesses: defaultReconnectHalfOpenSuccesses,
	}
}

func (b *Breaker) SetReconnectRecovery(cooldown time.Duration, halfOpenSuccesses int) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if cooldown <= 0 {
		cooldown = defaultReconnectCooldown
	}
	if halfOpenSuccesses < 1 {
		halfOpenSuccesses = defaultReconnectHalfOpenSuccesses
	}
	b.reconnectCooldown = cooldown
	b.reconnectHalfOpenSuccesses = halfOpenSuccesses
}

func (b *Breaker) RecordPlace(err error) error {
	if b == nil {
		return nil
	}
	return b.record("place order", &b.place, err)
}

func (b *Breaker) RecordCancel(err error) error {
	if b == nil {
		return nil
	}
	return b.record("cancel order", &b.cancel, err)
}

func (b *Breaker) RecordReconnect(err error) error {
	if b == nil {
		return nil
	}
	return b.record("reconnect", &b.reconnect, err)
}

func (b *Breaker) AllowReconnect() error {
	if b == nil || !b.enabled {
		return nil
	}
	b.mu.Lock()
	state := b.reconnect.state
	now := time.Now().UTC()
	if state == circuitOpen {
		if b.reconnectCooldown > 0 && now.Sub(b.reconnect.openedAt) < b.reconnectCooldown {
			err := b.reconnect.openErr
			if err == nil {
				err = fmt.Errorf("%w: reconnect circuit is open", ErrCircuitOpen)
			}
			b.mu.Unlock()
			return err
		}
		b.reconnect.state = circuitHalfOpen
		b.reconnect.halfOpenSuccess = 0
		b.reconnect.failures = 0
		b.reconnect.openErr = nil
		alerter := b.alerter
		b.mu.Unlock()
		log.Printf("level=INFO event=circuit_breaker_half_open action=%q cooldown_sec=%d", "reconnect", int64(b.reconnectCooldown/time.Second))
		if alerter != nil {
			alerter.Important("circuit_breaker_half_open", map[string]string{
				"action":       "reconnect",
				"cooldown_sec": strconv.FormatInt(int64(b.reconnectCooldown/time.Second), 10),
			})
		}
		return nil
	}
	b.mu.Unlock()
	return nil
}

func (b *Breaker) ReconnectCooldownRemaining() time.Duration {
	if b == nil || !b.enabled {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.reconnect.state != circuitOpen {
		return 0
	}
	if b.reconnectCooldown <= 0 {
		return 0
	}
	elapsed := time.Since(b.reconnect.openedAt)
	if elapsed >= b.reconnectCooldown {
		return 0
	}
	return b.reconnectCooldown - elapsed
}

func (b *Breaker) ResetReconnect() {
	if b == nil {
		return
	}
	_ = b.RecordReconnect(nil)
}

func (b *Breaker) SetAlerter(alerter alert.Alerter) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.alerter = alerter
}

func (b *Breaker) record(name string, c *circuit, err error) error {
	if b == nil || !b.enabled || c == nil {
		return nil
	}

	b.mu.Lock()
	if c.maxFailures < 1 {
		b.mu.Unlock()
		return nil
	}

	if err == nil {
		prevFailures := c.failures
		prevState := c.state
		recovered := false
		switch c.state {
		case circuitHalfOpen:
			c.halfOpenSuccess++
			if c.halfOpenSuccess >= b.reconnectHalfOpenSuccesses || name != "reconnect" {
				recovered = true
				c.state = circuitClosed
				c.failures = 0
				c.openErr = nil
				c.openedAt = time.Time{}
				c.halfOpenSuccess = 0
			}
		case circuitOpen:
			// 非重连路径在 open 状态下不能探测；保持状态不变。
		case circuitClosed:
			if c.failures > 0 {
				recovered = true
				c.failures = 0
			}
		}
		alerter := b.alerter
		b.mu.Unlock()
		if recovered {
			log.Printf(
				"level=INFO event=circuit_breaker_recovered action=%q previous_consecutive_failures=%d from_state=%q",
				name,
				prevFailures,
				string(prevState),
			)
			if alerter != nil {
				alerter.Important("circuit_breaker_recovered", map[string]string{
					"action":                        name,
					"previous_consecutive_failures": strconv.Itoa(prevFailures),
					"from_state":                    string(prevState),
				})
			}
		}
		return nil
	}

	if c.state == circuitOpen {
		openErr := c.openErr
		if openErr == nil {
			openErr = fmt.Errorf("%w: %s circuit is open", ErrCircuitOpen, name)
			c.openErr = openErr
		}
		b.mu.Unlock()
		return openErr
	}

	if c.state == circuitHalfOpen {
		openErr := b.tripLocked(name, c, err, 1, "half_open_probe_failed")
		alerter := b.alerter
		b.mu.Unlock()
		log.Printf(
			"level=ERROR event=circuit_breaker_trip action=%q phase=%q threshold=%d last_error=%q",
			name,
			"half_open",
			c.maxFailures,
			err.Error(),
		)
		if alerter != nil {
			alerter.Important("circuit_breaker_trip", map[string]string{
				"action":     name,
				"phase":      "half_open",
				"threshold":  strconv.Itoa(c.maxFailures),
				"last_error": err.Error(),
			})
		}
		return openErr
	}

	c.failures++
	failures := c.failures
	limit := c.maxFailures
	alerter := b.alerter
	if failures < limit {
		nearTrip := shouldWarnNearTrip(name, failures, limit)
		b.mu.Unlock()
		if nearTrip {
			log.Printf(
				"level=WARN event=circuit_breaker_near_trip action=%q consecutive_failures=%d threshold=%d last_error=%q",
				name,
				failures,
				limit,
				err.Error(),
			)
			if alerter != nil {
				alerter.Important("circuit_breaker_near_trip", map[string]string{
					"action":               name,
					"consecutive_failures": strconv.Itoa(failures),
					"threshold":            strconv.Itoa(limit),
					"last_error":           err.Error(),
				})
			}
		}
		return nil
	}

	openErr := b.tripLocked(name, c, err, failures, "consecutive_failures")
	b.mu.Unlock()
	log.Printf(
		"level=ERROR event=circuit_breaker_trip action=%q consecutive_failures=%d threshold=%d last_error=%q",
		name,
		failures,
		limit,
		err.Error(),
	)
	if alerter != nil {
		alerter.Important("circuit_breaker_trip", map[string]string{
			"action":               name,
			"consecutive_failures": strconv.Itoa(failures),
			"threshold":            strconv.Itoa(limit),
			"last_error":           err.Error(),
		})
	}
	return openErr
}

func (b *Breaker) tripLocked(name string, c *circuit, err error, failures int, reason string) error {
	if failures < 1 {
		failures = c.maxFailures
	}
	c.state = circuitOpen
	c.openedAt = time.Now().UTC()
	c.halfOpenSuccess = 0
	c.failures = failures
	if name == "reconnect" && b.reconnectCooldown > 0 {
		c.openErr = fmt.Errorf("%w: %s failed %d consecutive times, cooldown=%s, reason=%s, last error: %v", ErrCircuitOpen, name, failures, b.reconnectCooldown.String(), reason, err)
	} else {
		c.openErr = fmt.Errorf("%w: %s failed %d consecutive times, reason=%s, last error: %v", ErrCircuitOpen, name, failures, reason, err)
	}
	return c.openErr
}

func shouldWarnNearTrip(action string, failures, limit int) bool {
	if limit <= 1 || failures != limit-1 {
		return false
	}
	return action == "place order" || action == "cancel order"
}

type Executor interface {
	PlaceOrder(ctx context.Context, order core.Order) (core.Order, error)
	CancelOrder(ctx context.Context, symbol, orderID string) error
	Balances(ctx context.Context) (core.Balance, error)
}

type GuardedExecutor struct {
	inner   Executor
	breaker *Breaker
}

func NewGuardedExecutor(inner Executor, breaker *Breaker) *GuardedExecutor {
	return &GuardedExecutor{
		inner:   inner,
		breaker: breaker,
	}
}

func (e *GuardedExecutor) PlaceOrder(ctx context.Context, order core.Order) (core.Order, error) {
	placed, err := e.inner.PlaceOrder(ctx, order)
	if trip := e.breaker.RecordPlace(err); trip != nil {
		return placed, trip
	}
	return placed, err
}

func (e *GuardedExecutor) CancelOrder(ctx context.Context, symbol, orderID string) error {
	err := e.inner.CancelOrder(ctx, symbol, orderID)
	if trip := e.breaker.RecordCancel(err); trip != nil {
		return trip
	}
	return err
}

func (e *GuardedExecutor) Balances(ctx context.Context) (core.Balance, error) {
	return e.inner.Balances(ctx)
}

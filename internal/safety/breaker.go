package safety

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"sync"

	"grid-trading/internal/alert"
	"grid-trading/internal/core"
)

var ErrCircuitOpen = errors.New("circuit breaker open")

type Breaker struct {
	enabled bool

	maxPlaceFailures     int
	maxCancelFailures    int
	maxReconnectFailures int

	mu               sync.Mutex
	placeFailures    int
	cancelFailures   int
	reconnectFailure int
	open             bool
	openErr          error
	alerter          alert.Alerter
}

func NewBreaker(enabled bool, maxPlaceFailures, maxCancelFailures, maxReconnectFailures int) *Breaker {
	return &Breaker{
		enabled:              enabled,
		maxPlaceFailures:     maxPlaceFailures,
		maxCancelFailures:    maxCancelFailures,
		maxReconnectFailures: maxReconnectFailures,
	}
}

func (b *Breaker) RecordPlace(err error) error {
	return b.record("place order", &b.placeFailures, b.maxPlaceFailures, err)
}

func (b *Breaker) RecordCancel(err error) error {
	return b.record("cancel order", &b.cancelFailures, b.maxCancelFailures, err)
}

func (b *Breaker) RecordReconnect(err error) error {
	return b.record("reconnect", &b.reconnectFailure, b.maxReconnectFailures, err)
}

func (b *Breaker) ResetReconnect() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.open || !b.enabled {
		return
	}
	b.reconnectFailure = 0
}

func (b *Breaker) SetAlerter(alerter alert.Alerter) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.alerter = alerter
}

func (b *Breaker) record(name string, counter *int, limit int, err error) error {
	if b == nil || !b.enabled {
		return nil
	}
	b.mu.Lock()
	if b.open {
		openErr := b.openErr
		b.mu.Unlock()
		return openErr
	}
	if limit < 1 {
		b.mu.Unlock()
		return nil
	}
	if err == nil {
		prevFailures := *counter
		alerter := b.alerter
		*counter = 0
		b.mu.Unlock()
		if prevFailures > 0 {
			log.Printf(
				"level=INFO event=circuit_breaker_recovered action=%q previous_consecutive_failures=%d threshold=%d",
				name,
				prevFailures,
				limit,
			)
			if alerter != nil {
				alerter.Important("circuit_breaker_recovered", map[string]string{
					"action":                        name,
					"previous_consecutive_failures": strconv.Itoa(prevFailures),
					"threshold":                     strconv.Itoa(limit),
				})
			}
		}
		return nil
	}
	*counter++
	failures := *counter
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

	b.open = true
	b.openErr = fmt.Errorf("%w: %s failed %d consecutive times, last error: %v", ErrCircuitOpen, name, failures, err)
	openErr := b.openErr
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

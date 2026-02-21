package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"spot-dual/internal/alert"
	"spot-dual/internal/core"
	"spot-dual/internal/exchange/binance"
	"spot-dual/internal/safety"
	"spot-dual/internal/store"
	"spot-dual/internal/strategy"
)

var ErrManualIntervention = errors.New("manual intervention required")
var ErrFatalLocal = errors.New("fatal local error")

const liveSeenTrackerMaxEntries = 10000

type LiveRunner struct {
	Exchange   *binance.Client
	Strategy   strategy.Strategy
	Symbol     string
	Mode       string
	InstanceID string
	Keepalive  time.Duration
	Heartbeat  time.Duration
	Reconcile  time.Duration
	Store      *store.Store
	Breaker    *safety.Breaker
	Alerts     alert.Alerter
}

func (r *LiveRunner) Run(ctx context.Context) (runErr error) {
	seen := newSeenTracker(liveSeenTrackerMaxEntries, 24*time.Hour)
	backoff := time.Second
	reconnectAttempts := 0
	disconnectStartedAt := time.Time{}
	startedAt := time.Now().UTC()

	r.persistRuntimeStatus("starting", startedAt, reconnectAttempts, disconnectStartedAt, nil)
	defer func() {
		err := runErr
		if errors.Is(err, context.Canceled) {
			err = nil
		}
		r.persistRuntimeStatus("stopped", startedAt, reconnectAttempts, disconnectStartedAt, err)
	}()

	for {
		reconnect := reconnectAttempts > 0
		if reconnect && r.Breaker != nil {
			if allowErr := r.Breaker.AllowReconnect(); allowErr != nil {
				r.persistRuntimeStatus("degraded", startedAt, reconnectAttempts, disconnectStartedAt, allowErr)
				wait := time.Second
				if rem := r.Breaker.ReconnectCooldownRemaining(); rem > wait {
					wait = rem
				}
				select {
				case <-time.After(wait):
				case <-ctx.Done():
					runErr = ctx.Err()
					return runErr
				}
				continue
			}
		}
		r.persistRuntimeStatus("running", startedAt, reconnectAttempts, disconnectStartedAt, nil)
		if err := r.runOnce(ctx, reconnect, seen, &reconnectAttempts, &disconnectStartedAt, &backoff, startedAt); err != nil {
			if ctx.Err() != nil {
				runErr = ctx.Err()
				return runErr
			}
			if errors.Is(err, ErrFatalLocal) {
				log.Printf("level=ERROR event=runner_stopped reason=%q", err.Error())
				r.alertImportant("runner_stopped", map[string]string{
					"reason": err.Error(),
				})
				r.alertImportant("manual_intervention_required", map[string]string{
					"reason": "local_state_failure",
					"detail": err.Error(),
				})
				runErr = err
				return runErr
			}
			if disconnectStartedAt.IsZero() {
				disconnectStartedAt = time.Now().UTC()
				r.alertImportant("user_stream_disconnected", map[string]string{
					"reason": err.Error(),
				})
			}
			if errors.Is(err, ErrManualIntervention) {
				r.persistRuntimeStatus("degraded", startedAt, reconnectAttempts, disconnectStartedAt, err)
				log.Printf("level=ERROR event=runner_stopped reason=%q", err.Error())
				r.alertImportant("runner_stopped", map[string]string{
					"reason": err.Error(),
				})
				r.alertImportant("manual_intervention_required", map[string]string{
					"reason": "state_reconcile_risk",
					"detail": err.Error(),
				})
				runErr = err
				return runErr
			}
			nextAttempts := reconnectAttempts + 1
			r.persistRuntimeStatus("degraded", startedAt, nextAttempts, disconnectStartedAt, err)
			var trip error
			if r.Breaker != nil {
				trip = r.Breaker.RecordReconnect(err)
			}
			if trip != nil && !errors.Is(trip, safety.ErrCircuitOpen) {
				reconnectAttempts = nextAttempts
				runErr = trip
				return runErr
			}
			reconnectAttempts = nextAttempts
			wait := backoff
			if trip != nil && errors.Is(trip, safety.ErrCircuitOpen) && r.Breaker != nil {
				if rem := r.Breaker.ReconnectCooldownRemaining(); rem > wait {
					wait = rem
				}
			}
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				runErr = ctx.Err()
				return runErr
			}
			if backoff < 30*time.Second {
				backoff *= 2
				if backoff > 30*time.Second {
					backoff = 30 * time.Second
				}
			}
			continue
		}
		runErr = nil
		return nil
	}
}

func (r *LiveRunner) runOnce(ctx context.Context, reconnect bool, seen *seenTracker, reconnectAttempts *int, disconnectStartedAt *time.Time, backoff *time.Duration, startedAt time.Time) error {
	price, err := r.Exchange.TickerPrice(ctx, r.Symbol)
	if err != nil {
		return err
	}

	persisted, skipPersistedReconcile, err := r.loadPersistedForResync(reconnect)
	if err != nil {
		return err
	}

	if err := r.resync(ctx, price, seen, persisted, !skipPersistedReconcile); err != nil {
		if errors.Is(err, strategy.ErrStopped) {
			return nil
		}
		return err
	}

	stream, err := r.Exchange.NewUserStream(ctx, r.Keepalive)
	if err != nil {
		return err
	}
	if reconnect && disconnectStartedAt != nil && !disconnectStartedAt.IsZero() {
		down := time.Since(*disconnectStartedAt).Round(time.Second)
		attempts := 0
		if reconnectAttempts != nil {
			attempts = *reconnectAttempts
		}
		r.alertImportant("user_stream_reconnected", map[string]string{
			"reconnect_attempts": strconv.Itoa(attempts),
			"down_duration":      down.String(),
		})
		*disconnectStartedAt = time.Time{}
		if reconnectAttempts != nil {
			*reconnectAttempts = 0
		}
		if backoff != nil {
			*backoff = time.Second
		}
		r.persistRuntimeStatus("running", startedAt, 0, time.Time{}, nil)
	}
	r.Breaker.ResetReconnect()
	trades, errs := stream.Trades(ctx, r.Symbol)
	var heartbeat <-chan time.Time
	if r.Heartbeat > 0 {
		ticker := time.NewTicker(r.Heartbeat)
		defer ticker.Stop()
		heartbeat = ticker.C
	}
	var reconcileTick <-chan time.Time
	if r.Reconcile > 0 {
		ticker := time.NewTicker(r.Reconcile)
		defer ticker.Stop()
		reconcileTick = ticker.C
	}
	for {
		select {
		case trade, ok := <-trades:
			if !ok {
				return errors.New("user stream closed")
			}
			dup, err := r.shouldSkipTrade(trade, seen, time.Now().UTC())
			if err != nil {
				return fmt.Errorf("%w: trade dedup check: %v", ErrFatalLocal, err)
			}
			if dup {
				continue
			}
			if err := r.Strategy.OnFill(ctx, trade); err != nil {
				if errors.Is(err, strategy.ErrStopped) {
					r.alertImportant("manual_intervention_required", map[string]string{
						"reason": "strategy_stopped",
						"stage":  "on_fill",
					})
					return nil
				}
				return fmt.Errorf("%w: strategy on_fill: %v", ErrFatalLocal, err)
			}
			if err := r.recordTradeLedger(trade); err != nil {
				return fmt.Errorf("%w: trade ledger record: %v", ErrFatalLocal, err)
			}
		case err, ok := <-errs:
			if ok && err != nil {
				return err
			}
		case <-heartbeat:
			attempts := 0
			if reconnectAttempts != nil {
				attempts = *reconnectAttempts
			}
			downSince := time.Time{}
			if disconnectStartedAt != nil {
				downSince = *disconnectStartedAt
			}
			r.persistRuntimeStatus("running", startedAt, attempts, downSince, nil)
		case <-reconcileTick:
			if err := r.periodicReconcile(ctx, seen); err != nil {
				if errors.Is(err, strategy.ErrStopped) {
					return nil
				}
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (r *LiveRunner) periodicReconcile(ctx context.Context, seen *seenTracker) error {
	price, err := r.Exchange.TickerPrice(ctx, r.Symbol)
	if err != nil {
		return err
	}
	return r.resync(ctx, price, seen, nil, true)
}

func (r *LiveRunner) loadPersistedForResync(reconnect bool) ([]core.Order, bool, error) {
	if reconnect || r.Store == nil {
		return nil, false, nil
	}
	state, stateOK, err := r.Store.LoadGridState()
	if err != nil {
		return nil, false, fmt.Errorf("%w: load grid state: %v", ErrFatalLocal, err)
	}
	openOrders, openOrdersOK, err := r.Store.LoadOpenOrdersSnapshot()
	if err != nil {
		return nil, false, fmt.Errorf("%w: load open orders snapshot: %v", ErrFatalLocal, err)
	}
	if stateOK && openOrdersOK {
		stateID := strings.TrimSpace(state.SnapshotID)
		ordersID := strings.TrimSpace(openOrders.SnapshotID)
		if (stateID != "" || ordersID != "") && stateID != ordersID {
			r.alertImportant("snapshot_mismatch_skip_reconcile_missing", map[string]string{
				"state_snapshot_id":       stateID,
				"open_orders_snapshot_id": ordersID,
				"action":                  "skip_reconcile_missing",
			})
			log.Printf(
				"level=WARN event=snapshot_mismatch_skip_reconcile_missing state_snapshot_id=%q open_orders_snapshot_id=%q",
				stateID,
				ordersID,
			)
			return nil, true, nil
		}
	}
	if !openOrdersOK || len(openOrders.Orders) == 0 {
		return nil, false, nil
	}
	return openOrders.Orders, false, nil
}

func (r *LiveRunner) resync(ctx context.Context, price decimal.Decimal, seen *seenTracker, persisted []core.Order, allowPersistedReconcile bool) error {
	open, err := r.Exchange.OpenOrders(ctx, r.Symbol)
	if err != nil {
		r.alertImportant("reconcile_open_orders_failed", map[string]string{
			"err": err.Error(),
		})
		return err
	}

	if allowPersistedReconcile {
		if len(persisted) == 0 && r.Store != nil {
			if orders, ok, err := r.Store.LoadOpenOrders(); err != nil {
				return fmt.Errorf("%w: load open orders in resync: %v", ErrFatalLocal, err)
			} else if ok && len(orders) > 0 {
				persisted = orders
			}
		}

		if len(persisted) > 0 {
			var err error
			open, err = r.reconcileMissing(ctx, open, persisted, seen)
			if err != nil {
				r.alertImportant("reconcile_missing_failed", map[string]string{
					"err": err.Error(),
				})
				return err
			}
		}
	}

	if reconciler, ok := r.Strategy.(strategy.Reconciler); ok {
		if err := reconciler.Reconcile(ctx, price, open); err != nil {
			if errors.Is(err, strategy.ErrStopped) {
				return err
			}
			r.alertImportant("reconcile_gap_failed", map[string]string{
				"err": err.Error(),
			})
			return fmt.Errorf("%w: strategy reconcile: %v", ErrFatalLocal, err)
		}
		return nil
	}
	r.alertImportant("reconcile_not_supported", map[string]string{
		"err": "strategy does not support reconcile",
	})
	return errors.New("strategy does not support reconcile")
}

func (r *LiveRunner) reconcileMissing(ctx context.Context, open []core.Order, persisted []core.Order, seen *seenTracker) ([]core.Order, error) {
	openByID := make(map[string]struct{}, len(open))
	for _, ord := range open {
		if ord.ID != "" {
			openByID[ord.ID] = struct{}{}
		}
	}

	missing := make([]core.Order, 0)
	for _, ord := range persisted {
		if ord.ID == "" {
			continue
		}
		if _, ok := openByID[ord.ID]; ok {
			continue
		}
		missing = append(missing, ord)
	}
	if len(missing) == 0 {
		return open, nil
	}

	stillOpen := make([]core.Order, 0)
	appliedTrade := false
	for _, ord := range missing {
		status, err := r.Exchange.QueryOrder(ctx, r.Symbol, ord.ID, ord.ClientID)
		if err != nil {
			if errors.Is(err, core.ErrOrderNotFound) || binance.IsAPIErrorCode(err, -2013) {
				continue
			}
			r.alertImportant("reconcile_query_order_failed", map[string]string{
				"order_id":          ord.ID,
				"client_order_id":   ord.ClientID,
				"missing_total":     strconv.Itoa(len(missing)),
				"exchange_response": err.Error(),
			})
			return open, err
		}
		switch status.Order.Status {
		case core.OrderNew, core.OrderPartiallyFilled:
			still := status.Order
			if status.ExecutedQty.Cmp(decimal.Zero) > 0 && still.Qty.Cmp(status.ExecutedQty) > 0 {
				still.Qty = still.Qty.Sub(status.ExecutedQty)
			}
			stillOpen = append(stillOpen, still)
		case core.OrderFilled:
			trade := tradeFromOrder(status, ord)
			if trade.OrderID != "" {
				dup, err := r.shouldSkipTrade(trade, seen, time.Now().UTC())
				if err != nil {
					return open, fmt.Errorf("%w: trade dedup check: %v", ErrFatalLocal, err)
				}
				if dup {
					break
				}
				if err := r.Strategy.OnFill(ctx, trade); err != nil {
					if errors.Is(err, strategy.ErrStopped) {
						r.alertImportant("manual_intervention_required", map[string]string{
							"reason": "strategy_stopped",
							"stage":  "reconcile_apply_fill",
						})
						return open, nil
					}
					r.alertImportant("reconcile_apply_fill_failed", map[string]string{
						"order_id":        trade.OrderID,
						"client_order_id": status.Order.ClientID,
						"err":             err.Error(),
					})
					return open, fmt.Errorf("%w: strategy reconcile apply fill: %v", ErrFatalLocal, err)
				}
				if err := r.recordTradeLedger(trade); err != nil {
					return open, fmt.Errorf("%w: trade ledger record: %v", ErrFatalLocal, err)
				}
				appliedTrade = true
			}
		case core.OrderCanceled, core.OrderRejected, core.OrderExpired:
			if status.ExecutedQty.Cmp(decimal.Zero) > 0 {
				trade := tradeFromOrder(status, ord)
				if trade.OrderID != "" {
					dup, err := r.shouldSkipTrade(trade, seen, time.Now().UTC())
					if err != nil {
						return open, fmt.Errorf("%w: trade dedup check: %v", ErrFatalLocal, err)
					}
					if dup {
						break
					}
					if err := r.Strategy.OnFill(ctx, trade); err != nil {
						if errors.Is(err, strategy.ErrStopped) {
							r.alertImportant("manual_intervention_required", map[string]string{
								"reason": "strategy_stopped",
								"stage":  "reconcile_apply_partial_close",
							})
							return open, nil
						}
						r.alertImportant("reconcile_apply_partial_close_failed", map[string]string{
							"order_id":        trade.OrderID,
							"client_order_id": status.Order.ClientID,
							"err":             err.Error(),
						})
						return open, fmt.Errorf("%w: strategy reconcile apply partial close: %v", ErrFatalLocal, err)
					}
					if err := r.recordTradeLedger(trade); err != nil {
						return open, fmt.Errorf("%w: trade ledger record: %v", ErrFatalLocal, err)
					}
					appliedTrade = true
				}
				r.alertImportant("order_closed_with_partial_fill", map[string]string{
					"order_id":        status.Order.ID,
					"client_order_id": status.Order.ClientID,
					"status":          string(status.Order.Status),
					"executed_qty":    status.ExecutedQty.String(),
					"order_qty":       status.Order.Qty.String(),
					"price":           status.Order.Price.String(),
					"action":          "auto_reconciled",
				})
				continue
			}
			if status.Order.Status == core.OrderCanceled {
				continue
			}
			r.alertImportant("order_rejected_or_expired", map[string]string{
				"order_id":        status.Order.ID,
				"client_order_id": status.Order.ClientID,
				"status":          string(status.Order.Status),
				"side":            string(status.Order.Side),
				"price":           status.Order.Price.String(),
				"qty":             status.Order.Qty.String(),
			})
		default:
			continue
		}
	}

	if appliedTrade {
		refreshed, err := r.Exchange.OpenOrders(ctx, r.Symbol)
		if err != nil {
			r.alertImportant("reconcile_refresh_open_orders_failed", map[string]string{
				"err": err.Error(),
			})
			return open, err
		}
		open = refreshed
	}
	open = mergeOrders(open, stillOpen)
	return open, nil
}

func (r *LiveRunner) alertImportant(event string, fields map[string]string) {
	if r.Alerts == nil {
		return
	}
	r.Alerts.Important(event, fields)
}

func (r *LiveRunner) persistRuntimeStatus(state string, startedAt time.Time, reconnectAttempts int, disconnectStartedAt time.Time, lastErr error) {
	if r.Store == nil {
		return
	}
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	mode := r.Mode
	if mode == "" {
		mode = "live"
	}
	instanceID := r.InstanceID
	if instanceID == "" {
		instanceID = "default"
	}
	status := store.RuntimeStatus{
		Mode:              mode,
		Symbol:            r.Symbol,
		InstanceID:        instanceID,
		PID:               os.Getpid(),
		State:             state,
		StartedAt:         startedAt,
		ReconnectAttempts: reconnectAttempts,
	}
	if !disconnectStartedAt.IsZero() {
		t := disconnectStartedAt
		status.DisconnectedAt = &t
	}
	if lastErr != nil {
		status.LastError = lastErr.Error()
	}
	if err := r.Store.SaveRuntimeStatus(status); err != nil {
		log.Printf("level=WARN event=runtime_status_write_failed err=%q", err.Error())
	}
}

type seenTracker struct {
	items map[string]time.Time
	queue []seenEntry
	max   int
	ttl   time.Duration
}

type seenEntry struct {
	key string
	at  time.Time
}

func newSeenTracker(max int, ttl time.Duration) *seenTracker {
	if max < 1 {
		max = 1
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &seenTracker{
		items: make(map[string]time.Time, max),
		max:   max,
		ttl:   ttl,
	}
}

func (s *seenTracker) Seen(key string, now time.Time) bool {
	if s == nil || key == "" {
		return false
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	s.prune(now)
	if _, ok := s.items[key]; ok {
		return true
	}
	s.items[key] = now
	s.queue = append(s.queue, seenEntry{key: key, at: now})
	s.prune(now)
	return false
}

func (s *seenTracker) prune(now time.Time) {
	if s == nil {
		return
	}
	expireBefore := now.Add(-s.ttl)
	for len(s.queue) > 0 {
		head := s.queue[0]
		ts, ok := s.items[head.key]
		if !ok || !ts.Equal(head.at) {
			s.queue = s.queue[1:]
			continue
		}
		if s.ttl > 0 && ts.Before(expireBefore) {
			delete(s.items, head.key)
			s.queue = s.queue[1:]
			continue
		}
		if len(s.items) > s.max {
			delete(s.items, head.key)
			s.queue = s.queue[1:]
			continue
		}
		break
	}
}

func mergeOrders(open []core.Order, extra []core.Order) []core.Order {
	if len(extra) == 0 {
		return open
	}
	openByID := make(map[string]struct{}, len(open))
	for _, ord := range open {
		if ord.ID != "" {
			openByID[ord.ID] = struct{}{}
		}
	}
	for _, ord := range extra {
		if ord.ID == "" {
			continue
		}
		if _, ok := openByID[ord.ID]; ok {
			continue
		}
		open = append(open, ord)
		openByID[ord.ID] = struct{}{}
	}
	return open
}

func tradeFromOrder(status binance.OrderQuery, fallback core.Order) core.Trade {
	order := status.Order
	if order.ID == "" {
		order = fallback
	}
	qty := status.ExecutedQty
	if qty.Cmp(decimal.Zero) <= 0 {
		qty = order.Qty
	}
	price := order.Price
	if status.ExecutedQty.Cmp(decimal.Zero) > 0 && status.CumulativeQuoteQty.Cmp(decimal.Zero) > 0 {
		price = status.CumulativeQuoteQty.Div(status.ExecutedQty)
	}
	ts := status.UpdateTime
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	tradeID := ""
	if order.ID != "" {
		tradeID = "reconcile-" + order.ID
	}
	return core.Trade{
		OrderID: order.ID,
		TradeID: tradeID,
		Symbol:  order.Symbol,
		Side:    order.Side,
		Price:   price,
		Qty:     qty,
		Status:  order.Status,
		Time:    ts,
	}
}

func tradeEventKey(trade core.Trade) string {
	if trade.OrderID == "" {
		return ""
	}
	if trade.TradeID != "" {
		return "order:" + trade.OrderID + "|trade:" + trade.TradeID
	}
	key := "order:" + trade.OrderID
	if !trade.Time.IsZero() {
		key += "|time:" + trade.Time.UTC().Format(time.RFC3339Nano)
	}
	if trade.Price.Cmp(decimal.Zero) > 0 {
		key += "|price:" + trade.Price.String()
	}
	if trade.Qty.Cmp(decimal.Zero) > 0 {
		key += "|qty:" + trade.Qty.String()
	}
	return key
}

func tradeLedgerKey(trade core.Trade) string {
	if trade.OrderID == "" || trade.TradeID == "" {
		return ""
	}
	return "order:" + trade.OrderID + "|trade:" + trade.TradeID
}

func (r *LiveRunner) shouldSkipTrade(trade core.Trade, seen *seenTracker, now time.Time) (bool, error) {
	key := tradeEventKey(trade)
	if key != "" && seen != nil && seen.Seen(key, now) {
		return true, nil
	}
	if r.Store == nil {
		return false, nil
	}
	ledgerKey := tradeLedgerKey(trade)
	if ledgerKey == "" {
		return false, nil
	}
	seenBefore, err := r.Store.HasTradeLedgerKey(ledgerKey)
	if err != nil {
		return false, err
	}
	return seenBefore, nil
}

func (r *LiveRunner) recordTradeLedger(trade core.Trade) error {
	if r.Store == nil {
		return nil
	}
	ledgerKey := tradeLedgerKey(trade)
	if ledgerKey == "" {
		return nil
	}
	return r.Store.RecordTradeLedgerKey(ledgerKey, trade.Time)
}

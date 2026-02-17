package alert

import (
	"context"
	"log"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Notifier interface {
	Notify(ctx context.Context, msg string) error
}

type Alerter interface {
	Important(event string, fields map[string]string)
}

const (
	defaultAlertQueueSize     = 128
	defaultDropReportInterval = time.Minute
)

type ManagerOptions struct {
	QueueSize          int
	DropReportInterval time.Duration
}

type Manager struct {
	mode                 string
	symbol               string
	notifier             Notifier
	queue                chan alertEvent
	stop                 chan struct{}
	done                 chan struct{}
	dropReportInterval   time.Duration
	droppedTotal         uint64
	droppedSinceReported uint64
	wg                   sync.WaitGroup
	mu                   sync.RWMutex
	closed               bool
}

type alertEvent struct {
	event  string
	fields map[string]string
}

func NewManager(mode, symbol string, notifier Notifier) *Manager {
	return NewManagerWithOptions(mode, symbol, notifier, ManagerOptions{
		QueueSize:          defaultAlertQueueSize,
		DropReportInterval: defaultDropReportInterval,
	})
}

func NewManagerWithOptions(mode, symbol string, notifier Notifier, opts ManagerOptions) *Manager {
	if notifier == nil {
		return nil
	}
	queueSize := opts.QueueSize
	if queueSize <= 0 {
		queueSize = defaultAlertQueueSize
	}
	reportInterval := opts.DropReportInterval
	if reportInterval < 0 {
		reportInterval = 0
	}
	m := &Manager{
		mode:               mode,
		symbol:             symbol,
		notifier:           notifier,
		queue:              make(chan alertEvent, queueSize),
		stop:               make(chan struct{}),
		done:               make(chan struct{}),
		dropReportInterval: reportInterval,
	}
	m.wg.Add(1)
	go m.loop()
	if m.dropReportInterval > 0 {
		m.wg.Add(1)
		go m.dropReportLoop()
	}
	go func() {
		m.wg.Wait()
		close(m.done)
	}()
	return m
}

func (m *Manager) Important(event string, fields map[string]string) {
	if m == nil || m.notifier == nil {
		return
	}
	ev := alertEvent{
		event:  event,
		fields: cloneFields(fields),
	}
	m.mu.RLock()
	if m.closed {
		m.mu.RUnlock()
		return
	}
	select {
	case m.queue <- ev:
		m.mu.RUnlock()
		return
	default:
		droppedTotal := atomic.AddUint64(&m.droppedTotal, 1)
		droppedInWindow := atomic.AddUint64(&m.droppedSinceReported, 1)
		m.mu.RUnlock()
		// Report first dropped alert in a window immediately; periodic summary emits total drops in window.
		if droppedInWindow == 1 {
			log.Printf(
				"level=WARN event=alert_queue_dropped target_event=%q reason=%q dropped_total=%d queue_len=%d queue_cap=%d",
				event,
				"queue_full",
				droppedTotal,
				len(m.queue),
				cap(m.queue),
			)
		}
	}
}

func (m *Manager) Close(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	close(m.stop)
	done := m.done
	m.mu.Unlock()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) loop() {
	defer m.wg.Done()
	for {
		select {
		case ev := <-m.queue:
			m.send(ev)
		case <-m.stop:
			for {
				select {
				case ev := <-m.queue:
					m.send(ev)
				default:
					m.reportDroppedSummary()
					return
				}
			}
		}
	}
}

func (m *Manager) dropReportLoop() {
	defer m.wg.Done()
	ticker := time.NewTicker(m.dropReportInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			m.reportDroppedSummary()
		case <-m.stop:
			m.reportDroppedSummary()
			return
		}
	}
}

func (m *Manager) reportDroppedSummary() {
	dropped := atomic.SwapUint64(&m.droppedSinceReported, 0)
	if dropped == 0 {
		return
	}
	droppedTotal := atomic.LoadUint64(&m.droppedTotal)
	log.Printf(
		"level=WARN event=alert_queue_dropped_report dropped_since_last=%d dropped_total=%d report_interval_sec=%d queue_len=%d queue_cap=%d",
		dropped,
		droppedTotal,
		int64(m.dropReportInterval/time.Second),
		len(m.queue),
		cap(m.queue),
	)
}

func (m *Manager) droppedStats() (uint64, uint64) {
	if m == nil {
		return 0, 0
	}
	return atomic.LoadUint64(&m.droppedTotal), atomic.LoadUint64(&m.droppedSinceReported)
}

func (m *Manager) send(ev alertEvent) {
	msg := m.buildMessage(ev.event, ev.fields)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := m.notifier.Notify(ctx, msg); err != nil {
		log.Printf("level=ERROR event=alert_notify_failed target_event=%q err=%q", ev.event, err.Error())
	}
}

func (m *Manager) buildMessage(event string, fields map[string]string) string {
	lines := []string{
		"[grid-trading] important",
		"time: " + time.Now().UTC().Format(time.RFC3339),
		"mode: " + m.mode,
		"symbol: " + m.symbol,
		"event: " + event,
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		lines = append(lines, k+": "+fields[k])
	}
	return strings.Join(lines, "\n")
}

func cloneFields(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

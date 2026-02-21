package alert

import (
	"bytes"
	"context"
	"log"
	"strings"
	"sync"
	"testing"
	"time"
)

type notifierSpy struct {
	block   <-chan struct{}
	entered chan struct{}
	once    sync.Once

	mu   sync.Mutex
	msgs []string
}

func (n *notifierSpy) Notify(ctx context.Context, msg string) error {
	if n.entered != nil {
		n.once.Do(func() {
			close(n.entered)
		})
	}
	if n.block != nil {
		select {
		case <-n.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	n.mu.Lock()
	n.msgs = append(n.msgs, msg)
	n.mu.Unlock()
	return nil
}

func (n *notifierSpy) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.msgs)
}

func (n *notifierSpy) first() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.msgs) == 0 {
		return ""
	}
	return n.msgs[0]
}

func TestManagerCloseFlushesQueuedEvents(t *testing.T) {
	spy := &notifierSpy{}
	m := NewManager("live", "BTCUSDT", spy)
	if m == nil {
		t.Fatalf("NewManager() returned nil")
	}

	m.Important("runner_started", map[string]string{"a": "1"})
	m.Important("runner_stopped", map[string]string{"b": "2"})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := m.Close(ctx); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	if spy.count() != 2 {
		t.Fatalf("notified count = %d, want 2", spy.count())
	}
	msg := spy.first()
	if !strings.Contains(msg, "event: runner_started") {
		t.Fatalf("first message missing event, got %q", msg)
	}
}

func TestManagerImportantNonBlockingWhenQueueFull(t *testing.T) {
	block := make(chan struct{})
	spy := &notifierSpy{
		block:   block,
		entered: make(chan struct{}),
	}
	m := NewManager("live", "BTCUSDT", spy)
	if m == nil {
		t.Fatalf("NewManager() returned nil")
	}
	m.Important("seed", nil)
	select {
	case <-spy.entered:
	case <-time.After(300 * time.Millisecond):
		t.Fatalf("notifier did not enter blocked state")
	}

	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			m.Important("spam", map[string]string{"i": "x"})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(300 * time.Millisecond):
		t.Fatalf("Important() appears blocked when queue is full")
	}

	close(block)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := m.Close(ctx); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestManagerTracksDroppedCountAndPendingWindow(t *testing.T) {
	block := make(chan struct{})
	spy := &notifierSpy{
		block:   block,
		entered: make(chan struct{}),
	}
	m := NewManagerWithOptions("live", "BTCUSDT", spy, ManagerOptions{
		QueueSize:          1,
		DropReportInterval: 0,
	})
	if m == nil {
		t.Fatalf("NewManagerWithOptions() returned nil")
	}

	m.Important("seed", nil)
	select {
	case <-spy.entered:
	case <-time.After(time.Second):
		t.Fatalf("notifier did not enter blocked state")
	}

	// 在 notifier 协程阻塞时先填满队列，再触发丢弃。
	m.Important("queue_fill", nil)
	for i := 0; i < 10; i++ {
		m.Important("spam", map[string]string{"i": "x"})
	}

	total, pending := m.droppedStats()
	if total != 10 {
		t.Fatalf("dropped total = %d, want 10", total)
	}
	if pending != 10 {
		t.Fatalf("dropped pending window = %d, want 10", pending)
	}

	close(block)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := m.Close(ctx); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestManagerPeriodicDroppedReportEmitsAndResetsWindow(t *testing.T) {
	var logs bytes.Buffer
	origOutput := log.Writer()
	origFlags := log.Flags()
	log.SetOutput(&logs)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(origOutput)
		log.SetFlags(origFlags)
	}()

	block := make(chan struct{})
	spy := &notifierSpy{
		block:   block,
		entered: make(chan struct{}),
	}
	m := NewManagerWithOptions("live", "BTCUSDT", spy, ManagerOptions{
		QueueSize:          1,
		DropReportInterval: 40 * time.Millisecond,
	})
	if m == nil {
		t.Fatalf("NewManagerWithOptions() returned nil")
	}

	m.Important("seed", nil)
	select {
	case <-spy.entered:
	case <-time.After(time.Second):
		t.Fatalf("notifier did not enter blocked state")
	}

	m.Important("queue_fill", nil)
	for i := 0; i < 3; i++ {
		m.Important("spam", nil)
	}

	deadline := time.Now().Add(800 * time.Millisecond)
	for {
		if strings.Contains(logs.String(), "event=alert_queue_dropped_report") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("missing dropped report log, got logs: %s", logs.String())
		}
		time.Sleep(10 * time.Millisecond)
	}

	_, pending := m.droppedStats()
	if pending != 0 {
		t.Fatalf("dropped pending window = %d, want 0 after periodic report", pending)
	}

	close(block)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := m.Close(ctx); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

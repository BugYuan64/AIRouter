package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// blockingInserter 在信号放行前一直阻塞，用于模拟慢数据库。
type blockingInserter struct {
	release chan struct{}
	mu      sync.Mutex
	batches [][]UsageEvent
}

func (b *blockingInserter) InsertUsage(ctx context.Context, events []UsageEvent) error {
	select {
	case <-b.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	b.mu.Lock()
	cp := make([]UsageEvent, len(events))
	copy(cp, events)
	b.batches = append(b.batches, cp)
	b.mu.Unlock()
	return nil
}

// 队列满时必须立即返回并计入丢弃，绝不阻塞生成请求。
// 这是数据面吞吐优先的核心保证。
func TestRecordNeverBlocksWhenQueueFull(t *testing.T) {
	release := make(chan struct{})
	inserter := &blockingInserter{release: release}
	r := NewRecorder(inserter, RecorderOptions{QueueSize: 4, BatchSize: 1, FlushEvery: time.Millisecond})
	defer func() {
		close(release)
		r.Close()
	}()

	// 先灌满：writer 卡在 InsertUsage，队列填满后继续投递应全部丢弃。
	var worst time.Duration
	for i := 0; i < 500; i++ {
		start := time.Now()
		r.Record(UsageEvent{ClientID: "c", Model: "m", Protocol: ProtocolChat})
		if d := time.Since(start); d > worst {
			worst = d
		}
	}

	// 单次投递应在微秒级。阈值取 10ms：-race 会显著放大计时，
	// 而真正的失败模式（写入端阻塞）会让耗时至秒级，两者量级可区分。
	if worst > 10*time.Millisecond {
		t.Fatalf("Record blocked for %v; must never block", worst)
	}
	if r.Stats().Dropped == 0 {
		t.Fatal("expected drops when queue is full and writer is stuck")
	}
}

// 关闭时必须先刷出队列中已有记录，不能直接丢弃。
func TestCloseFlushesPending(t *testing.T) {
	inserter := &blockingInserter{release: make(chan struct{})}
	close(inserter.release)
	r := NewRecorder(inserter, RecorderOptions{QueueSize: 128, BatchSize: 10, FlushEvery: time.Hour})

	for i := 0; i < 25; i++ {
		r.Record(UsageEvent{ClientID: "c", Model: "m", Protocol: ProtocolChat})
	}
	r.Close()

	if got := r.Stats().Flushed; got != 25 {
		t.Fatalf("want 25 flushed, got %d (dropped=%d)", got, r.Stats().Dropped)
	}
	if got := r.Stats().Dropped; got != 0 {
		t.Fatalf("want 0 dropped, got %d", got)
	}
}

// 写入失败不重试、不 panic，只计数。
type failingInserter struct{ calls int32 }

func (f *failingInserter) InsertUsage(context.Context, []UsageEvent) error {
	f.calls++
	return errors.New("database unavailable")
}

func TestInsertFailureIsCountedNotFatal(t *testing.T) {
	r := NewRecorder(&failingInserter{}, RecorderOptions{QueueSize: 64, BatchSize: 5, FlushEvery: 5 * time.Millisecond})
	for i := 0; i < 10; i++ {
		r.Record(UsageEvent{ClientID: "c", Model: "m", Protocol: ProtocolChat})
	}
	// 等待至少一次刷盘
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r.Stats().Failed > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	r.Close()
	if r.Stats().Failed == 0 {
		t.Fatal("insert failures must be counted")
	}
	if r.Stats().Flushed != 0 {
		t.Fatal("failed batches must not count as flushed")
	}
}

// 慢数据库不能拖住调用方：即使 InsertUsage 阻塞，Record 仍立即返回。
func TestRecordLatencyUnaffectedBySlowDatabase(t *testing.T) {
	release := make(chan struct{})
	r := NewRecorder(&blockingInserter{release: release}, RecorderOptions{
		QueueSize: 4096, BatchSize: 1, FlushEvery: time.Hour,
	})
	var worst time.Duration
	for i := 0; i < 1000; i++ {
		start := time.Now()
		r.Record(UsageEvent{ClientID: "c", Model: "m", Protocol: ProtocolChat})
		if d := time.Since(start); d > worst {
			worst = d
		}
	}
	close(release)
	r.Close()
	// 同上：10ms 阈值在 -race 下仍远低于「被慢数据库阻塞」的量级。
	if worst > 10*time.Millisecond {
		t.Fatalf("worst Record latency %v with slow DB; must stay non-blocking", worst)
	}
}

// 未知 token 与成本必须保持 nil，不能变成 0。
func TestUnknownUsageFieldsStayNil(t *testing.T) {
	var captured []UsageEvent
	inserter := &captureInserter{captured: &captured}
	r := NewRecorder(inserter, RecorderOptions{QueueSize: 8, BatchSize: 1, FlushEvery: time.Hour})
	r.Record(UsageEvent{ClientID: "c", Model: "m", Protocol: ProtocolChat, Status: 200})
	r.Close()

	if len(captured) != 1 {
		t.Fatalf("want 1 event, got %d", len(captured))
	}
	e := captured[0]
	if e.InputTokens != nil || e.OutputTokens != nil || e.CostMicros != nil || e.CacheTokens != nil {
		t.Fatal("unknown token/cost fields must be nil, never zero")
	}
	if !e.CreatedAt.After(time.Now().Add(-time.Minute)) {
		t.Fatal("CreatedAt must be filled automatically")
	}
}

type captureInserter struct{ captured *[]UsageEvent }

func (c *captureInserter) InsertUsage(_ context.Context, events []UsageEvent) error {
	*c.captured = append(*c.captured, events...)
	return nil
}

// Close 可重复调用且不 panic。
func TestCloseIsIdempotent(t *testing.T) {
	r := NewRecorder(nil, RecorderOptions{})
	r.Close()
	r.Close()
}

// 协议值必须与数据库 CHECK 约束一致，否则写入会被 PostgreSQL 拒绝。
func TestProtocolValidMatchesSchema(t *testing.T) {
	for _, p := range []Protocol{ProtocolChat, ProtocolResponses, ProtocolMessages, ProtocolCountTokens} {
		if !p.Valid() {
			t.Errorf("%s should be valid", p)
		}
	}
	for _, p := range []Protocol{"", "unknown", "CHAT", "embedding"} {
		if p.Valid() {
			t.Errorf("%q should be invalid", p)
		}
	}
}

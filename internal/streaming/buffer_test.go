package streaming

import (
	"context"
	"sync"
	"testing"
	"time"
)

// slowSender is a FrameSender that blocks until told to proceed.
type slowSender struct {
	mu     sync.Mutex
	frames []*Frame
	ready  chan struct{}
}

func newSlowSender() *slowSender {
	return &slowSender{ready: make(chan struct{})}
}

func (s *slowSender) SendFrame(f *Frame) error {
	<-s.ready
	s.mu.Lock()
	defer s.mu.Unlock()
	s.frames = append(s.frames, f)
	return nil
}

func (s *slowSender) release() {
	close(s.ready)
}

func TestBackpressureBuffer_BasicWriteAndDrain(t *testing.T) {
	ms := &threadSafeSender{}
	// Use lowWM=0 so the drain loop fully drains all items.
	b := NewBackpressureBuffer(ms, 100, 80, 0)

	for i := 0; i < 5; i++ {
		if err := b.Write(NewStdoutFrame("msg", "e1")); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	// Wait for drain
	if err := b.WaitDrain(context.Background()); err != nil {
		t.Fatalf("WaitDrain: %v", err)
	}
	b.Stop()

	stats := b.Stats()
	if stats.TotalReceived != 5 {
		t.Errorf("TotalReceived = %d, want 5", stats.TotalReceived)
	}
	if ms.count() != 5 {
		t.Errorf("sender got %d frames, want 5", ms.count())
	}
	if stats.TotalSent != 5 {
		t.Errorf("TotalSent = %d, want 5", stats.TotalSent)
	}
	if stats.TotalDropped != 0 {
		t.Errorf("TotalDropped = %d, want 0", stats.TotalDropped)
	}
}

func TestBackpressureBuffer_HighWatermarkDrops(t *testing.T) {
	// Use a slow sender so frames accumulate
	ss := newSlowSender()
	b := NewBackpressureBuffer(ss, 10, 5, 1)

	// Write 8 frames; high watermark is 5, so frames 6-8 should be dropped
	for i := 0; i < 8; i++ {
		if err := b.Write(NewKeepAliveFrame()); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}

	// Release sender and wait for drain
	ss.release()
	b.WaitDrain(context.Background())
	b.Stop()

	stats := b.Stats()
	if stats.TotalReceived != 8 {
		t.Errorf("TotalReceived = %d, want 8", stats.TotalReceived)
	}
	if stats.TotalSent > 5 {
		t.Errorf("TotalSent = %d, want <= 5 (high watermark)", stats.TotalSent)
	}
	if stats.TotalDropped == 0 {
		t.Error("expected some frames to be dropped")
	}
}

func TestBackpressureBuffer_WriteAfterStop(t *testing.T) {
	ms := &mockSender{}
	b := NewBackpressureBuffer(ms, 10, 10, 5)
	b.Stop()

	err := b.Write(NewKeepAliveFrame())
	if err == nil {
		t.Error("expected error when writing to stopped buffer")
	}
}

func TestBackpressureBuffer_StopIsIdempotent(t *testing.T) {
	ms := &mockSender{}
	b := NewBackpressureBuffer(ms, 10, 10, 5)
	b.Write(NewKeepAliveFrame())
	b.Stop()
	b.Stop() // should not panic
}

func TestBackpressureBuffer_Resize(t *testing.T) {
	ss := newSlowSender()
	b := NewBackpressureBuffer(ss, 10, 10, 5)

	// Write a few frames
	for i := 0; i < 3; i++ {
		b.Write(NewKeepAliveFrame())
	}

	// Resize to larger buffer
	b.Resize(50, 40, 10)

	// Release sender, wait for drain
	ss.release()
	b.WaitDrain(context.Background())
	b.Stop()

	stats := b.Stats()
	if stats.TotalReceived != 3 {
		t.Errorf("TotalReceived = %d, want 3", stats.TotalReceived)
	}
}

func TestBackpressureBuffer_ResizeSmaller(t *testing.T) {
	ss := newSlowSender()
	b := NewBackpressureBuffer(ss, 20, 20, 10)

	for i := 0; i < 10; i++ {
		b.Write(NewKeepAliveFrame())
	}

	// Resize to smaller - some frames should be dropped
	b.Resize(3, 3, 1)

	ss.release()
	b.WaitDrain(context.Background())
	b.Stop()

	stats := b.Stats()
	// We sent 10 but new buffer is only 3, so at most 3 should survive
	if stats.TotalSent > 3 {
		t.Errorf("TotalSent = %d, want <= 3 after resize", stats.TotalSent)
	}
}

func TestBackpressureBuffer_WaitDrainTimeout(t *testing.T) {
	ss := newSlowSender()
	b := NewBackpressureBuffer(ss, 10, 10, 5)
	b.Write(NewKeepAliveFrame())

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := b.WaitDrain(ctx)
	if err == nil {
		t.Error("expected timeout error from WaitDrain")
	}

	ss.release()
	b.Stop()
}

func TestBackpressureBuffer_StartExplicit(t *testing.T) {
	ts := &threadSafeSender{}
	b := NewBackpressureBuffer(ts, 100, 80, 20)

	ctx := context.Background()
	b.Start(ctx)
	b.Start(ctx) // second call should be no-op

	b.Write(NewKeepAliveFrame())
	b.WaitDrain(context.Background())
	b.Stop()

	if ts.count() < 1 {
		t.Errorf("expected at least 1 frame sent, got %d", ts.count())
	}
}

func TestBackpressureBuffer_Stats(t *testing.T) {
	ts := &threadSafeSender{}
	b := NewBackpressureBuffer(ts, 100, 80, 20)

	stats := b.Stats()
	if stats.TotalReceived != 0 || stats.TotalSent != 0 || stats.TotalDropped != 0 {
		t.Error("initial stats should be all zeros")
	}

	b.Write(NewKeepAliveFrame())
	b.WaitDrain(context.Background())
	b.Stop()

	stats = b.Stats()
	if stats.TotalReceived != 1 {
		t.Errorf("TotalReceived = %d, want 1", stats.TotalReceived)
	}
	if stats.TotalSent != 1 {
		t.Errorf("TotalSent = %d, want 1", stats.TotalSent)
	}
}

func TestBackpressureBuffer_Defaults(t *testing.T) {
	ts := &threadSafeSender{}
	// Zero/negative values should get sensible defaults
	b := NewBackpressureBuffer(ts, 0, -1, -1)

	b.Write(NewKeepAliveFrame())
	b.WaitDrain(context.Background())
	b.Stop()

	if ts.count() != 1 {
		t.Errorf("expected 1 frame sent, got %d", ts.count())
	}
}

func TestBackpressureBuffer_PanicOnNilSender(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic when sender is nil")
		}
	}()
	NewBackpressureBuffer(nil, 10, 10, 5)
}

func TestNewBackpressureBuffer_Validation(t *testing.T) {
	ts := &threadSafeSender{}

	// lowWM > highWM should be adjusted
	b := NewBackpressureBuffer(ts, 100, 50, 90)
	b.Write(NewKeepAliveFrame())
	b.WaitDrain(context.Background())
	b.Stop()

	stats := b.Stats()
	if stats.TotalSent != 1 {
		t.Errorf("expected 1 frame sent, got %d", stats.TotalSent)
	}
}

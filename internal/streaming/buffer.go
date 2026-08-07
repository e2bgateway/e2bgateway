package streaming

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
)

// BufferStats holds counters for a BackpressureBuffer.
type BufferStats struct {
	TotalReceived int64
	TotalSent     int64
	TotalDropped  int64
}

// BackpressureBuffer accepts writes unconditionally, then drains them through
// the underlying FrameSender. When the buffered count exceeds HighWatermark
// new writes are dropped (counted in stats). The drain goroutine pauses once
// the buffer drops to or below LowWatermark.
type BackpressureBuffer struct {
	sender         FrameSender
	highWatermark  int
	lowWatermark   int
	buffer         chan *Frame
	mu             sync.Mutex
	running        bool
	stopped        bool
	stopCh         chan struct{}
	wg             sync.WaitGroup
	stats          BufferStats
}

// NewBackpressureBuffer creates a buffer that writes to sender.
// bufferCapacity is the maximum number of items the internal channel can hold.
// highWM / lowWM control when frames are dropped and when drain pauses.
// If highWM <= 0 or lowWM < 0 or lowWM > highWM, sensible defaults are used.
func NewBackpressureBuffer(sender FrameSender, bufferCapacity, highWM, lowWM int) *BackpressureBuffer {
	if sender == nil {
		panic("streaming: sender must not be nil")
	}
	if bufferCapacity <= 0 {
		bufferCapacity = 1024
	}
	if highWM <= 0 || highWM > bufferCapacity {
		highWM = bufferCapacity
	}
	if lowWM < 0 || lowWM > highWM {
		lowWM = highWM / 2
	}
	return &BackpressureBuffer{
		sender:        sender,
		highWatermark: highWM,
		lowWatermark:  lowWM,
		buffer:        make(chan *Frame, bufferCapacity),
		stopCh:        make(chan struct{}),
	}
}

// Write appends a frame to the buffer. If the buffer is at or above the high
// watermark the frame is dropped and the drop counter is incremented. A drain
// goroutine is spawned if one is not already running.
func (b *BackpressureBuffer) Write(frame *Frame) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.stopped {
		return fmt.Errorf("buffer is stopped")
	}

	atomic.AddInt64(&b.stats.TotalReceived, 1)

	if len(b.buffer) >= b.highWatermark {
		atomic.AddInt64(&b.stats.TotalDropped, 1)
		return nil
	}

	b.buffer <- frame

	if !b.running {
		b.running = true
		b.wg.Add(1)
		go b.drainLoop()
	}
	return nil
}

// Start is an alias for the implicit start that happens on the first Write.
// It is provided for symmetry with Stop. Calling Start more than once before
// Stop is a no-op.
func (b *BackpressureBuffer) Start(ctx context.Context) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.running || b.stopped {
		return
	}
	b.running = true
	b.wg.Add(1)
	go b.drainLoopCtx(ctx)
}

// Stop signals the drain goroutine to exit and waits for it to finish.
// It is safe to call Stop multiple times.
func (b *BackpressureBuffer) Stop() {
	b.mu.Lock()
	wasStopped := b.stopped
	b.stopped = true
	b.mu.Unlock()

	if wasStopped {
		return
	}

	close(b.stopCh)
	b.wg.Wait()
}

// WaitDrain blocks until the drain goroutine finishes. If no drain is active
// it returns immediately.
func (b *BackpressureBuffer) WaitDrain(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		b.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Resize atomically replaces the internal buffer with one of the new capacity.
// Existing items are preserved (subject to the new capacity) and forwarded to
// the new channel. Watermarks are updated.
func (b *BackpressureBuffer) Resize(newCapacity, newHighWM, newLowWM int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if newCapacity <= 0 {
		newCapacity = cap(b.buffer)
	}
	if newHighWM <= 0 || newHighWM > newCapacity {
		newHighWM = newCapacity
	}
	if newLowWM < 0 || newLowWM > newHighWM {
		newLowWM = newHighWM / 2
	}

	newBuf := make(chan *Frame, newCapacity)
	oldBuf := b.buffer
	b.buffer = newBuf
	b.highWatermark = newHighWM
	b.lowWatermark = newLowWM

	// Forward surviving items from the old buffer into the new one. Items that
	// do not fit are counted as drops.
	go forwardItems(oldBuf, newBuf, &b.stats)
}

// Stats returns a snapshot of the buffer statistics.
func (b *BackpressureBuffer) Stats() BufferStats {
	return BufferStats{
		TotalReceived: atomic.LoadInt64(&b.stats.TotalReceived),
		TotalSent:     atomic.LoadInt64(&b.stats.TotalSent),
		TotalDropped:  atomic.LoadInt64(&b.stats.TotalDropped),
	}
}

// drainLoop runs until the buffer is empty (or at/below low watermark) and then
// exits. It is re-spawned by Write whenever new items arrive while idle.
func (b *BackpressureBuffer) drainLoop() {
	defer b.wg.Done()
	defer func() {
		b.mu.Lock()
		b.running = false
		b.mu.Unlock()
	}()

	for {
		select {
		case <-b.stopCh:
			b.flushRemaining()
			return
		default:
		}

		b.mu.Lock()
		n := len(b.buffer)
		buf := b.buffer // Capture buffer reference while holding lock
		b.mu.Unlock()

		if n == 0 {
			return
		}

		select {
		case frame := <-buf:
			if err := b.sender.SendFrame(frame); err != nil {
				// Sender error; stop draining but mark as not running so a
				// future Write can try again (though it will also fail).
				return
			}
			atomic.AddInt64(&b.stats.TotalSent, 1)
		case <-b.stopCh:
			b.flushRemaining()
			return
		}

		b.mu.Lock()
		shouldStop := len(b.buffer) <= b.lowWatermark
		b.mu.Unlock()

		if shouldStop {
			return
		}
	}
}

// drainLoopCtx is like drainLoop but also exits when the context is canceled.
func (b *BackpressureBuffer) drainLoopCtx(ctx context.Context) {
	defer b.wg.Done()
	defer func() {
		b.mu.Lock()
		b.running = false
		b.mu.Unlock()
	}()

	for {
		select {
		case <-b.stopCh:
			b.flushRemaining()
			return
		case <-ctx.Done():
			b.flushRemaining()
			return
		default:
		}

		b.mu.Lock()
		n := len(b.buffer)
		buf := b.buffer // Capture buffer reference while holding lock
		b.mu.Unlock()

		if n == 0 {
			return
		}

		select {
		case frame := <-buf:
			if err := b.sender.SendFrame(frame); err != nil {
				return
			}
			atomic.AddInt64(&b.stats.TotalSent, 1)
		case <-b.stopCh:
			b.flushRemaining()
			return
		case <-ctx.Done():
			b.flushRemaining()
			return
		}

		b.mu.Lock()
		shouldStop := len(b.buffer) <= b.lowWatermark
		b.mu.Unlock()

		if shouldStop {
			return
		}
	}
}

// flushRemaining sends every frame left in the buffer, ignoring errors.
func (b *BackpressureBuffer) flushRemaining() {
	b.mu.Lock()
	buf := b.buffer // Capture buffer reference while holding lock
	b.mu.Unlock()

	for {
		select {
		case frame := <-buf:
			_ = b.sender.SendFrame(frame)
			atomic.AddInt64(&b.stats.TotalSent, 1)
		default:
			return
		}
	}
}

// forwardItems moves items from old to new, counting overflow as drops.
func forwardItems(src, dst chan *Frame, stats *BufferStats) {
	for {
		select {
		case item := <-src:
			select {
			case dst <- item:
			default:
				atomic.AddInt64(&stats.TotalDropped, 1)
			}
		default:
			return
		}
	}
}

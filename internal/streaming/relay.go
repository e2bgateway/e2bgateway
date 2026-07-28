package streaming

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Relay manages bidirectional frame forwarding between a client and a backend.
// It copies frames arriving on the client sender to the backend sender, and
// vice-versa. A keep-alive heartbeat is sent to both sides at a configurable
// interval to keep the connections alive.
type Relay struct {
	client      FrameSender
	backend     FrameSender
	fromClient  chan *Frame
	fromBackend chan *Frame
	stopCh      chan struct{}
	wg          sync.WaitGroup
	mu          sync.Mutex
	started     bool
	stopped     bool
	connections int64
	keepAlive   time.Duration
}

// RelayConfig holds configuration for a Relay.
type RelayConfig struct {
	KeepAliveInterval time.Duration
}

// NewRelay creates a Relay that forwards frames between client and backend.
// Both senders must be non-nil. Use Start() to begin forwarding.
func NewRelay(client, backend FrameSender, cfg RelayConfig) *Relay {
	if cfg.KeepAliveInterval <= 0 {
		cfg.KeepAliveInterval = 30 * time.Second
	}
	return &Relay{
		client:      client,
		backend:     backend,
		fromClient:  make(chan *Frame, 256),
		fromBackend: make(chan *Frame, 256),
		stopCh:      make(chan struct{}),
		keepAlive:   cfg.KeepAliveInterval,
	}
}

// ClientSender returns a FrameSender that queues frames read from the client
// side and forwarded to the backend.
func (r *Relay) ClientSender() FrameSender {
	return &relaySideSender{ch: r.fromClient}
}

// BackendSender returns a FrameSender that queues frames read from the backend
// side and forwarded to the client.
func (r *Relay) BackendSender() FrameSender {
	return &relaySideSender{ch: r.fromBackend}
}

// relaySideSender is a FrameSender that enqueues frames into a channel.
type relaySideSender struct {
	ch chan *Frame
}

func (s *relaySideSender) SendFrame(f *Frame) error {
	select {
	case s.ch <- f:
		return nil
	default:
		return fmt.Errorf("relay channel full, frame dropped")
	}
}

// Start begins forwarding frames in both directions. It blocks until the relay
// is stopped, both sides are closed, or the context is canceled. Start must
// be called at most once.
func (r *Relay) Start(ctx context.Context) error {
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return fmt.Errorf("relay already started")
	}
	select {
	case <-r.stopCh:
		r.mu.Unlock()
		return fmt.Errorf("relay already stopped")
	default:
	}
	r.started = true
	r.mu.Unlock()

	atomic.AddInt64(&r.connections, 1)
	defer atomic.AddInt64(&r.connections, -1)

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.relayLoop(ctx)
	}()

	r.wg.Wait()
	return nil
}

// Stop signals the relay to shut down gracefully. It waits for all goroutines
// to finish, ensuring in-flight frames are drained before returning.
func (r *Relay) Stop() {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return
	}
	r.stopped = true
	r.mu.Unlock()

	close(r.stopCh)
	r.wg.Wait()
}

// ActiveConnections returns the number of currently active relay loops.
func (r *Relay) ActiveConnections() int64 {
	return atomic.LoadInt64(&r.connections)
}

// relayLoop is the main forwarding loop. It reads from both side channels and
// forwards to the corresponding sender. It exits when both sides are closed or
// when the stop channel fires.
func (r *Relay) relayLoop(ctx context.Context) {
	clientClosed := false
	backendClosed := false

	// keepAliveTicker may be nil if keepAlive is zero or very large.
	var kaTicker *time.Ticker
	var kaCh <-chan time.Time
	if r.keepAlive > 0 {
		kaTicker = time.NewTicker(r.keepAlive)
		kaCh = kaTicker.C
		defer kaTicker.Stop()
	}

	for {
		if clientClosed && backendClosed {
			return
		}

		select {
		case frame, ok := <-r.fromClient:
			if !ok {
				clientClosed = true
				continue
			}
			if err := r.backend.SendFrame(frame); err != nil {
				backendClosed = true
			}

		case frame, ok := <-r.fromBackend:
			if !ok {
				backendClosed = true
				continue
			}
			if err := r.client.SendFrame(frame); err != nil {
				clientClosed = true
			}

		case <-kaCh:
			_ = r.client.SendFrame(NewKeepAliveFrame())
			_ = r.backend.SendFrame(NewKeepAliveFrame())

		case <-r.stopCh:
			// Drain remaining frames before exiting.
			r.drainChannel(r.fromClient, r.backend)
			r.drainChannel(r.fromBackend, r.client)
			return

		case <-ctx.Done():
			r.drainChannel(r.fromClient, r.backend)
			r.drainChannel(r.fromBackend, r.client)
			return
		}
	}
}

// drainChannel forwards all buffered frames from src to dst, then returns.
func (r *Relay) drainChannel(src chan *Frame, dst FrameSender) {
	for {
		select {
		case frame := <-src:
			_ = dst.SendFrame(frame)
		default:
			return
		}
	}
}

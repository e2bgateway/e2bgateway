package streaming

import (
	"context"
	"sync"
	"testing"
	"time"
)

// threadSafeSender is a FrameSender that records frames with a mutex.
type threadSafeSender struct {
	mu     sync.Mutex
	frames []*Frame
}

func (s *threadSafeSender) SendFrame(f *Frame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.frames = append(s.frames, f)
	return nil
}

func (s *threadSafeSender) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.frames)
}

func (s *threadSafeSender) getFrames() []*Frame {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Frame, len(s.frames))
	copy(out, s.frames)
	return out
}

func TestRelay_BidirectionalForwarding(t *testing.T) {
	clientOut := &threadSafeSender{}
	backendOut := &threadSafeSender{}

	r := NewRelay(clientOut, backendOut, RelayConfig{KeepAliveInterval: 1 * time.Hour})

	// Enqueue frames from "client" -> should go to backendOut
	cs := r.ClientSender()
	_ = cs.SendFrame(NewStdoutFrame("from-client", "e1"))

	// Enqueue frames from "backend" -> should go to clientOut
	bs := r.BackendSender()
	_ = bs.SendFrame(NewStdoutFrame("from-backend", "e2"))

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- r.Start(ctx)
	}()

	// Wait a bit for relay to process
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after context cancel")
	}

	if backendOut.count() < 1 {
		t.Errorf("expected at least 1 frame forwarded to backend, got %d", backendOut.count())
	}
	if clientOut.count() < 1 {
		t.Errorf("expected at least 1 frame forwarded to client, got %d", clientOut.count())
	}

	// Verify the content was forwarded correctly
	backendFrames := backendOut.getFrames()
	found := false
	for _, f := range backendFrames {
		if f.Type == FrameStdout {
			d, _ := f.Marshal()
			if len(d) > 0 {
				found = true
			}
		}
	}
	if !found {
		t.Error("expected a stdout frame forwarded to backend")
	}
}

func TestRelay_StopDrainsFrames(t *testing.T) {
	clientOut := &threadSafeSender{}
	backendOut := &threadSafeSender{}

	r := NewRelay(clientOut, backendOut, RelayConfig{KeepAliveInterval: 0})

	// Queue several frames before starting
	cs := r.ClientSender()
	for i := 0; i < 5; i++ {
		_ = cs.SendFrame(NewStdoutFrame("msg", "e1"))
	}

	done := make(chan error, 1)
	go func() {
		done <- r.Start(context.Background())
	}()

	time.Sleep(20 * time.Millisecond)
	r.Stop()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after Stop")
	}

	// All frames should have been drained to backend
	if backendOut.count() < 5 {
		t.Errorf("expected at least 5 frames drained to backend, got %d", backendOut.count())
	}
}

func TestRelay_DoubleStartReturnsError(t *testing.T) {
	clientOut := &threadSafeSender{}
	backendOut := &threadSafeSender{}
	r := NewRelay(clientOut, backendOut, RelayConfig{})

	done := make(chan error, 1)
	go func() {
		done <- r.Start(context.Background())
	}()

	time.Sleep(20 * time.Millisecond)

	// Second start should fail
	err := make(chan error, 1)
	go func() {
		err <- r.Start(context.Background())
	}()

	select {
	case e := <-err:
		if e == nil {
			t.Error("expected error on double start")
		}
	case <-time.After(time.Second):
		t.Fatal("double start did not return")
	}

	r.Stop()
	<-done
}

func TestRelay_StopIsIdempotent(t *testing.T) {
	clientOut := &threadSafeSender{}
	backendOut := &threadSafeSender{}
	r := NewRelay(clientOut, backendOut, RelayConfig{})

	done := make(chan error, 1)
	go func() {
		done <- r.Start(context.Background())
	}()
	time.Sleep(20 * time.Millisecond)

	r.Stop()
	r.Stop() // should not panic
	<-done
}

func TestRelay_ActiveConnections(t *testing.T) {
	clientOut := &threadSafeSender{}
	backendOut := &threadSafeSender{}
	r := NewRelay(clientOut, backendOut, RelayConfig{})

	if r.ActiveConnections() != 0 {
		t.Errorf("expected 0 active connections before start, got %d", r.ActiveConnections())
	}

	done := make(chan error, 1)
	go func() {
		done <- r.Start(context.Background())
	}()

	time.Sleep(20 * time.Millisecond)
	if r.ActiveConnections() != 1 {
		t.Errorf("expected 1 active connection, got %d", r.ActiveConnections())
	}

	r.Stop()
	<-done

	if r.ActiveConnections() != 0 {
		t.Errorf("expected 0 active connections after stop, got %d", r.ActiveConnections())
	}
}

func TestRelay_ContextCancellation(t *testing.T) {
	clientOut := &threadSafeSender{}
	backendOut := &threadSafeSender{}
	r := NewRelay(clientOut, backendOut, RelayConfig{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- r.Start(ctx)
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after context cancel")
	}
}

func TestRelaySideSender_ChannelFull(t *testing.T) {
	ch := make(chan *Frame, 1)
	s := &relaySideSender{ch: ch}

	// Fill the channel
	ch <- NewKeepAliveFrame()

	// Next send should fail
	err := s.SendFrame(NewKeepAliveFrame())
	if err == nil {
		t.Error("expected error when relay channel is full")
	}
}

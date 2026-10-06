package app

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func waitOutbox(t *testing.T, o *realtimeOutbox) {
	t.Helper()
	select {
	case <-o.done:
	case <-time.After(time.Second):
		t.Fatal("writer did not exit")
	}
}

func TestRealtimeOutboxDoesNotBlockOnSlowClient(t *testing.T) {
	started := make(chan struct{})
	var aborted atomic.Int32
	o := newRealtimeOutbox(context.Background(), func(ctx context.Context, _ any) error { close(started); <-ctx.Done(); return ctx.Err() }, func() { aborted.Add(1) })
	defer o.stop()
	if err := o.enqueue(context.Background(), realtimePacket{value: "first"}); err != nil {
		t.Fatal(err)
	}
	<-started
	for i := 0; i < realtimeQueueCapacity; i++ {
		if err := o.enqueue(context.Background(), realtimePacket{value: i}); err != nil {
			t.Fatal(err)
		}
	}
	if err := o.enqueue(context.Background(), realtimePacket{value: "overflow"}); !errors.Is(err, errRealtimeBackpressure) {
		t.Fatalf("overflow: %v", err)
	}
	waitOutbox(t, o)
	if aborted.Load() != 1 {
		t.Fatalf("abort calls: %d", aborted.Load())
	}
	if err := o.enqueue(context.Background(), realtimePacket{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("stopped queue accepted message: %v", err)
	}
}

func TestRealtimeOutboxPreservesMessagesBeforeClose(t *testing.T) {
	received := make(chan string, 3)
	o := newRealtimeOutbox(context.Background(), func(_ context.Context, v any) error { received <- v.(string); return nil }, func() {})
	defer o.stop()
	for _, message := range []string{"snapshot", "accepted"} {
		if err := o.enqueue(context.Background(), realtimePacket{value: message}); err != nil {
			t.Fatal(err)
		}
	}
	if err := o.enqueue(context.Background(), realtimePacket{value: "replaced", after: func() { close(received) }}); err != nil {
		t.Fatal(err)
	}
	waitOutbox(t, o)
	for _, want := range []string{"snapshot", "accepted", "replaced"} {
		if got := <-received; got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	}
	if _, ok := <-received; ok {
		t.Fatal("close callback did not run")
	}
}

func TestRealtimeOutboxStopsAfterWriteFailure(t *testing.T) {
	o := newRealtimeOutbox(context.Background(), func(context.Context, any) error { return errors.New("socket failed") }, func() {})
	defer o.stop()
	if err := o.enqueue(context.Background(), realtimePacket{}); err != nil {
		t.Fatal(err)
	}
	waitOutbox(t, o)
	if err := o.enqueue(context.Background(), realtimePacket{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("failed socket accepted message: %v", err)
	}
}

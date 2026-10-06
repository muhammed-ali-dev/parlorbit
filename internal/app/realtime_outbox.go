package app

import (
	"context"
	"errors"
	"sync"
)

const realtimeQueueCapacity = 32

var errRealtimeBackpressure = errors.New("realtime client fell behind")

type realtimePacket struct {
	value any
	after func()
}

// Each connection has one writer. Producers never wait for socket I/O.
// Overflow closes the connection; reconnect supplies a complete snapshot.
type realtimeOutbox struct {
	ctx    context.Context
	cancel context.CancelFunc
	queue  chan realtimePacket
	done   chan struct{}
	once   sync.Once
	abort  func()
}

func newRealtimeOutbox(parent context.Context, send func(context.Context, any) error, abort func()) *realtimeOutbox {
	ctx, cancel := context.WithCancel(parent)
	o := &realtimeOutbox{ctx: ctx, cancel: cancel, queue: make(chan realtimePacket, realtimeQueueCapacity), done: make(chan struct{}), abort: abort}
	go func() {
		defer close(o.done)
		defer o.stop()
		for {
			select {
			case <-ctx.Done():
				return
			case packet := <-o.queue:
				if err := send(ctx, packet.value); err != nil {
					return
				}
				if packet.after != nil {
					packet.after()
					return
				}
			}
		}
	}()
	return o
}

func (o *realtimeOutbox) stop() { o.once.Do(func() { o.cancel(); o.abort() }) }

func (o *realtimeOutbox) enqueue(ctx context.Context, packet realtimePacket) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := o.ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-o.ctx.Done():
		return o.ctx.Err()
	case o.queue <- packet:
		return nil
	default:
		o.stop()
		return errRealtimeBackpressure
	}
}

// Package sharedflight coalesces concurrent loads while retaining independent
// caller cancellation. Results must be immutable or copied by each caller.
package sharedflight

import (
	"context"
	"errors"
	"sync"
)

type call[V any] struct {
	done       chan struct{}
	cancel     context.CancelFunc
	waiters    int
	value      V
	err        error
	panicValue any
}

type Group[K comparable, V any] struct {
	mu    sync.Mutex
	calls map[K]*call[V]
}

func (g *Group[K, V]) Do(ctx context.Context, key K, fn func(context.Context) (V, error)) (V, error) {
	var zero V
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	g.mu.Lock()
	if g.calls == nil {
		g.calls = make(map[K]*call[V])
	}
	c := g.calls[key]
	if c == nil {
		loadCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		c = &call[V]{done: make(chan struct{}), cancel: cancel}
		g.calls[key] = c
		go func() {
			defer cancel()
			completed := false
			var value V
			var err error
			defer func() {
				failure := recover()
				if !completed && failure == nil {
					err = errors.New("shared load exited without returning")
				}
				g.mu.Lock()
				c.value, c.err, c.panicValue = value, err, failure
				if g.calls[key] == c {
					delete(g.calls, key)
				}
				close(c.done)
				g.mu.Unlock()
			}()
			value, err = fn(loadCtx)
			completed = true
		}()
	}
	c.waiters++
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		c.waiters--
		if c.waiters == 0 {
			if g.calls[key] == c {
				delete(g.calls, key)
			}
			c.cancel()
		}
		g.mu.Unlock()
	}()
	select {
	case <-ctx.Done():
		return zero, ctx.Err()
	case <-c.done:
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		if c.panicValue != nil {
			panic(c.panicValue)
		}
		return c.value, c.err
	}
}

func (g *Group[K, V]) Cancel() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for key, c := range g.calls {
		delete(g.calls, key)
		c.cancel()
	}
}

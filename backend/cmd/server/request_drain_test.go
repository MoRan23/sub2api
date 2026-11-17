package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRequestDrainWaitsForFinalWorkAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, finalWork, release, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	d := &requestDrain{next: http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(finalWork)
		<-release // A synchronous mandatory settlement still owns storage.
	})}
	go d.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil).WithContext(ctx))
	<-entered
	d.seal()
	cancel()
	<-finalWork
	go func() { d.wait(); close(stopped) }()
	select {
	case <-stopped:
		t.Fatal("drained before the handler completed its final work")
	case <-time.After(20 * time.Millisecond):
	}
	w := httptest.NewRecorder()
	d.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	once.Do(func() { close(release) })
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("drain did not finish")
	}
}

func TestRequestDrainConcurrentAdmissionAndSeal(t *testing.T) {
	d := &requestDrain{next: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })}
	var callers sync.WaitGroup
	for range 64 {
		callers.Add(1)
		go func() {
			defer callers.Done()
			d.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
		}()
	}
	d.seal()
	d.wait()
	callers.Wait()
}

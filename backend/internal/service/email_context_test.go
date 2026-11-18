package service

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSMTPContextCancelsAuthentication(t *testing.T) {
	conn, server := net.Pipe()
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	authStarted := make(chan struct{})
	go func() {
		fmt.Fprint(server, "220 localhost ESMTP\r\n")
		reader := bufio.NewReader(server)
		line, err := reader.ReadString('\n')
		if err != nil || !strings.HasPrefix(line, "EHLO ") {
			return
		}
		fmt.Fprint(server, "250-localhost\r\n250 AUTH PLAIN\r\n")
		line, err = reader.ReadString('\n')
		if err == nil && strings.HasPrefix(line, "AUTH ") {
			close(authStarted)
		}
	}()
	client, err := newSMTPClientContext(ctx, conn, "localhost")
	require.NoError(t, err)
	defer client.Close()
	done := make(chan error, 1)
	go func() { done <- client.Auth(smtp.PlainAuth("", "test", "test", "localhost")) }()
	select {
	case <-authStarted:
	case <-time.After(time.Second):
		t.Fatal("AUTH not sent")
	}
	cancel()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("SMTP AUTH ignored cancellation")
	}
}

func TestSMTPContextCancelsGreetingWithoutDeadline(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := newSMTPClientContext(ctx, client, "localhost"); done <- err }()
	cancel()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("SMTP greeting ignored cancellation")
	}
}

func TestSMTPContextDeadlineBoundsGreeting(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := newSMTPClientContext(ctx, client, "localhost")
	require.Error(t, err)
	require.Less(t, time.Since(start), time.Second)
}

func TestSMTPContextCancelsQuit(t *testing.T) {
	conn, server := net.Pipe()
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	quitStarted := make(chan struct{})
	go func() {
		fmt.Fprint(server, "220 localhost ESMTP\r\n")
		reader := bufio.NewReader(server)
		if line, err := reader.ReadString('\n'); err != nil || !strings.HasPrefix(line, "EHLO ") {
			return
		}
		fmt.Fprint(server, "250 localhost\r\n")
		if line, err := reader.ReadString('\n'); err == nil && strings.TrimSpace(line) == "QUIT" {
			close(quitStarted)
		}
	}()
	client, err := newSMTPClientContext(ctx, conn, "localhost")
	require.NoError(t, err)
	defer client.Close()
	done := make(chan error, 1)
	go func() { done <- client.Quit() }()
	select {
	case <-quitStarted:
	case <-time.After(time.Second):
		t.Fatal("QUIT not sent")
	}
	cancel()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("SMTP QUIT ignored cancellation")
	}
}

// Expose AfterFunc registration so cleanup is checked without sleeps or
// assumptions about when the context cancellation goroutine is scheduled.
type smtpCallbackTrackingContext struct {
	context.Context
	registered atomic.Int32
	active     atomic.Int32
}

func (c *smtpCallbackTrackingContext) Value(any) any { return nil }

func (c *smtpCallbackTrackingContext) AfterFunc(f func()) func() bool {
	c.registered.Add(1)
	c.active.Add(1)
	stop := context.AfterFunc(c.Context, func() {
		defer c.active.Add(-1)
		f()
	})
	return func() bool {
		if stop() {
			c.active.Add(-1)
			return true
		}
		return false
	}
}

type smtpCloseTrackingConn struct {
	net.Conn
	closed atomic.Bool
}

func (c *smtpCloseTrackingConn) Close() error {
	c.closed.Store(true)
	return c.Conn.Close()
}

func TestSMTPContextReleasesCancellationCallback(t *testing.T) {
	for _, operation := range []string{"close", "quit", "quit_error", "greeting_error"} {
		t.Run(operation, func(t *testing.T) {
			baseCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := &smtpCallbackTrackingContext{Context: baseCtx}
			conn, server := net.Pipe()
			defer server.Close()
			tracked := &smtpCloseTrackingConn{Conn: conn}
			go func() {
				defer server.Close()
				if operation == "greeting_error" {
					fmt.Fprint(server, "421 service unavailable\r\n")
					return
				}
				fmt.Fprint(server, "220 localhost ESMTP\r\n")
				reader := bufio.NewReader(server)
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					if strings.TrimSpace(line) == "QUIT" {
						if operation == "quit_error" {
							fmt.Fprint(server, "500 quit failed\r\n")
						} else {
							fmt.Fprint(server, "221 bye\r\n")
						}
						return
					}
					fmt.Fprint(server, "250 localhost\r\n")
				}
			}()
			client, err := newSMTPClientContext(ctx, tracked, "localhost")
			if operation == "greeting_error" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				defer client.Close()
				require.EqualValues(t, 1, ctx.active.Load())
				if operation == "close" {
					require.NoError(t, client.Close())
				} else if operation == "quit_error" {
					require.Error(t, client.Quit())
				} else {
					require.NoError(t, client.Quit())
				}
			}
			require.EqualValues(t, 1, ctx.registered.Load())
			require.Zero(t, ctx.active.Load(), "completed SMTP sessions must release the context callback")
			require.True(t, tracked.closed.Load(), "completed SMTP sessions must close the transport")
		})
	}
}

func TestAttributionMaxConcurrentJobsBounds(t *testing.T) {
	for _, n := range []int{0, 1, 8, 64} {
		cfg := DefaultAttributionConfig()
		cfg.MaxConcurrentJobs = n
		got, err := NormalizeAttributionConfig(cfg)
		require.NoError(t, err)
		expected := n
		if expected == 0 {
			expected = 8
		}
		require.Equal(t, expected, got.MaxConcurrentJobs)
	}
	for _, n := range []int{-1, 65} {
		cfg := DefaultAttributionConfig()
		cfg.MaxConcurrentJobs = n
		_, err := NormalizeAttributionConfig(cfg)
		require.Error(t, err)
	}
}

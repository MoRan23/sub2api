package service

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/smtp"
	"strings"
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

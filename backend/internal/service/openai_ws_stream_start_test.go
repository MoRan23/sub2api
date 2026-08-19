package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestStampOpenAICodexWSStreamRequestStart(t *testing.T) {
	now := time.UnixMilli(1776556800123)
	payload := []byte(`{"type":"response.create","client_metadata":{"keep":"yes","x-codex-ws-stream-request-start-ms":"old"}}`)
	updated, err := stampOpenAICodexWSStreamRequestStart(payload, now)
	require.NoError(t, err)
	require.Equal(t, "1776556800123", gjson.GetBytes(updated, "client_metadata.x-codex-ws-stream-request-start-ms").String())
	require.Equal(t, "yes", gjson.GetBytes(updated, "client_metadata.keep").String())

	second, err := stampOpenAICodexWSStreamRequestStart(updated, now.Add(time.Millisecond))
	require.NoError(t, err)
	require.Equal(t, "1776556800124", gjson.GetBytes(second, "client_metadata.x-codex-ws-stream-request-start-ms").String())
}

func TestStampOpenAICodexWSStreamRequestStartIgnoresNonCreate(t *testing.T) {
	payload := []byte(`{"type":"response.cancel","client_metadata":{"keep":"yes"}}`)
	updated, err := stampOpenAICodexWSStreamRequestStart(payload, time.Now())
	require.NoError(t, err)
	require.Equal(t, payload, updated)
}

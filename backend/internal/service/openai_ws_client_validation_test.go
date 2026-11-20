package service

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIWSClientValidation_PerInvocationAndFrame(t *testing.T) {
	calls := 0
	hooks := &OpenAIWSIngressHooks{ValidateClientPayload: func([]byte) error {
		calls++
		return nil
	}}
	first := []byte(`{"type":"response.create","model":"gpt-6-sol"}`)
	checked, err := validateOpenAIWSInitialPayload(first, hooks)
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.False(t, hooks.InitialPayloadValidated)
	_, err = validateOpenAIWSInitialPayload(first, checked)
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	for _, frame := range []string{`{"type":"response.create"}`, `{"type":"session.update"}`} {
		require.NoError(t, validateOpenAIWSClientPayload([]byte(frame), checked))
	}
	require.Equal(t, 3, calls)
	_, err = validateOpenAIWSInitialPayload(first, &OpenAIWSIngressHooks{InitialPayloadValidated: true, ValidateClientPayload: hooks.ValidateClientPayload})
	require.NoError(t, err)
	require.Equal(t, 3, calls)
}

func TestOpenAIWSClientValidation_IndependentEntry(t *testing.T) {
	for _, frame := range []string{
		`{"type":"response.create","model":"a","model":"a"}`,
		`{"type":"session.update","session":{"model":"a","model":"b"}}`,
		`{"type":"response.interrupt","response_id":"a","response_id":"b"}`,
		`{"type":"response.create"} trailing`,
	} {
		t.Run(frame, func(t *testing.T) {
			require.Error(t, validateOpenAIWSClientPayload([]byte(frame), nil))
		})
	}
	require.NoError(t, validateOpenAIWSClientPayload([]byte(`{"type":"response.create","model":"a"}`), nil))
	want := errors.New("group policy rejection")
	require.ErrorIs(t, validateOpenAIWSClientPayload([]byte(`{}`), &OpenAIWSIngressHooks{ValidateClientPayload: func([]byte) error { return want }}), want)
}

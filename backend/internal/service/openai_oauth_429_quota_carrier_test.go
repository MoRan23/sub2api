package service

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseOpenAIRateLimitResetTimeResponseFailedCarrier(t *testing.T) {
	resetAt := time.Now().Add(90 * time.Minute).UTC().Truncate(time.Second)
	body := []byte(fmt.Sprintf(`{"type":"response.failed","response":{"error":{"code":"usage_limit_reached","resets_at":"%d"}}}`, resetAt.Unix()))

	got := parseOpenAIRateLimitResetTime(body)

	require.NotNil(t, got)
	require.Equal(t, resetAt.Unix(), *got)
}

func TestParseOpenAIRateLimitPlanTypeResponseFailedCarrier(t *testing.T) {
	body := []byte(`{"type":"response.failed","response":{"error":{"type":"usage_limit_reached","plan_type":"Pro"}}}`)

	require.Equal(t, "pro", parseOpenAIRateLimitPlanType(body))
}

func TestClassifyOpenAIOAuth429ExplicitQuotaWithoutReset(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "root usage limit type",
			body: `{"error":{"type":"usage_limit_reached","message":"quota exhausted"}}`,
		},
		{
			name: "nested usage limit code",
			body: `{"type":"response.failed","response":{"error":{"code":"usage_limit_reached","message":"quota exhausted"}}}`,
		},
		{
			name: "root insufficient quota code",
			body: `{"error":{"code":"insufficient_quota","message":"quota exhausted"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			disposition, resetAt := classifyOpenAIOAuth429(nil, []byte(tt.body))

			require.Equal(t, openAIOAuth429QuotaReset, disposition)
			require.Nil(t, resetAt)
		})
	}
}

func TestClassifyOpenAIOAuth429OrdinaryRateLimitWithoutQuotaSignalRemainsTransient(t *testing.T) {
	body := []byte(`{"type":"response.failed","response":{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"try again"}}}`)

	disposition, resetAt := classifyOpenAIOAuth429(nil, body)

	require.Equal(t, openAIOAuth429Transient, disposition)
	require.Nil(t, resetAt)
}

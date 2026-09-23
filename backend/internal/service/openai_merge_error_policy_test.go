//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

type openAIMergeAuthorizationRaceRepo struct {
	*openAIOSRuntimeRepo
	replaceDuringMutation bool
	mutations             int
}

func (r *openAIMergeAuthorizationRaceRepo) MutateOpenAIOAuthAccountStateIfUnchanged(ctx context.Context, id int64, snapshot OpenAIOAuthAccountStateSnapshot, change OpenAIOAuthAccountStateChange) (*OpenAIOAuthAccountStateResult, error) {
	r.mutations++
	if r.replaceDuringMutation {
		r.slots[OpenAIOSWindows].AuthorizationGeneration = "replacement-authorization"
	}
	return r.openAIOSRuntimeRepo.MutateOpenAIOAuthAccountStateIfUnchanged(ctx, id, snapshot, change)
}

func TestOpenAIMergeAuthFailureRuntimeBlockRequiresCurrentAuthorization(t *testing.T) {
	for _, failure := range []struct {
		name   string
		status int
		body   string
	}{
		{"revoked", http.StatusUnauthorized, `{"error":{"code":"token_revoked"}}`},
		{"forbidden", http.StatusForbidden, `{"error":{"message":"workspace forbidden"}}`},
		{"access_state", http.StatusForbidden, `{"error":{"code":"account_deactivated"}}`},
	} {
		for _, timing := range []string{"current", "late_response", "reauthorized_during_cas"} {
			t.Run(failure.name+"/"+timing, func(t *testing.T) {
				r := &openAIMergeAuthorizationRaceRepo{openAIOSRuntimeRepo: newOpenAIOSRuntimeRepo()}
				account, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), r, r.account, OpenAIOSLinux)
				require.NoError(t, err)
				if timing == "late_response" {
					r.slots[OpenAIOSWindows].AuthorizationGeneration = "replacement-authorization"
				}
				r.replaceDuringMutation = timing == "reauthorized_during_cas"
				rateLimits := NewRateLimitService(r, nil, nil, nil, nil)
				gateway := &OpenAIGatewayService{rateLimitService: rateLimits}
				rateLimits.SetAccountRuntimeBlocker(gateway)

				require.True(t, gateway.handleOpenAIAccountUpstreamError(context.Background(), account, failure.status, nil, []byte(failure.body)), "the failed attempt must retain its failure signal")
				if timing == "current" {
					require.Equal(t, StatusError, r.account.Status)
					require.False(t, r.account.Schedulable)
					require.True(t, gateway.isOpenAIAccountRuntimeBlocked(r.account))
				} else {
					require.Equal(t, StatusActive, r.account.Status)
					require.True(t, r.account.Schedulable)
					require.False(t, gateway.isOpenAIAccountRuntimeBlocked(r.account), "a rejected CAS cannot be followed by an unversioned runtime block")
				}
				if timing == "late_response" {
					require.Zero(t, r.mutations)
				} else {
					require.Equal(t, 1, r.mutations)
				}
			})
		}
	}
}

func TestOpenAIMergeCloudflare1010SkipsOAuthAuthorizationAndPolicyPenalty(t *testing.T) {
	r := &openAIMergeAuthorizationRaceRepo{openAIOSRuntimeRepo: newOpenAIOSRuntimeRepo()}
	r.account.Credentials["temp_unschedulable_enabled"] = true
	r.account.Credentials["temp_unschedulable_rules"] = []any{map[string]any{
		"error_code": http.StatusForbidden, "keywords": []any{"1010"}, "duration_minutes": 10,
	}}
	account, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), r, r.account, OpenAIOSLinux)
	require.NoError(t, err)
	counter := &countingOpenAI403CounterCache{openAI403CounterCacheStub: openAI403CounterCacheStub{counts: []int64{3}}}
	rateLimits := NewRateLimitService(r, nil, nil, nil, nil)
	gateway := &OpenAIGatewayService{rateLimitService: rateLimits}
	rateLimits.SetAccountRuntimeBlocker(gateway)
	rateLimits.SetOpenAI403CounterCache(counter)
	body := []byte("error code: 1010\n")

	require.Equal(t, ErrorPolicySkipped, rateLimits.CheckErrorPolicy(context.Background(), account, http.StatusForbidden, body, "gpt-6-astra"))
	require.False(t, rateLimits.HandleUpstreamError(context.Background(), account, http.StatusForbidden, nil, body))
	require.False(t, gateway.handleOpenAIAccountUpstreamError(context.Background(), account, http.StatusForbidden, nil, body, "gpt-6-astra"))
	require.Zero(t, counter.increments)
	require.Zero(t, r.mutations)
	require.Equal(t, StatusActive, r.account.Status)
	require.Nil(t, r.account.TempUnschedulableUntil)
	require.False(t, gateway.isOpenAIAccountRuntimeBlocked(r.account))
}

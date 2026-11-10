package service

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCodexEngineBillingSnapshotFailureRecordsLocalOpsBeforeDispatch(t *testing.T) {
	for _, tc := range []struct {
		name       string
		wantReason string
		mutate     func(*OpenAIGatewayService, *OpenAIRecordUsageInput)
	}{
		{"inputs_unavailable", "billing_snapshot_inputs_unavailable", func(s *OpenAIGatewayService, _ *OpenAIRecordUsageInput) {
			s.billingService = nil
		}},
		{"quota_unavailable", "billing_snapshot_quota_unavailable", func(_ *OpenAIGatewayService, in *OpenAIRecordUsageInput) {
			group := &Group{ID: 4, SubscriptionType: SubscriptionTypeTotalQuota, RateMultiplier: 1}
			in.APIKey.GroupID, in.APIKey.Group = &group.ID, group
			in.Subscription = &UserSubscription{ID: 5}
		}},
		{"marshal_failed", "billing_snapshot_encode_failed", func(s *OpenAIGatewayService, _ *OpenAIRecordUsageInput) {
			s.cfg.Default.RateMultiplier = math.Inf(1)
		}},
		{"storage_limit", "billing_snapshot_size_limit", func(_ *OpenAIGatewayService, in *OpenAIRecordUsageInput) {
			in.ModelMappingChain = strings.Repeat("snapshot-private-data", codexEnginePricingSnapshotLimit/len("snapshot-private-data")+1)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := engineAccount()
			account.ID, account.Name = 44, "Engine fixture"
			account.Credentials["model_mapping"] = map[string]any{}
			account.Credentials["api_key"] = "upstream-private-key"
			cfg := &config.Config{}
			cfg.Default.RateMultiplier = 1
			created, upstreamCalls, accountLookups := 0, 0, 0
			store := &engineBillingTestStore{create: func(context.Context, *CodexEngineBillingPending) (*CodexEngineBillingJob, error) {
				created++
				return nil, errors.New("unexpected billing job")
			}}
			accounts := &engineBillingTestAccounts{get: func(context.Context, int64) (*Account, error) {
				accountLookups++
				return account, nil
			}}
			upstream := &engineDisconnectUpstream{do: func(*http.Request) (*http.Response, error) {
				upstreamCalls++
				return nil, errors.New("unexpected upstream request")
			}}
			billing := NewCodexEngineBillingService(store, accounts, upstream, cfg)
			svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream, codexEngineBilling: billing,
				billingService: &BillingService{fallbackPrices: map[string]*ModelPricing{
					"gpt-5.4": {InputPricePerToken: .01, OutputPricePerToken: .01},
				}}}
			key := &APIKey{ID: 2, Key: "downstream-private-key", UserID: 1, User: &User{ID: 1}}
			in := &OpenAIRecordUsageInput{Account: account, APIKey: key, User: key.User}
			tc.mutate(svc, in)
			writer := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(writer)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(t.Context())
			c.Set("api_key", key)
			SetCodexEngineBillingInput(c, in)
			// A previous attempt's response headers must not become evidence for a
			// snapshot failure that happened before any upstream request.
			state := beginOpenAIResponseEvidence(c, "gpt-5.4")
			state.accountID, state.requestID = account.ID, "previous-request"
			observeOpenAIResponseEvidenceHeaders(state, http.Header{"X-Codex-Imagegen-Request-Id": {"previous-image-request"}}, "response")
			result, err := svc.forwardCodexEngine(t.Context(), c, account, []byte(`{"model":"gpt-5.4","input":"request-private-body"}`), "/v1/responses", "")
			require.Error(t, err)
			require.Nil(t, result)
			require.Zero(t, accountLookups, "snapshot failure must precede billing preparation")
			require.Zero(t, created)
			require.Zero(t, upstreamCalls, "neither capacity lookup nor inference may be sent")
			require.False(t, c.Writer.Written())
			require.Empty(t, writer.Body.String())
			require.NotContains(t, c.Keys, OpsUpstreamStatusCodeKey)
			value, exists := c.Get(OpsUpstreamErrorsKey)
			require.True(t, exists)
			events, ok := value.([]*OpsUpstreamErrorEvent)
			require.True(t, ok)
			require.Len(t, events, 1)
			event := events[0]
			require.Equal(t, "billing_snapshot_error", event.Kind)
			require.Equal(t, "billing_snapshot", event.Stage)
			require.Equal(t, "local", event.Scope)
			require.Equal(t, tc.wantReason, event.Reason)
			require.NotEmpty(t, event.Message)
			require.Equal(t, account.ID, event.AccountID)
			require.Equal(t, account.Name, event.AccountName)
			require.Equal(t, account.Platform, event.Platform)
			require.Nil(t, event.ProxyID)
			require.Equal(t, opsProxyNameUnknown, event.ProxyName)
			require.Nil(t, event.HeaderDiagnostics)
			require.Zero(t, event.UpstreamStatusCode)
			require.Empty(t, event.UpstreamRequestID)
			require.Empty(t, event.UpstreamURL)
			require.Empty(t, event.UpstreamResponseBody)
			var snapshotErr *codexEngineBillingSnapshotError
			if errors.As(err, &snapshotErr) {
				require.Equal(t, snapshotErr.Error(), event.Message)
			}
			raw, marshalErr := json.Marshal(event)
			require.NoError(t, marshalErr)
			for _, private := range []string{"upstream-private-key", "downstream-private-key", "request-private-body", "snapshot-private-data", "previous-image-request", "input_price", "output_price", "engine.example"} {
				require.NotContains(t, string(raw), private)
			}
		})
	}
}

func TestCodexEngineBillingSnapshotFailureOmitsUnknownCause(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	recordCodexEngineBillingSnapshotFailure(c, engineAccount(), errors.New("untrusted private-price-card request-private-body Bearer upstream-private-key"))
	events := c.MustGet(OpsUpstreamErrorsKey).([]*OpsUpstreamErrorEvent)
	require.Len(t, events, 1)
	require.Equal(t, "billing_snapshot_failed", events[0].Reason)
	require.Equal(t, "Engine billing snapshot capture failed", events[0].Message)
	require.Empty(t, events[0].Detail)
}

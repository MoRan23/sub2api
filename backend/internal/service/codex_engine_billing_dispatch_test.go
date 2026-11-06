package service

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type engineDispatchStore struct {
	engineBillingTestStore
	notDispatched int
}

func (s *engineDispatchStore) MarkNotDispatched(ctx context.Context, id int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.notDispatched++
	return nil
}

func TestCodexEngineBillingDispatchDurableBeforeSend(t *testing.T) {
	for _, scenario := range []string{"success", "missing_headers", "capacity_failed", "persist_failed", "cancel_before_send", "dial_failed", "unknown_write"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			account := engineAccount()
			account.ID = 44
			account.Credentials["base_url"] = "https://engine.example/v1"
			account.Credentials["model_mapping"] = map[string]any{}
			cfg := &config.Config{}
			cfg.Default.RateMultiplier = 1
			created, dispatched := 0, 0
			var pending *CodexEngineBillingPending
			store := &engineDispatchStore{}
			store.create = func(_ context.Context, p *CodexEngineBillingPending) (*CodexEngineBillingJob, error) {
				created++
				if scenario == "persist_failed" {
					return nil, errors.New("fixture persistence failed")
				}
				copy := *p
				pending = &copy
				if scenario == "cancel_before_send" {
					cancel()
				}
				return &CodexEngineBillingJob{ID: 9, CodexEngineBillingPending: copy}, nil
			}
			accounts := &engineBillingTestAccounts{get: func(context.Context, int64) (*Account, error) { return account, nil }}
			upstream := &engineDisconnectUpstream{do: func(r *http.Request) (*http.Response, error) {
				if r.Method == http.MethodGet {
					if r.URL.Path != "/v1/engine/capacity" {
						return nil, errors.New("unexpected capacity fixture URL")
					}
					if scenario == "capacity_failed" {
						return capacitySyncTestResponse(404, `{}`), nil
					}
					return capacitySyncTestResponse(200, `{"object":"codex_engine.capacity","instance_id":"`+billingTestInstance+`","capabilities":{"usage_receipts":true,"billing_contract":"confirmed-events"}}`), nil
				}
				dispatched++
				require.NotNil(t, pending, "persistence must precede physical inference")
				require.Equal(t, pending.ClientRequestID, r.Header.Get(CodexEngineBillingClientRequestHeader))
				require.NotEqual(t, "client-spoofed-correlation", pending.ClientRequestID)
				require.Equal(t, billingTestInstance, pending.InstanceID)
				require.NotContains(t, string(pending.Snapshot), account.GetOpenAIApiKey())
				require.NoError(t, r.Context().Err())
				if scenario == "dial_failed" {
					httptrace.ContextClientTrace(r.Context()).GetConn(r.URL.Host)
					return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("fixture refused")}
				}
				if scenario == "unknown_write" {
					httptrace.ContextClientTrace(r.Context()).WroteHeaders()
					return nil, io.ErrUnexpectedEOF
				}
				response := capacitySyncTestResponse(200, `{"id":"resp_original","usage":{"input_tokens":500,"output_tokens":2}}`)
				if scenario != "missing_headers" {
					response.Header.Set(CodexEngineBillingContractHeader, CodexEngineBillingContract)
					response.Header.Set(CodexEngineBillingInstanceHeader, billingTestInstance)
				}
				return response, nil
			}}
			billing := NewCodexEngineBillingService(store, accounts, upstream, cfg)
			svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream, codexEngineBilling: billing,
				billingService: &BillingService{fallbackPrices: map[string]*ModelPricing{"gpt-5.4": {InputPricePerToken: .01, OutputPricePerToken: .01}}}}
			writer := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(writer)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			c.Request.Header.Set(CodexEngineBillingClientRequestHeader, "client-spoofed-correlation")
			key := &APIKey{ID: 2, UserID: 1, User: &User{ID: 1}}
			c.Set("api_key", key)
			SetCodexEngineBillingInput(c, &OpenAIRecordUsageInput{Account: account, APIKey: key, User: key.User})
			result, err := svc.forwardCodexEngine(ctx, c, account, []byte(`{"model":"gpt-5.4","input":"fixture"}`), "/v1/responses", "")
			switch scenario {
			case "success", "missing_headers":
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Contains(t, writer.Body.String(), `"input_tokens":500`)
				require.Equal(t, 1, dispatched)
			case "capacity_failed":
				require.Error(t, err)
				require.Zero(t, created)
				require.Zero(t, dispatched)
			case "persist_failed", "cancel_before_send":
				require.Error(t, err)
				require.Equal(t, 1, created)
				require.Zero(t, dispatched)
			default:
				require.Error(t, err)
				require.Equal(t, 1, dispatched)
			}
			wantNotDispatched := 0
			if scenario == "cancel_before_send" || scenario == "dial_failed" {
				wantNotDispatched = 1
			}
			require.Equal(t, wantNotDispatched, store.notDispatched)
		})
	}
}

func TestCodexEngineBillingDispatchMissingAuthContextFailsClosed(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`))
	c.Set("api_key", &APIKey{ID: 2})
	svc := &OpenAIGatewayService{}
	_, err := svc.prepareCodexEngineBillingDispatch(t.Context(), c, engineAccount(), nil, "/v1/responses", "gpt-5.4", "gpt-5.4", "gpt-5.4")
	require.ErrorContains(t, err, "context unavailable")
	job, err := svc.prepareCodexEngineBillingDispatch(t.Context(), c, engineAccount(), nil, "/v1/messages/count_tokens", "gpt-5.4", "gpt-5.4", "gpt-5.4")
	require.NoError(t, err)
	require.Nil(t, job)
}

package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type engineDeliveryWriter struct {
	gin.ResponseWriter
	mode   string
	cancel context.CancelFunc
}

func (w *engineDeliveryWriter) Write(p []byte) (int, error) {
	if w.mode == "during_terminal" && strings.Contains(string(p), "response.completed") {
		return 0, io.ErrClosedPipe
	}
	return w.ResponseWriter.Write(p)
}

func (w *engineDeliveryWriter) FlushError() error {
	w.ResponseWriter.Flush()
	if w.mode == "after_terminal" {
		w.cancel()
	}
	return nil
}

func TestCodexEngineBillingForwardDeliveryOutcome(t *testing.T) {
	for _, scenario := range []string{"complete", "before_terminal", "during_terminal", "after_terminal", "failed_terminal", "incomplete_terminal", "done_incomplete", "truncated", "json_success", "json_error", "json_incomplete", "chat_complete", "chat_truncated", "chat_length", "messages_complete", "messages_truncated", "messages_length"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			account := engineAccount()
			account.ID = 44
			account.Credentials["base_url"] = "https://engine.example/v1"
			account.Credentials["model_mapping"] = map[string]any{}
			cfg := &config.Config{}
			cfg.Default.RateMultiplier = 1
			store := &engineBillingDeliveryTestStore{engineBillingTestStore: &engineBillingTestStore{}}
			store.create = func(_ context.Context, p *CodexEngineBillingPending) (*CodexEngineBillingJob, error) {
				return &CodexEngineBillingJob{ID: 10, CodexEngineBillingPending: *p, DeliveryOutcome: CodexEngineBillingDeliveryPending}, nil
			}
			accounts := &engineBillingTestAccounts{get: func(context.Context, int64) (*Account, error) { return account, nil }}
			upstream := &engineDisconnectUpstream{do: func(r *http.Request) (*http.Response, error) {
				if r.Method == http.MethodGet {
					return capacitySyncTestResponse(200, `{"object":"codex_engine.capacity","instance_id":"`+billingTestInstance+`","capabilities":{"usage_receipts":true,"billing_contract":"confirmed-events"}}`), nil
				}
				if scenario == "before_terminal" {
					cancel()
				}
				payload := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_done\",\"usage\":{\"input_tokens\":11,\"output_tokens\":4}}}\n\n"
				switch scenario {
				case "failed_terminal":
					payload = "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"native_error\",\"message\":\"failed\"}}}\n\n"
				case "incomplete_terminal":
					payload = "event: response.incomplete\ndata: {\"type\":\"response.incomplete\",\"response\":{\"incomplete_details\":{\"reason\":\"max_output_tokens\"}}}\n\n"
				case "done_incomplete":
					payload = "event: response.done\ndata: {\"response\":{\"status\":\"incomplete\"}}\n\n"
				case "truncated":
					payload = "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"
				case "json_success":
					return capacitySyncTestResponse(200, `{"id":"resp_done","usage":{"input_tokens":11,"output_tokens":4}}`), nil
				case "json_error":
					return capacitySyncTestResponse(400, `{"error":{"message":"failed","code":"invalid_request"}}`), nil
				case "json_incomplete":
					return capacitySyncTestResponse(200, `{"id":"resp_partial","status":"incomplete"}`), nil
				case "chat_complete":
					payload = "data: {\"choices\":[{\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
				case "chat_truncated":
					payload = "data: [DONE]\n\n"
				case "chat_length":
					payload = "data: {\"choices\":[{\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n"
				case "messages_complete":
					payload = "event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {}\n\n"
				case "messages_truncated":
					payload = "event: message_stop\ndata: {}\n\n"
				case "messages_length":
					payload = "event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"max_tokens\"}}\n\nevent: message_stop\ndata: {}\n\n"
				}
				resp := capacitySyncTestResponse(200, payload)
				resp.Header.Set("Content-Type", "text/event-stream")
				return resp, nil
			}}
			billing := NewCodexEngineBillingService(store, accounts, upstream, cfg)
			svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream, codexEngineBilling: billing,
				billingService: &BillingService{fallbackPrices: map[string]*ModelPricing{"gpt-5.4": {InputPricePerToken: .01, OutputPricePerToken: .01}}}}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			c.Writer = &engineDeliveryWriter{ResponseWriter: c.Writer, mode: scenario, cancel: cancel}
			c.Set("codex_engine_delivery_complete", true) // A prior WS turn must not leak its outcome.
			key := &APIKey{ID: 2, UserID: 1, User: &User{ID: 1}}
			c.Set("api_key", key)
			SetCodexEngineBillingInput(c, &OpenAIRecordUsageInput{Account: account, APIKey: key, User: key.User})
			_, _ = svc.forwardCodexEngine(ctx, c, account, []byte(`{"model":"gpt-5.4","input":"fixture","stream":true}`), "/v1/responses", "")
			want := CodexEngineBillingNoCustomerCharge
			if scenario == "complete" || scenario == "after_terminal" || scenario == "json_success" || scenario == "chat_complete" || scenario == "messages_complete" || scenario == "chat_length" || scenario == "messages_length" || scenario == "incomplete_terminal" {
				want = CodexEngineBillingDelivered
			}
			require.Equal(t, want, store.outcome)
		})
	}
}

func TestCodexEngineBillingDeliveryOutcome(t *testing.T) {
	require.Equal(t, CodexEngineBillingNoCustomerCharge, codexEngineBillingDeliveryOutcome(false), "disconnects and ambiguous incomplete responses fail closed")
	require.Equal(t, CodexEngineBillingDelivered, codexEngineBillingDeliveryOutcome(true), "a response written and flushed to completion is billable")
}

func TestCodexEngineBillingDeliveredProtocolStopReasons(t *testing.T) {
	for _, protocol := range []string{"responses", "chat", "messages"} {
		for _, tc := range []struct {
			reason string
			want   bool
		}{
			{`"stop"`, true}, {`"length"`, true}, {`"max_tokens"`, true}, {`"max_output_tokens"`, true},
			{`"content_filter"`, true}, {`"pause_turn"`, true}, {`"refusal"`, true}, {`"model_context_window_exceeded"`, true},
			{`"vendor_extension"`, true}, {`""`, false}, {`"   "`, false}, {`42`, false}, {`true`, false}, {`{}`, false}, {`null`, false},
		} {
			t.Run(protocol+"/"+tc.reason, func(t *testing.T) {
				ctx, cancel := context.WithCancelCause(t.Context())
				defer cancel(nil)
				var payload string
				switch protocol {
				case "responses":
					payload = fmt.Sprintf("event: response.incomplete\ndata: {\"response\":{\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":%s}}}\n\n", tc.reason)
				case "chat":
					payload = fmt.Sprintf("data: {\"choices\":[{\"finish_reason\":%s}]}\n\ndata: [DONE]\n\n", tc.reason)
				case "messages":
					payload = fmt.Sprintf("event: message_delta\ndata: {\"delta\":{\"stop_reason\":%s}}\n\nevent: message_stop\ndata: {}\n\n", tc.reason)
				}
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
				resp := capacitySyncTestResponse(http.StatusOK, payload)
				defer resp.Body.Close()
				svc := &OpenAIGatewayService{cfg: &config.Config{}}
				_ = svc.relayCodexEngineStream(ctx, ctx, c, resp, &OpenAIForwardResult{Stream: true}, nil, time.Now(), false, cancel)
				require.Equal(t, tc.want, c.GetBool("codex_engine_delivery_complete"), "delivery gates transport completion, not the native stop reason")
				require.Equal(t, payload, recorder.Body.String(), "billing eligibility must not rewrite forwarded frames")
			})
		}
	}
}

type engineBillingDeliveryTestStore struct {
	*engineBillingTestStore
	outcome string
	err     error
}

func (s *engineBillingDeliveryTestStore) FinalizeDelivery(_ context.Context, id int64, outcome string) error {
	if s.err != nil {
		return s.err
	}
	s.outcome = outcome
	if outcome == CodexEngineBillingNoCustomerCharge {
		s.mu.Lock()
		delete(s.received, id)
		s.mu.Unlock()
	}
	return nil
}

func TestCodexEngineBillingReceivedEventsWaitForDeliveryOutcome(t *testing.T) {
	for _, outcome := range []string{CodexEngineBillingDelivered, CodexEngineBillingNoCustomerCharge} {
		t.Run(outcome, func(t *testing.T) {
			ctx := t.Context()
			base := &engineBillingTestStore{}
			store := &engineBillingDeliveryTestStore{engineBillingTestStore: base}
			job := &CodexEngineBillingJob{ID: 3, ClaimToken: "claim", ReceiptID: billingTestReceipt,
				DeliveryOutcome: CodexEngineBillingDeliveryPending, State: CodexEngineBillingPendingState}
			facts := billingTestEvent("confirmed-before-response", 1)
			event, err := ParseCodexEngineBillingUsageEvent(billingJSON(t, facts))
			require.NoError(t, err)
			require.NoError(t, base.StageEvents(ctx, &CodexEngineBillingReceivedPage{JobID: job.ID, Events: []CodexEngineBillingReceivedEvent{{
				EventID: event.EventID, Sequence: event.Sequence, Fingerprint: event.Fingerprint, Raw: event.Raw,
			}}}))

			settled := 0
			svc := NewCodexEngineBillingService(store, nil, nil, nil)
			svc.settle = func(_ context.Context, j *CodexEngineBillingJob, e *CodexEngineBillingUsageEvent) error {
				settled++
				base.markSettled(j.ID, e)
				return nil
			}

			pending, err := svc.settleReceived(ctx, job)
			require.NoError(t, err)
			require.True(t, pending)
			require.Zero(t, settled, "confirmed upstream facts remain staged until delivery is finalized")

			finalizeErr := svc.FinalizeDelivery(ctx, job, outcome)
			require.NoError(t, finalizeErr)
			require.Equal(t, outcome, store.outcome)
			require.Equal(t, outcome, job.DeliveryOutcome)
			pending, err = svc.settleReceived(ctx, job)
			require.NoError(t, err)
			require.False(t, pending)
			if outcome == CodexEngineBillingDelivered {
				require.Equal(t, 1, settled)
			} else {
				require.Zero(t, settled, "incomplete responses must not write usage or cost records")
				require.Empty(t, base.received[job.ID], "staged receipt facts are discarded")
			}
		})
	}
}

func TestCodexEngineBillingUnfinishedDeliveryNeverPolls(t *testing.T) {
	for _, outcome := range []string{CodexEngineBillingDeliveryPending, CodexEngineBillingNoCustomerCharge} {
		t.Run(outcome, func(t *testing.T) {
			svc := NewCodexEngineBillingService(nil, nil, nil, nil)
			svc.settle = func(context.Context, *CodexEngineBillingJob, *CodexEngineBillingUsageEvent) error {
				t.Fatal("unfinished delivery must not reach settlement")
				return nil
			}
			// All dependencies are nil: a receipt query or persistence attempt would panic.
			svc.reconcile(t.Context(), &CodexEngineBillingJob{ID: 7, DeliveryOutcome: outcome})
		})
	}
}

func TestCodexEngineBillingDeliveryUpdateFailureRemainsNonBillable(t *testing.T) {
	base := &engineBillingTestStore{}
	store := &engineBillingDeliveryTestStore{engineBillingTestStore: base, err: errors.New("database unavailable")}
	job := &CodexEngineBillingJob{ID: 3, ClaimToken: "claim", ReceiptID: billingTestReceipt, DeliveryOutcome: CodexEngineBillingDeliveryPending}
	svc := NewCodexEngineBillingService(store, nil, nil, nil)
	require.ErrorContains(t, svc.FinalizeDelivery(context.Background(), job, CodexEngineBillingDelivered), "database unavailable")
	require.Equal(t, CodexEngineBillingDeliveryPending, job.DeliveryOutcome)
	svc.settle = func(context.Context, *CodexEngineBillingJob, *CodexEngineBillingUsageEvent) error {
		t.Fatal("pending delivery must never reach the settler")
		return nil
	}
	pending, err := svc.settleReceived(context.Background(), job)
	require.NoError(t, err)
	require.True(t, pending)
}

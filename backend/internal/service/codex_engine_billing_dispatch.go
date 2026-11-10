package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

const codexEngineBillingInputKey = "codex_engine_billing_input"

// SetCodexEngineBillingInput is an internal handler hook, never populated from
// client headers. Its account/key pointers live only for the current request.
func SetCodexEngineBillingInput(c *gin.Context, input *OpenAIRecordUsageInput) {
	if c != nil && input != nil {
		copyInput := *input
		c.Set(codexEngineBillingInputKey, &copyInput)
	}
}

func (s *OpenAIGatewayService) prepareCodexEngineBillingDispatch(ctx context.Context, c *gin.Context, account *Account, body []byte, endpoint, model, billingModel, upstreamModel string) (*CodexEngineBillingJob, error) {
	if endpoint == "/v1/messages/count_tokens" || isOpenAICandyTest(ctx) {
		return nil, nil
	}
	var value any
	if c != nil {
		value, _ = c.Get(codexEngineBillingInputKey)
	}
	input, _ := value.(*OpenAIRecordUsageInput)
	if input == nil {
		// Internal diagnostics have no end-user wallet. Authenticated inference
		// must have a billing context and must fail before any dispatch if missing.
		if getAPIKeyIDFromContext(c) > 0 {
			return nil, errors.New("engine billing context unavailable")
		}
		return nil, nil
	}
	if s.codexEngineBilling == nil || input.Account == nil || input.Account.ID != account.ID {
		return nil, errors.New("engine billing service unavailable")
	}
	if input.User == nil || input.APIKey == nil {
		return nil, errors.New("engine billing identity unavailable")
	}
	in := *input
	in.Account = account
	in.UpstreamEndpoint = endpoint
	in.Result = &OpenAIForwardResult{Model: model, BillingModel: billingModel, UpstreamModel: upstreamModel,
		UpstreamEndpoint: endpoint, Stream: gjson.GetBytes(body, "stream").Bool(), OpenAIWSMode: GetOpenAIClientTransport(c) == OpenAIClientTransportWS,
		ImageEndpoint: len(endpoint) >= 11 && endpoint[:11] == "/v1/images/"}
	if tier := gjson.GetBytes(body, "service_tier").String(); tier != "" {
		in.Result.ServiceTier = &tier
	}
	effort := firstNonEmpty(gjson.GetBytes(body, "reasoning.effort").String(), gjson.GetBytes(body, "reasoning_effort").String())
	if effort != "" {
		in.Result.ReasoningEffort = &effort
	}
	snapshot, err := s.CaptureCodexEngineBillingSnapshot(ctx, &in)
	if err != nil {
		recordCodexEngineBillingSnapshotFailure(c, account, err)
		return nil, err
	}
	return s.codexEngineBilling.Prepare(ctx, &CodexEngineBillingPending{
		ClientRequestID: uuid.NewString(), AccountID: account.ID, UserID: in.User.ID, APIKeyID: in.APIKey.ID,
		IdentityDigest: CodexEngineBillingIdentity(account), Snapshot: snapshot, NextAttemptAt: time.Now().Add(2 * time.Second),
	})
}

// Snapshot capture is local preparation: no billing job or upstream attempt
// exists yet. Keep its diagnostic separate from HTTP response evidence, and
// never log the raw snapshot or an arbitrary underlying error.
func recordCodexEngineBillingSnapshotFailure(c *gin.Context, account *Account, err error) {
	reason, message := "billing_snapshot_failed", "Engine billing snapshot capture failed"
	var snapshotErr *codexEngineBillingSnapshotError
	var valueErr *json.UnsupportedValueError
	var typeErr *json.UnsupportedTypeError
	var marshalErr *json.MarshalerError
	switch {
	case errors.As(err, &snapshotErr):
		reason, message = snapshotErr.Reason, snapshotErr.Error()
	case errors.As(err, &valueErr), errors.As(err, &typeErr), errors.As(err, &marshalErr):
		reason, message = "billing_snapshot_encode_failed", "Engine billing snapshot encoding failed"
	case err != nil:
		// These are fixed validation errors owned by snapshot capture. Unknown
		// causes keep the generic diagnostic instead of exposing error contents.
		switch err.Error() {
		case "engine billing snapshot inputs unavailable":
			reason, message = "billing_snapshot_inputs_unavailable", "Engine billing snapshot inputs unavailable"
		case "engine admitted quota spend snapshot unavailable":
			reason, message = "billing_snapshot_quota_unavailable", "Engine admitted quota spend snapshot unavailable"
		case "unexpected engine quota spend snapshot", "invalid engine quota event ID", "duplicate engine quota event ID", "engine quota overflow event was not admitted":
			reason, message = "billing_snapshot_quota_invalid", "Engine billing snapshot quota admission is invalid"
		case "engine price interval exceeds token range":
			reason, message = "billing_snapshot_interval_invalid", "Engine price interval exceeds token range"
		}
	}
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform: account.Platform, AccountID: account.ID, AccountName: account.Name,
		Kind: "billing_snapshot_error", Stage: "billing_snapshot", Scope: "local", Reason: reason, Message: message,
		// Suppress automatic inheritance from a previous attempt on this account.
		HeaderDiagnostics: &OpenAIHeaderDiagnostics{},
	})
}

func (s *OpenAIGatewayService) markCodexEngineBillingNotDispatched(job *CodexEngineBillingJob) {
	if job == nil || s.codexEngineBilling == nil {
		return
	}
	// This small durable update remains valid after caller cancellation. An
	// update failure leaves the job pending; it never becomes an inferred seal.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = s.codexEngineBilling.NotDispatched(ctx, job)
}

func (s *OpenAIGatewayService) observeCodexEngineBillingResponse(c *gin.Context, account *Account, job *CodexEngineBillingJob, headers http.Header) error {
	if job == nil {
		return nil
	}
	if err := s.codexEngineBilling.ObserveResponse(job, headers); err != nil {
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{Platform: account.Platform, AccountID: account.ID, AccountName: account.Name,
			Kind: "billing_contract_mismatch", Reason: "billing_contract_mismatch", Message: "Engine billing contract or instance changed",
			UpstreamRequestID: headers.Get("X-Request-Id")})
		return err
	}
	if headers.Get(CodexEngineBillingContractHeader) == "" || headers.Get(CodexEngineBillingInstanceHeader) == "" {
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{Platform: account.Platform, AccountID: account.ID, AccountName: account.Name,
			Kind: "billing_headers_missing", Reason: "billing_headers_missing", Message: "Engine billing evidence will be resolved by original client request ID",
			UpstreamRequestID: headers.Get("X-Request-Id")})
	}
	return nil
}

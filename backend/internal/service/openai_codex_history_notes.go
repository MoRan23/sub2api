package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
)

const (
	codexAuxiliaryRequestTimeout        = 35 * time.Second
	codexAuxiliaryResponseLimit         = 16 << 20
	codexNewSessionThreadHintContextKey = "codex_new_session_thread_hint"
)

var (
	ErrCodexHistoryNotesInvalidContext = errors.New("invalid history/notes context.session_id")
	errCodexAuxiliaryInvalidResponse   = errors.New("invalid history/notes JSON response")
	errCodexAuxiliaryResponseTooLarge  = errors.New("history/notes response exceeds size limit")
)

// ForwardCodexHistoryNotes forwards a Codex History/Notes auxiliary request.
// These calls bypass billing, user/account concurrency and rate limits. Only
// unexpired paid OAuth accounts may own their durable History/Notes binding.
func (s *OpenAIGatewayService) ForwardCodexHistoryNotes(ctx context.Context, c *gin.Context, apiKey *APIKey, path string, body []byte) (*http.Response, error) {
	if s == nil || s.accountRepo == nil || apiKey == nil {
		return nil, fmt.Errorf("codex history/notes dependencies unavailable")
	}
	path = "/" + strings.TrimPrefix(strings.TrimSpace(path), "/")
	if !strings.HasPrefix(path, "/alpha/history/v2/") && !strings.HasPrefix(path, "/alpha/notes/v2/") {
		return nil, fmt.Errorf("unsupported codex history/notes path")
	}
	// Auxiliary calls use context.session_id, not Responses metadata. Capture
	// that authoritative protocol field using the same logical session mapping
	// as Responses, without letting unrelated headers or selectors override it.
	capture, err := captureCodexAuxiliaryIdentity(body)
	if err != nil {
		return nil, err
	}
	SetOpenAIOAuthIdentityCapture(c, capture)
	// Reuse the Responses session hash to seed the first auxiliary assignment.
	// Later model requests cannot change the independent History/Notes binding.
	key := s.GenerateSessionHashForOpenAIOAuthIdentity(c, body, capture.Logical.SessionKey)
	// Session capture attaches the legacy hash to the Gin request after the
	// caller supplied ctx. Preserve the caller's cancellation/deadline while
	// carrying that hash into the same compatibility helpers used by Responses.
	stickyCtx := ctx
	if c != nil && c.Request != nil {
		stickyCtx = withOpenAILegacySessionHash(ctx, openAILegacySessionHashFromContext(c.Request.Context()))
	}
	accounts, err := s.listCodexAuxiliaryAccounts(ctx, apiKey)
	if err != nil {
		return nil, err
	}
	if len(accounts) == 0 {
		return nil, ErrNoAvailableAccounts
	}
	// Responses affinity is only an initial preference. The independent atomic
	// binding is established before forwarding, including on concurrent first calls.
	preferredID := int64(0)
	if s.cache != nil {
		if id, lookupErr := s.getStickySessionAccountID(stickyCtx, apiKey.GroupID, key); lookupErr == nil {
			preferredID = id
		}
	}
	eligibleIDs := make([]int64, 0, len(accounts))
	for _, account := range accounts {
		eligibleIDs = append(eligibleIDs, account.ID)
	}
	bindingKey := codexAuxiliaryAccountBindingKey(apiKey, capture.Logical.SessionKey)
	var binding CodexAuxiliaryAccountBinding
	stickySource := "local"
	if s.cache != nil {
		store, ok := s.cache.(CodexAuxiliaryAccountBindingStore)
		if !ok {
			return nil, ErrCodexAuxiliaryAccountBindingStoreUnavailable
		}
		binding, err = store.ResolveCodexAuxiliaryAccountBinding(ctx, bindingKey, eligibleIDs, preferredID)
		stickySource = "redis"
	} else {
		binding, err = resolveLocalCodexAuxiliaryAccountBinding(&s.codexAuxiliarySticky, bindingKey, eligibleIDs, preferredID)
	}
	if err != nil {
		return nil, err
	}
	var account *Account
	for _, candidate := range accounts {
		if candidate.ID == binding.AccountID {
			account = candidate
			break
		}
	}
	if account == nil {
		return nil, ErrCodexAuxiliaryAccountBindingStoredInvalid
	}
	seeded := !binding.HadBinding && preferredID == account.ID
	stickyHit := binding.Reused || seeded
	if !stickyHit {
		stickySource = "none"
	}
	kind := "history"
	if strings.HasPrefix(path, "/alpha/notes/") {
		kind = "notes"
	}
	BeginCodexContextManagementObservation(c, kind, path)
	if entry := codexContextObservationFromContext(c); entry != nil {
		entry.AccountID, entry.AccountName = account.ID, account.Name
		entry.Attempt, entry.Fallback = 1, false
		entry.StickyHit, entry.StickySource = stickyHit, stickySource
	}
	attemptCtx, cancel := context.WithTimeout(ctx, codexAuxiliaryRequestTimeout)
	resp, reqErr := s.doCodexAuxiliaryRequest(attemptCtx, c, account, path, body)
	if c != nil && (binding.HadBinding || seeded) {
		// An established task stays old even when its former account lost eligibility.
		c.Set(codexNewSessionThreadHintContextKey, false)
	}
	if reqErr == nil && resp == nil {
		reqErr = errCodexAuxiliaryInvalidResponse
	}
	// Keep the timeout active through EOF and reject incomplete JSON. An upstream
	// status or transport failure never changes ownership of History/Notes data.
	if reqErr == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		reqErr = bufferCodexAuxiliaryResponse(resp)
		cancel()
	}
	if entry := codexContextObservationFromContext(c); entry != nil {
		if resp != nil {
			entry.HTTPStatus = resp.StatusCode
			entry.UpstreamHTTPStatus = resp.StatusCode
		}
		entry.ErrorKind = codexAuxiliaryObservationErrorKind(reqErr)
		if reqErr == nil && resp.StatusCode >= 500 {
			entry.ErrorKind = "upstream_5xx"
		}
	}
	if reqErr != nil {
		cancel()
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return nil, reqErr
	}
	if resp.Body != nil && !(resp.StatusCode >= 200 && resp.StatusCode < 300) {
		resp.Body = &codexAuxiliaryCancelBody{ReadCloser: resp.Body, cancel: cancel}
	} else {
		cancel()
	}
	return resp, nil
}

type codexAuxiliaryCancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
	once   sync.Once
}

func (b *codexAuxiliaryCancelBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.cancel)
	return err
}

func (s *OpenAIGatewayService) listCodexAuxiliaryAccounts(ctx context.Context, apiKey *APIKey) ([]*Account, error) {
	var accounts []Account
	var err error
	includeGrouped := false
	if s.cfg != nil && s.cfg.RunMode == config.RunModeSimple {
		includeGrouped = true
	}
	if apiKey.GroupID != nil {
		accounts, err = s.accountRepo.ListModelAvailabilityCandidates(ctx, apiKey.GroupID, []string{PlatformOpenAI}, includeGrouped)
	} else {
		accounts, err = s.accountRepo.ListModelAvailabilityCandidates(ctx, nil, []string{PlatformOpenAI}, includeGrouped)
	}
	if err != nil {
		return nil, err
	}
	result := make([]*Account, 0, len(accounts))
	now := time.Now()
	parents := make(map[int64]*Account)
	for i := range accounts {
		account := &accounts[i]
		if !account.IsOpenAIOAuth() || !account.Schedulable || !codexAuxiliaryAccountAvailable(account, now) {
			continue
		}
		credentials := account
		if account.IsShadow() {
			parentID := *account.ParentAccountID
			var found bool
			credentials, found = parents[parentID]
			if !found {
				credentials, err = s.accountRepo.GetByID(ctx, parentID)
				if err != nil && !errors.Is(err, ErrAccountNotFound) {
					return nil, err
				}
				parents[parentID] = credentials
			}
			if credentials == nil || credentials.IsShadow() || !codexAuxiliaryAccountAvailable(credentials, now) {
				continue
			}
		}
		if codexAuxiliaryPaidOAuthAccount(credentials, now) {
			result = append(result, account)
		}
	}
	return result, nil
}

// Account state and credential failures still prevent dispatch. Model quota,
// concurrency, overload and 429 cooldowns do not govern auxiliary resources.
func codexAuxiliaryAccountAvailable(account *Account, now time.Time) bool {
	if account == nil || !account.IsActive() {
		return false
	}
	if account.AutoPauseOnExpired && account.ExpiresAt != nil && !now.Before(*account.ExpiresAt) {
		return false
	}
	if account.TempUnschedulableUntil != nil && now.Before(*account.TempUnschedulableUntil) &&
		!IsAccountSchedulingThresholdReason(account.TempUnschedulableReason) &&
		!wasTempUnschedByStatusCode(account.TempUnschedulableReason, http.StatusTooManyRequests) {
		return false
	}
	return true
}

func codexAuxiliaryPaidOAuthAccount(account *Account, now time.Time) bool {
	if account == nil || !account.IsOpenAIOAuth() || account.IsOpenAIPersonalAccessToken() || account.IsOpenAIAgentIdentity() {
		return false
	}
	plan := strings.ToLower(strings.TrimSpace(account.GetCredential("plan_type")))
	if plan == "" {
		plan = strings.ToLower(strings.TrimSpace(account.GetCredential("chatgpt_plan_type")))
	}
	switch plan {
	case "plus", "pro", "pro_lite", "prolite", "pro-lite":
	default:
		return false
	}
	// This is the paid subscription expiry, never the refreshable access token's
	// expires_at. Unknown or malformed expiry cannot establish paid eligibility.
	expiresAt := account.GetCredentialAsTime("subscription_expires_at")
	return expiresAt != nil && now.Before(*expiresAt)
}

func codexAuxiliaryAccountBindingKey(apiKey *APIKey, logicalSession string) string {
	// Include the API key as well as its group; unrelated clients may submit the
	// same session string. Do not couple persistent ownership to JWT key rotation.
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%d:%d:%s", derefGroupID(apiKey.GroupID), apiKey.ID, logicalSession))))
}

func (s *OpenAIGatewayService) doCodexAuxiliaryRequest(ctx context.Context, c *gin.Context, account *Account, path string, body []byte) (*http.Response, error) {
	if c != nil {
		c.Set(codexNewSessionThreadHintContextKey, false)
	}
	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return nil, err
	}
	// Token acquisition can synchronously refresh and persist a different plan.
	// Re-read that account before sending so newly confirmed expiry/downgrade
	// cannot use the eligibility snapshot taken before the refresh.
	if s.openAITokenProvider != nil {
		account, err = s.accountRepo.GetByID(ctx, account.ID)
		if err != nil {
			return nil, err
		}
	}
	now := time.Now()
	if account == nil || !account.Schedulable || !codexAuxiliaryAccountAvailable(account, now) {
		return nil, ErrNoAvailableAccounts
	}
	credentials, err := resolveCredentialAccount(ctx, s.accountRepo, account)
	if err != nil {
		return nil, err
	}
	if !codexAuxiliaryAccountAvailable(credentials, now) || !codexAuxiliaryPaidOAuthAccount(credentials, now) {
		return nil, ErrNoAvailableAccounts
	}
	target, err := s.codexAuxiliaryURL(account, path)
	if err != nil {
		return nil, err
	}
	projectedBody := body
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(projectedBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("content-type", "application/json")
	if c != nil && c.Request != nil {
		for _, name := range []string{"accept", "accept-language", "x-openai-encrypted-tool-arguments", "x-openai-tool-output-truncation-policy"} {
			if value := c.GetHeader(name); value != "" {
				req.Header.Set(name, value)
			}
		}
	}
	// Share the Responses session resolver, but project only the auxiliary
	// protocol's context.session_id. History window/item selectors are opaque
	// upstream references, not the current Responses window's identity.
	if account.UsesOpenAICodexProtocol() {
		capture, ok := OpenAIOAuthIdentityCaptureFromContext(c)
		if !ok {
			capture, err = captureCodexAuxiliaryIdentity(body)
			if err != nil {
				return nil, err
			}
			SetOpenAIOAuthIdentityCapture(c, capture)
		}
		plan, planErr := s.GetOrResolveOpenAIOAuthOutboundIdentity(ctx, c, account, capture, OpenAIOAuthIdentityPlanOptions{
			TurnIdentityEnabled: true,
			ProjectionMode:      OpenAIOAuthIdentityProjectionExistingTurnMetadataOnly,
			InstallationPolicy:  OpenAIOAuthInstallationPreserve,
		}, nil)
		if planErr != nil {
			return nil, planErr
		}
		if !plan.TurnIdentityEnabled || strings.TrimSpace(plan.WireProfile.SessionID) == "" {
			return nil, errors.New("history/notes session identity unavailable")
		}
		if c != nil && path == "/alpha/notes/v2/thread_hint" {
			c.Set(codexNewSessionThreadHintContextKey, IsNewOpenAICodexSession(c, plan.WireProfile.SessionID))
		}
		if plan.ClientIdentityEnabled {
			applyCodexClientIdentityPlan(req.Header, plan.ClientIdentity)
		}
		projectedBody = rewriteCodexAuxiliaryJSON(body, plan)
		var inboundHeaders http.Header
		if c != nil && c.Request != nil {
			inboundHeaders = c.Request.Header
		}
		observeCodexContextIdentityRewrite(c, body, projectedBody, inboundHeaders, req.Header)
		req.Body = http.NoBody
		req.GetBody = nil
		if projectedBody != nil {
			req.Body = io.NopCloser(bytes.NewReader(projectedBody))
			req.ContentLength = int64(len(projectedBody))
			req.GetBody = func() (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(projectedBody)), nil
			}
		}
	}
	auth, err := s.buildOpenAIAuthenticationHeaders(ctx, account, token)
	if err != nil {
		return nil, err
	}
	for name, values := range auth {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	if account.UsesOpenAICodexProtocol() {
		req.Host = "chatgpt.com"
		if err := resolveAndSetOpenAIChatGPTAccountHeaders(ctx, s.accountRepo, req.Header, account); err != nil {
			return nil, err
		}
	}
	if entry := codexContextObservationFromContext(c); entry != nil {
		entry.UpstreamSent = true
	}
	resp, err := s.doCodexAuxiliaryUpstream(req, resolveAccountProxyURL(account), account)
	if err != nil {
		// Preserve transport error classification without changing account ownership.
		return resp, &codexAuxiliaryTransportError{err: err}
	}
	return resp, nil
}

// IsNewCodexThreadHintRequest identifies the initial Notes probe for an upstream
// session allocated by this request. It does not infer freshness from a missing
// sticky-cache entry or depend on diagnostic observation being enabled.
func IsNewCodexThreadHintRequest(c *gin.Context) bool {
	return c != nil && c.GetBool(codexNewSessionThreadHintContextKey)
}

func captureCodexAuxiliaryIdentity(body []byte) (OpenAIOAuthIdentityCapture, error) {
	var request struct {
		Context struct {
			SessionID string `json:"session_id"`
		} `json:"context"`
	}
	if json.Unmarshal(body, &request) != nil || sanitizeSessionID(request.Context.SessionID) == "" {
		return OpenAIOAuthIdentityCapture{}, ErrCodexHistoryNotesInvalidContext
	}
	metadata, _ := json.Marshal(map[string]string{"session_id": request.Context.SessionID})
	// The synthetic capture is internal only. No turn metadata or Responses
	// body fields are emitted onto this auxiliary endpoint.
	return CaptureOpenAIOAuthIdentityWithTurnMetadata(nil, nil, "", string(metadata)), nil
}

type codexAuxiliaryTransportError struct{ err error }

func (e *codexAuxiliaryTransportError) Error() string { return e.err.Error() }
func (e *codexAuxiliaryTransportError) Unwrap() error { return e.err }

type codexAuxiliaryResponseReadError struct{ err error }

func (e *codexAuxiliaryResponseReadError) Error() string { return e.err.Error() }
func (e *codexAuxiliaryResponseReadError) Unwrap() error { return e.err }

func bufferCodexAuxiliaryResponse(resp *http.Response) error {
	if resp.Body == nil {
		return errCodexAuxiliaryInvalidResponse
	}
	upstreamBody := resp.Body
	resp.Body = http.NoBody
	defer upstreamBody.Close()
	if resp.ContentLength > codexAuxiliaryResponseLimit {
		return errCodexAuxiliaryResponseTooLarge
	}
	body, err := io.ReadAll(io.LimitReader(upstreamBody, codexAuxiliaryResponseLimit+1))
	if err != nil {
		return &codexAuxiliaryResponseReadError{err: err}
	}
	if len(body) > codexAuxiliaryResponseLimit {
		return errCodexAuxiliaryResponseTooLarge
	}
	if resp.ContentLength > 0 && int64(len(body)) != resp.ContentLength {
		return &codexAuxiliaryResponseReadError{err: io.ErrUnexpectedEOF}
	}
	if !json.Valid(body) {
		return errCodexAuxiliaryInvalidResponse
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return nil
}

func codexAuxiliaryObservationErrorKind(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(err, errCodexAuxiliaryInvalidResponse) {
		return "invalid_response"
	}
	if errors.Is(err, errCodexAuxiliaryResponseTooLarge) {
		return "response_too_large"
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return "timeout"
		}
		return "connection_error"
	}
	return "request_error"
}

// rewriteCodexAuxiliaryJSON rewrites the official context.session_id carrier.
// Root window_id/item_id are historical selectors and must remain opaque,
// including absent/null selectors. Notes strings are never recursively parsed.
func rewriteCodexAuxiliaryJSON(body []byte, plan OpenAIOAuthIdentityPlan) []byte {
	var root map[string]json.RawMessage
	if len(body) == 0 || json.Unmarshal(body, &root) != nil || root == nil {
		return body
	}
	for _, key := range []string{"session_id", "thread_id", "context_window_id", "window_number", "first_window_id", "previous_window_id", "client_metadata", "x-codex-turn-metadata", "x-codex-window-id"} {
		delete(root, key)
	}
	var metadata map[string]json.RawMessage
	if json.Unmarshal(root["metadata"], &metadata) == nil && metadata != nil {
		for _, key := range []string{"session_id", "thread_id", "window_id", "context_window_id", "window_number", "first_window_id", "previous_window_id", "client_metadata", "x-codex-turn-metadata", "x-codex-window-id"} {
			delete(metadata, key)
		}
		if len(metadata) == 0 {
			delete(root, "metadata")
		} else {
			root["metadata"], _ = json.Marshal(metadata)
		}
	}
	var nested map[string]json.RawMessage
	if json.Unmarshal(root["context"], &nested) == nil && nested != nil {
		for _, key := range []string{"thread_id", "window_id", "context_window_id", "window_number", "first_window_id", "previous_window_id", "client_metadata", "x-codex-turn-metadata", "x-codex-window-id"} {
			delete(nested, key)
		}
		if plan.WireProfile.SessionID != "" {
			nested["session_id"], _ = json.Marshal(plan.WireProfile.SessionID)
		}
		root["context"], _ = json.Marshal(nested)
	}
	encoded, err := json.Marshal(root)
	if err != nil {
		return body
	}
	return encoded
}

func (s *OpenAIGatewayService) codexAuxiliaryURL(account *Account, path string) (string, error) {
	if account.UsesOpenAICodexProtocol() {
		return "https://chatgpt.com/backend-api/codex" + path, nil
	}
	base := strings.TrimSpace(account.GetOpenAIBaseURL())
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	base = strings.TrimRight(base, "/")
	base = strings.TrimSuffix(base, "/responses")
	return base + path, nil
}

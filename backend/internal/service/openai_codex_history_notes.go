package service

import (
	"bytes"
	"context"
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
	codexAuxiliaryRequestTimeout = 35 * time.Second
	codexAuxiliaryResponseLimit  = 16 << 20
)

var (
	ErrCodexHistoryNotesInvalidContext = errors.New("invalid history/notes context.session_id")
	errCodexAuxiliaryInvalidResponse   = errors.New("invalid history/notes JSON response")
	errCodexAuxiliaryResponseTooLarge  = errors.New("history/notes response exceeds size limit")
)

// ForwardCodexHistoryNotes forwards a Codex History/Notes auxiliary request.
// These calls deliberately bypass model billing, account concurrency slots and
// RPM accounting; the caller has already passed normal API-key authentication.
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
	// Reuse the exact session hash used by Responses scheduling so auxiliary
	// calls follow the account selected by the first model request.
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
	ordered, stickySource := s.orderCodexAuxiliaryAccounts(stickyCtx, key, accounts, derefGroupID(apiKey.GroupID))
	readOnly := codexAuxiliaryReadOnlyPath(path)
	var lastErr error
	for i, account := range ordered {
		if account == nil {
			continue
		}
		kind := "history"
		if strings.HasPrefix(path, "/alpha/notes/") {
			kind = "notes"
		}
		BeginCodexContextManagementObservation(c, kind, path)
		if entry := codexContextObservationFromContext(c); entry != nil {
			entry.AccountID, entry.AccountName = account.ID, account.Name
			entry.Attempt, entry.Fallback = i+1, i > 0
			entry.StickyHit, entry.StickySource = i == 0 && stickySource != "none", stickySource
		}
		attemptCtx, cancel := context.WithTimeout(ctx, codexAuxiliaryRequestTimeout)
		resp, reqErr := s.doCodexAuxiliaryRequest(attemptCtx, c, account, path, body)
		// A 2xx header alone is insufficient: a truncated/invalid response must
		// not move affinity. Buffer only a bounded response and keep the upstream
		// timeout active through EOF. Notes writes may already have taken effect,
		// so ambiguous post-send failures must never be retried on another account.
		if reqErr == nil && resp != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			reqErr = bufferCodexAuxiliaryResponse(resp)
			cancel()
		}
		if entry := codexContextObservationFromContext(c); entry != nil {
			if resp != nil {
				entry.HTTPStatus = resp.StatusCode
				entry.UpstreamHTTPStatus = resp.StatusCode
			}
			entry.ErrorKind = codexAuxiliaryObservationErrorKind(reqErr)
			if reqErr == nil && resp != nil && resp.StatusCode >= 500 {
				entry.ErrorKind = "upstream_5xx"
			}
		}
		if resp != nil && resp.Body != nil && reqErr == nil && !(resp.StatusCode >= 200 && resp.StatusCode < 300) {
			// Keep the deadline active while the handler drains the response body;
			// cancel it as soon as the body is closed.
			resp.Body = &codexAuxiliaryCancelBody{ReadCloser: resp.Body, cancel: cancel}
		} else {
			cancel()
		}
		if reqErr == nil {
			if resp != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
				s.storeCodexAuxiliarySticky(key, account.ID, derefGroupID(apiKey.GroupID))
				_ = s.setStickySessionAccountID(stickyCtx, apiKey.GroupID, key, account.ID, s.openAIWSSessionStickyTTL())
				return resp, nil
			}
			if resp != nil {
				// Retry only temporary read failures. Status errors after a Notes
				// write (including 5xx) cannot prove that the mutation did not run.
				if !readOnly || !codexAuxiliaryTemporaryStatus(resp.StatusCode) || i == len(ordered)-1 {
					return resp, nil
				}
				lastErr = fmt.Errorf("upstream history/notes status %s", resp.Status)
				if resp.Body != nil {
					_ = resp.Body.Close()
				}
			} else {
				lastErr = errCodexAuxiliaryInvalidResponse
				return nil, lastErr
			}
		} else {
			lastErr = reqErr
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
			if ctx.Err() != nil || !codexAuxiliaryRetryableError(reqErr, readOnly, resp != nil) {
				return nil, reqErr
			}
		}
		if i == len(ordered)-1 {
			break
		}
		if entry := codexContextObservationFromContext(c); entry != nil {
			RecordCodexContextManagementResult(c, kind, path, "failed", entry.HTTPStatus, 0, entry.ErrorKind)
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("history/notes upstream unavailable")
	}
	return nil, lastErr
}

func (s *OpenAIGatewayService) storeCodexAuxiliarySticky(key string, accountID int64, groupID int64) {
	now := time.Now()
	// Sweep expired local fallback entries opportunistically. Redis remains the
	// primary store; this keeps the in-process fallback bounded for deployments
	// without a cache backend.
	count := 0
	s.codexAuxiliarySticky.Range(func(k, v any) bool {
		count++
		if entry, ok := v.(codexAuxiliaryStickyEntry); ok && !entry.expiresAt.IsZero() && now.After(entry.expiresAt) {
			s.codexAuxiliarySticky.Delete(k)
			count--
		}
		return true
	})
	if count >= 4096 {
		// Drop one arbitrary fallback entry before inserting the new one; Redis
		// state (when available) is unaffected and has its own TTL.
		s.codexAuxiliarySticky.Range(func(k, _ any) bool {
			s.codexAuxiliarySticky.Delete(k)
			return false
		})
	}
	s.codexAuxiliarySticky.Store(codexAuxiliaryLocalStickyKey{groupID: groupID, sessionHash: key},
		codexAuxiliaryStickyEntry{accountID: accountID, expiresAt: now.Add(s.openAIWSSessionStickyTTL())})
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
	for i := range accounts {
		account := accounts[i]
		// History/Notes are Codex backend resources. Restrict candidates to
		// accounts using that protocol so an ordinary OpenAI API-key account
		// cannot be selected first and terminate the request with a misleading
		// 404 before a PAT/OAuth account is tried.
		if account.UsesOpenAICodexProtocol() {
			result = append(result, &account)
		}
	}
	return result, nil
}

func (s *OpenAIGatewayService) orderCodexAuxiliaryAccounts(ctx context.Context, key string, accounts []*Account, groupID int64) ([]*Account, string) {
	if s.cache != nil {
		if id, err := s.getStickySessionAccountID(ctx, &groupID, key); err == nil {
			for i, account := range accounts {
				if account != nil && account.ID == id {
					ordered := make([]*Account, 0, len(accounts))
					ordered = append(ordered, account)
					ordered = append(ordered, accounts[:i]...)
					ordered = append(ordered, accounts[i+1:]...)
					return ordered, "redis"
				}
			}
		}
	}
	localKey := codexAuxiliaryLocalStickyKey{groupID: groupID, sessionHash: key}
	if value, ok := s.codexAuxiliarySticky.Load(localKey); ok {
		if entry, ok := value.(codexAuxiliaryStickyEntry); ok {
			if !entry.expiresAt.IsZero() && time.Now().After(entry.expiresAt) {
				s.codexAuxiliarySticky.Delete(localKey)
				return accounts, "none"
			}
			id := entry.accountID
			for i, account := range accounts {
				if account != nil && account.ID == id {
					ordered := make([]*Account, 0, len(accounts))
					ordered = append(ordered, account)
					ordered = append(ordered, accounts[:i]...)
					ordered = append(ordered, accounts[i+1:]...)
					return ordered, "local"
				}
			}
		}
	}
	return accounts, "none"
}

// Match the shared Redis sticky scope; identical session signals in different
// groups must not influence the in-process auxiliary fallback.
type codexAuxiliaryLocalStickyKey struct {
	groupID     int64
	sessionHash string
}

type codexAuxiliaryStickyEntry struct {
	accountID int64
	expiresAt time.Time
}

func (s *OpenAIGatewayService) doCodexAuxiliaryRequest(ctx context.Context, c *gin.Context, account *Account, path string, body []byte) (*http.Response, error) {
	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return nil, err
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
	resp, err := s.doOpenAIUpstream(req, resolveAccountProxyURL(account), account)
	if err != nil {
		// Only actual transport failures are eligible for retry. Token refresh,
		// identity-store and configuration errors above remain authoritative.
		return resp, &codexAuxiliaryTransportError{err: err}
	}
	return resp, nil
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

func codexAuxiliaryReadOnlyPath(path string) bool {
	switch path {
	case "/alpha/history/v2/list_windows", "/alpha/history/v2/list_items",
		"/alpha/history/v2/read_item", "/alpha/history/v2/search_contents",
		"/alpha/notes/v2/list_files_by_prefix", "/alpha/notes/v2/read_file",
		"/alpha/notes/v2/search_contents", "/alpha/notes/v2/thread_hint":
		return true
	default:
		return false
	}
}

func codexAuxiliaryTemporaryStatus(status int) bool {
	return status == http.StatusInternalServerError || status == http.StatusBadGateway ||
		status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
}

func codexAuxiliaryRetryableError(err error, readOnly, responseStarted bool) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	var transport *codexAuxiliaryTransportError
	var bodyRead *codexAuxiliaryResponseReadError
	if !errors.As(err, &transport) && !(readOnly && errors.As(err, &bodyRead)) {
		return false
	}
	if !readOnly {
		// A dial failure proves the write could not reach an upstream. Generic
		// timeouts/EOFs, even without response headers, do not provide that proof.
		var op *net.OpError
		return !responseStarted && errors.As(err, &op) && op.Op == "dial"
	}
	var netErr net.Error
	var op *net.OpError
	var dns *net.DNSError
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) || errors.As(err, &op) || errors.As(err, &dns) ||
		(errors.As(err, &netErr) && netErr.Timeout())
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

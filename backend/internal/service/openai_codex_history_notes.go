package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

const codexAuxiliaryRequestTimeout = 35 * time.Second

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
	// Capture the logical Codex session before deriving the sticky key. This
	// keeps auxiliary calls aligned with the session hash used by Responses,
	// including canonical nested turn metadata that is not top-level JSON.
	if c != nil {
		if _, ok := OpenAIOAuthIdentityCaptureFromContext(c); !ok {
			SetOpenAIOAuthIdentityCapture(c, CaptureOpenAIOAuthIdentity(c, body, ""))
		}
	}
	// Reuse the exact session hash used by Responses scheduling so auxiliary
	// calls follow the account selected by the first model request.
	key := s.GenerateSessionHashForOpenAIOAuthIdentity(c, body, "")
	if strings.TrimSpace(key) == "" {
		key = codexAuxiliaryStickyKey(apiKey, c, body)
	}
	accounts, err := s.listCodexAuxiliaryAccounts(ctx, apiKey)
	if err != nil {
		return nil, err
	}
	if len(accounts) == 0 {
		return nil, ErrNoAvailableAccounts
	}
	ordered, stickySource := s.orderCodexAuxiliaryAccounts(ctx, key, accounts, derefGroupID(apiKey.GroupID))
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
		if resp != nil && resp.Body != nil && reqErr == nil && resp.StatusCode < 500 {
			// Keep the deadline active while the handler drains the response body;
			// cancel it as soon as the body is closed.
			resp.Body = &codexAuxiliaryCancelBody{ReadCloser: resp.Body, cancel: cancel}
		} else {
			cancel()
		}
		if reqErr == nil {
			if resp != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
				s.storeCodexAuxiliarySticky(key, account.ID)
				if s.cache != nil {
					_ = s.cache.SetSessionAccountID(ctx, derefGroupID(apiKey.GroupID), key, account.ID, s.openAIWSSessionStickyTTL())
				}
				return resp, nil
			}
			// Permission/validation errors are authoritative and must not rebind.
			if resp != nil && resp.StatusCode >= 400 && resp.StatusCode < 500 {
				return resp, nil
			}
			if resp != nil {
				lastErr = fmt.Errorf("upstream history/notes status %s", resp.Status)
				_ = resp.Body.Close()
			}
		} else {
			lastErr = reqErr
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

func (s *OpenAIGatewayService) storeCodexAuxiliarySticky(key string, accountID int64) {
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
	s.codexAuxiliarySticky.Store(key, codexAuxiliaryStickyEntry{accountID: accountID, expiresAt: now.Add(s.openAIWSSessionStickyTTL())})
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
		if id, err := s.cache.GetSessionAccountID(ctx, groupID, key); err == nil {
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
	if value, ok := s.codexAuxiliarySticky.Load(key); ok {
		if entry, ok := value.(codexAuxiliaryStickyEntry); ok {
			if !entry.expiresAt.IsZero() && time.Now().After(entry.expiresAt) {
				s.codexAuxiliarySticky.Delete(key)
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
		// Codex identity headers are only meaningful for ChatGPT Codex/PAT
		// upstreams. Do not leak OAuth-specific headers to ordinary API-key
		// OpenAI-compatible accounts.
		if account.UsesOpenAICodexProtocol() {
			for _, name := range []string{"x-codex-turn-metadata", "x-codex-window-id"} {
				if value := c.GetHeader(name); value != "" {
					req.Header.Set(name, value)
				}
			}
		}
	}
	// History/Notes carries the same Codex turn metadata as Responses. Resolve
	// the immutable account/window plan before forwarding so client supplied
	// session/thread/window identifiers cannot leak to the upstream account.
	if account.UsesOpenAICodexProtocol() {
		capture, ok := OpenAIOAuthIdentityCaptureFromContext(c)
		if !ok {
			capture = CaptureOpenAIOAuthIdentity(c, body, "")
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
		projectedBody, planErr = ApplyOpenAIOAuthIdentityPlan(req.Header, body, plan)
		if planErr != nil {
			return nil, planErr
		}
		projectedBody = rewriteCodexAuxiliaryJSON(projectedBody, plan)
		var inboundHeaders http.Header
		if c != nil && c.Request != nil {
			inboundHeaders = c.Request.Header
		}
		observeCodexContextIdentityRewrite(c, body, projectedBody, inboundHeaders, req.Header)
		req.Body = http.NoBody
		if projectedBody != nil {
			req.Body = io.NopCloser(bytes.NewReader(projectedBody))
			req.ContentLength = int64(len(projectedBody))
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
	return s.doOpenAIUpstream(req, resolveAccountProxyURL(account), account)
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
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return "timeout"
		}
		return "connection_error"
	}
	return "request_error"
}

// rewriteCodexAuxiliaryJSON replaces only protocol identity fields at the
// request's top level. Item IDs and note contents remain opaque payload data.
func rewriteCodexAuxiliaryJSON(body []byte, plan OpenAIOAuthIdentityPlan) []byte {
	var root map[string]json.RawMessage
	if len(body) == 0 || json.Unmarshal(body, &root) != nil || root == nil {
		return body
	}
	rewriteObject := func(object map[string]json.RawMessage) {
		values := map[string]string{
			"session_id": plan.WireProfile.SessionID,
			"thread_id":  plan.WireProfile.ThreadID,
			"window_id":  plan.WireProfile.WindowID,
		}
		for key, value := range values {
			if strings.TrimSpace(value) != "" {
				if encoded, err := json.Marshal(value); err == nil {
					object[key] = encoded
				}
			}
		}
		if plan.WireProfile.WindowNumber != nil {
			if encoded, err := json.Marshal(*plan.WireProfile.WindowNumber); err == nil {
				object["window_number"] = encoded
			}
		}
		if plan.Window.ContextWindowID != "" {
			if encoded, err := json.Marshal(plan.Window.ContextWindowID); err == nil {
				object["context_window_id"] = encoded
			}
		}
	}
	rewriteObject(root)
	// Codex History/Notes wraps request identity in a context object. Recurse
	// only through that protocol container so note contents and item payloads
	// remain opaque.
	for _, key := range []string{"context", "metadata"} {
		var nested map[string]json.RawMessage
		if raw, ok := root[key]; ok && json.Unmarshal(raw, &nested) == nil && nested != nil {
			rewriteObject(nested)
			if encoded, err := json.Marshal(nested); err == nil {
				root[key] = encoded
			}
		}
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

func codexAuxiliaryStickyKey(apiKey *APIKey, c *gin.Context, body []byte) string {
	seed := ""
	if capture, ok := OpenAIOAuthIdentityCaptureFromContext(c); ok {
		seed = strings.TrimSpace(capture.Logical.SessionKey)
	}
	if c != nil && c.Request != nil {
		if seed == "" {
			seed = strings.TrimSpace(c.GetHeader("session_id"))
		}
		if seed == "" {
			seed = strings.TrimSpace(c.GetHeader("conversation_id"))
		}
	}
	if seed == "" {
		// History/Notes calls commonly carry the logical session only in the
		// JSON body. Reuse that value across list/read/search operations so the
		// sticky binding is session scoped rather than operation scoped.
		var root map[string]json.RawMessage
		if json.Unmarshal(body, &root) == nil {
			for _, name := range []string{"session_id", "thread_id", "conversation_id", "threadId", "sessionId"} {
				var value string
				if raw, ok := root[name]; ok && json.Unmarshal(raw, &value) == nil {
					if value = strings.TrimSpace(value); value != "" {
						seed = value
						break
					}
				}
				if seed == "" {
					for _, container := range []string{"context", "metadata"} {
						var nested map[string]json.RawMessage
						if raw, ok := root[container]; !ok || json.Unmarshal(raw, &nested) != nil {
							continue
						}
						for _, name := range []string{"session_id", "thread_id", "conversation_id", "threadId", "sessionId"} {
							var value string
							if raw, ok := nested[name]; ok && json.Unmarshal(raw, &value) == nil {
								if value = strings.TrimSpace(value); value != "" {
									seed = value
									break
								}
							}
						}
						if seed != "" {
							break
						}
					}
				}
			}
		}
	}
	if seed == "" {
		seed = string(body)
	}
	h := sha256.Sum256([]byte(seed))
	return fmt.Sprintf("codex-aux:%d:%d:%d:%s", apiKey.UserID, apiKey.ID, derefGroupID(apiKey.GroupID), hex.EncodeToString(h[:]))
}

package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

const (
	openAIExcelNativeTTL     = 7 * 24 * time.Hour
	openAIExcelAttachmentTTL = 24 * time.Hour
	openAIExcelStateMaxBytes = 1 << 20
)

var ErrOpenAIExcelStateUnavailable = errors.New("excel session storage unavailable")
var ErrOpenAIExcelStateNotFound = errors.New("excel session state not found")

// OpenAIExcelStateBackend stores ciphertext only. A bounded sorted-set index
// limits each session's entries atomically across all gateway instances.
type OpenAIExcelStateBackend interface {
	PutOpenAIExcelState(context.Context, string, string, string, time.Duration, int) error
	GetOpenAIExcelState(context.Context, string, string) (string, error)
}

type OpenAIExcelStateStore struct {
	backend OpenAIExcelStateBackend
	cipher  SecretEncryptor
}

func NewOpenAIExcelStateStore(backend OpenAIExcelStateBackend, cipher SecretEncryptor) *OpenAIExcelStateStore {
	return &OpenAIExcelStateStore{backend: backend, cipher: cipher}
}

type openAIExcelStateEnvelope struct {
	Scope     string          `json:"scope"`
	Key       string          `json:"key"`
	ExpiresAt int64           `json:"expires_at"`
	Payload   json.RawMessage `json:"payload"`
}

func excelStateDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (s *OpenAIExcelStateStore) put(ctx context.Context, scope, kind, id string, value json.RawMessage, ttl time.Duration, limit int) error {
	if s == nil || s.backend == nil || s.cipher == nil || scope == "" || id == "" {
		return ErrOpenAIExcelStateUnavailable
	}
	if len(value) > openAIExcelStateMaxBytes || !json.Valid(value) {
		return errors.New("excel session item exceeds storage limit or is invalid")
	}
	scope = excelStateDigest(scope + "|" + kind)
	key := excelStateDigest(id)
	plain, err := json.Marshal(openAIExcelStateEnvelope{scope, key, time.Now().Add(ttl).Unix(), value})
	if err != nil {
		return ErrOpenAIExcelStateUnavailable
	}
	sealed, err := s.cipher.Encrypt(string(plain))
	if err != nil {
		return ErrOpenAIExcelStateUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := s.backend.PutOpenAIExcelState(ctx, scope, key, sealed, ttl, limit); err != nil {
		return ErrOpenAIExcelStateUnavailable
	}
	return nil
}

func (s *OpenAIExcelStateStore) get(ctx context.Context, scope, kind, id string) (json.RawMessage, error) {
	if s == nil || s.backend == nil || s.cipher == nil || scope == "" || id == "" {
		return nil, ErrOpenAIExcelStateUnavailable
	}
	scope = excelStateDigest(scope + "|" + kind)
	key := excelStateDigest(id)
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	sealed, err := s.backend.GetOpenAIExcelState(ctx, scope, key)
	if err != nil {
		if errors.Is(err, ErrOpenAIExcelStateNotFound) {
			return nil, err
		}
		return nil, ErrOpenAIExcelStateUnavailable
	}
	plain, err := s.cipher.Decrypt(sealed)
	if err != nil {
		return nil, ErrOpenAIExcelStateUnavailable
	}
	var item openAIExcelStateEnvelope
	if json.Unmarshal([]byte(plain), &item) != nil || item.Scope != scope || item.Key != key {
		return nil, ErrOpenAIExcelStateUnavailable
	}
	if item.ExpiresAt <= time.Now().Unix() {
		return nil, ErrOpenAIExcelStateNotFound
	}
	return item.Payload, nil
}

func (s *OpenAIExcelStateStore) LoadExcelNativeCall(ctx context.Context, scope, callID string) (json.RawMessage, error) {
	item, err := s.get(ctx, scope, "native", callID)
	if errors.Is(err, ErrOpenAIExcelStateNotFound) {
		return nil, ErrOpenAIExcelHistoryNotFound
	}
	return item, err
}

func (s *OpenAIExcelStateStore) StoreExcelNativeCall(ctx context.Context, scope, callID string, item json.RawMessage) error {
	// Keep only the native call fields needed to round-trip the protocol.
	var input map[string]json.RawMessage
	if json.Unmarshal(item, &input) != nil {
		return errors.New("invalid Excel native call")
	}
	filtered := make(map[string]json.RawMessage)
	for _, key := range []string{"type", "id", "call_id", "name", "namespace", "arguments", "status", "input"} {
		if v, ok := input[key]; ok {
			filtered[key] = v
		}
	}
	item, err := json.Marshal(filtered)
	if err != nil {
		return err
	}
	return s.put(ctx, scope, "native", callID, item, openAIExcelNativeTTL, 512)
}

type openAIExcelRequestScopeKey struct{}
type openAIExcelRequestScope struct{ Tenant, Session string }

func withOpenAIExcelRequestScope(ctx context.Context, c *gin.Context, account *Account, rawBody ...[]byte) context.Context {
	scope, exists := ctx.Value(openAIExcelRequestScopeKey{}).(openAIExcelRequestScope)
	if exists && scope.Session != "" {
		return ctx
	}
	if !exists {
		scope = openAIExcelRequestScope{Tenant: "internal"}
	}
	if c != nil {
		scope.Tenant = fmt.Sprintf("group:%d:key:%d", getOpenAIGroupIDFromContext(c), getAPIKeyIDFromContext(c))
		if c.Request != nil {
			scope.Session = firstNonEmpty(c.Request.Header.Get("session_id"), c.Request.Header.Get("conversation_id"))
		}
		if capture, ok := OpenAIOAuthIdentityCaptureFromContext(c); ok && scope.Session == "" {
			scope.Session = firstNonEmpty(capture.Logical.SessionKey, capture.PromptCacheKey.Value)
		}
	}
	if scope.Session == "" && len(rawBody) > 0 {
		scope.Session = gjson.GetBytes(rawBody[0], "prompt_cache_key").String()
	}
	return context.WithValue(ctx, openAIExcelRequestScopeKey{}, scope)
}

func (s *OpenAIGatewayService) openAIExcelHistoryScope(ctx context.Context, account *Account, headers http.Header, body []byte) string {
	scope, _ := ctx.Value(openAIExcelRequestScopeKey{}).(openAIExcelRequestScope)
	if scope.Tenant == "" {
		scope.Tenant = "internal"
	}
	if scope.Session == "" {
		scope.Session = strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String())
	}
	if scope.Session == "" {
		scope.Session = firstNonEmpty(headers.Get("session_id"), headers.Get("conversation_id"))
	}
	// No shared default session: uncorrelated calls must never share history.
	if scope.Session == "" {
		scope.Session = uuid.NewString()
	}
	grant := strings.TrimSpace(account.OpenAIOAuthAuthorizationGeneration)
	if grant == "" {
		// A chatgpt_account_id identifies the account, not an authorization
		// generation. Reusing it here could cross-contaminate state after
		// re-authorization, so stateful Excel history fails closed instead.
		return ""
	}
	return fmt.Sprintf("excel-v1|%d|%s|%s|%s|%s|%s", account.ID, grant, account.OpenAIUpstreamKind(), account.OpenAIUpstreamRouteGeneration(), scope.Tenant, scope.Session)
}

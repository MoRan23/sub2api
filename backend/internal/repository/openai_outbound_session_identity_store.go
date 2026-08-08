package repository

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const (
	// OpenAIOutboundSessionIdentityKeyPrefix is deliberately versioned so a
	// future derivation format can coexist without interpreting old values.
	OpenAIOutboundSessionIdentityKeyPrefix  = "openai-outbound-session:v1:"
	defaultOpenAIOutboundSessionIdentityTTL = 30 * 24 * time.Hour
)

var openAIOutboundSessionIdentityGetOrCreateScript = redis.NewScript(`
local key = KEYS[1]
local candidate = ARGV[1]
local ttl = tonumber(ARGV[2])
if ttl == nil or ttl < 1 then
  ttl = 1
end

local current = redis.call('GET', key)
if current and current ~= false then
  return current
end

-- The script is atomic, so the first writer wins even when many gateway
-- instances miss the key at the same time.
redis.call('SET', key, candidate, 'EX', ttl, 'NX')
current = redis.call('GET', key)
if current and current ~= false then
  redis.call('EXPIRE', key, ttl)
  return current
end
return candidate
`)

// Reconcile is a value-CAS, not a blind overwrite. Go validates the observed
// bytes first. A corrupt value is repaired only while those exact bytes still
// own the key; a concurrent repair/writer is read back as the winner. Valid
// reads refresh TTL only while the validated value is still current.
var openAIOutboundSessionIdentityReconcileScript = redis.NewScript(`
local key = KEYS[1]
local expected = ARGV[1]
local candidate = ARGV[2]
local ttl = tonumber(ARGV[3])
local repair = ARGV[4]
if ttl == nil or ttl < 1 then
  ttl = 1
end

local current = redis.call('GET', key)
if current == expected then
  if repair == '1' then
    redis.call('SET', key, candidate, 'EX', ttl)
    return candidate
  end
  redis.call('EXPIRE', key, ttl)
  return current
end

if current == false then
  redis.call('SET', key, candidate, 'EX', ttl, 'NX')
  current = redis.call('GET', key)
end
return current
`)

type openAIOutboundSessionIdentityRedisStore struct {
	rdb *redis.Client
}

// NewOpenAIOutboundSessionIdentityStore returns a Redis-backed narrow store.
// The gateway normally uses gatewayCache directly; this constructor is useful
// for focused tests and for callers that do not otherwise need GatewayCache.
func NewOpenAIOutboundSessionIdentityStore(rdb *redis.Client) service.OpenAIOutboundSessionIdentityStore {
	return &openAIOutboundSessionIdentityRedisStore{rdb: rdb}
}

// NewRedisOpenAIOutboundSessionIdentityStore is an explicit alias for callers
// that prefer the backend type in the constructor name.
func NewRedisOpenAIOutboundSessionIdentityStore(rdb *redis.Client) service.OpenAIOutboundSessionIdentityStore {
	return NewOpenAIOutboundSessionIdentityStore(rdb)
}

func (s *openAIOutboundSessionIdentityRedisStore) GetOrCreate(ctx context.Context, mappingKey string, candidate service.OpenAIOutboundSessionIdentity, ttl time.Duration) (service.OpenAIOutboundSessionIdentity, error) {
	if s == nil || s.rdb == nil {
		return service.OpenAIOutboundSessionIdentity{}, errors.New("openai outbound session identity redis store is unavailable")
	}
	return getOrCreateOpenAIOutboundSessionIdentity(ctx, s.rdb, mappingKey, candidate, ttl)
}

// GetOrCreate implements service.OpenAIOutboundSessionIdentityStore on the
// shared gateway cache without widening service.GatewayCache.
func (c *gatewayCache) GetOrCreate(ctx context.Context, mappingKey string, candidate service.OpenAIOutboundSessionIdentity, ttl time.Duration) (service.OpenAIOutboundSessionIdentity, error) {
	if c == nil || c.rdb == nil {
		return service.OpenAIOutboundSessionIdentity{}, errors.New("openai outbound session identity redis store is unavailable")
	}
	return getOrCreateOpenAIOutboundSessionIdentity(ctx, c.rdb, mappingKey, candidate, ttl)
}

func getOrCreateOpenAIOutboundSessionIdentity(ctx context.Context, rdb *redis.Client, mappingKey string, candidate service.OpenAIOutboundSessionIdentity, ttl time.Duration) (service.OpenAIOutboundSessionIdentity, error) {
	mappingKey = strings.TrimSpace(mappingKey)
	if !validOpenAIOutboundSessionIdentityMappingKey(mappingKey) {
		return service.OpenAIOutboundSessionIdentity{}, errors.New("openai outbound session identity mapping key must be a lowercase SHA-256 digest")
	}
	if err := service.ValidateOpenAIOutboundSessionIdentity(candidate); err != nil {
		return service.OpenAIOutboundSessionIdentity{}, err
	}
	if ttl <= 0 {
		ttl = defaultOpenAIOutboundSessionIdentityTTL
	}
	seconds := ttl / time.Second
	if seconds < 1 {
		seconds = 1
	}
	payload, err := json.Marshal(candidate)
	if err != nil {
		return service.OpenAIOutboundSessionIdentity{}, fmt.Errorf("marshal OpenAI outbound session identity: %w", err)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	result, err := openAIOutboundSessionIdentityGetOrCreateScript.Run(
		ctx,
		rdb,
		[]string{openAIOutboundSessionIdentityRedisKey(mappingKey)},
		string(payload),
		seconds,
	).Text()
	if err != nil {
		return service.OpenAIOutboundSessionIdentity{}, fmt.Errorf("get or create OpenAI outbound session identity: %w", err)
	}

	// A bounded loop handles a writer changing the key between our read and
	// value-CAS. Normal traffic completes in one reconciliation; corrupt values
	// complete in two (repair, then validated return).
	observed := result
	for attempt := 0; attempt < 6; attempt++ {
		identity, validationErr := decodeOpenAIOutboundSessionIdentity(observed)
		repair := "0"
		if validationErr != nil {
			repair = "1"
		}
		next, reconcileErr := openAIOutboundSessionIdentityReconcileScript.Run(
			ctx,
			rdb,
			[]string{openAIOutboundSessionIdentityRedisKey(mappingKey)},
			observed,
			string(payload),
			seconds,
			repair,
		).Text()
		if reconcileErr != nil {
			return service.OpenAIOutboundSessionIdentity{}, fmt.Errorf("reconcile OpenAI outbound session identity: %w", reconcileErr)
		}
		if validationErr == nil && next == observed {
			return identity, nil
		}
		observed = next
	}
	identity, err := decodeOpenAIOutboundSessionIdentity(observed)
	if err != nil {
		return service.OpenAIOutboundSessionIdentity{}, fmt.Errorf("%w after CAS repair: %v", service.ErrOpenAIOutboundSessionIdentityStoredValueInvalid, err)
	}
	return identity, nil
}

func decodeOpenAIOutboundSessionIdentity(raw string) (service.OpenAIOutboundSessionIdentity, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	start, err := decoder.Token()
	if err != nil {
		return service.OpenAIOutboundSessionIdentity{}, fmt.Errorf("decode OpenAI outbound session identity: %w", err)
	}
	if delim, ok := start.(json.Delim); !ok || delim != '{' {
		return service.OpenAIOutboundSessionIdentity{}, errors.New("decode OpenAI outbound session identity: expected object")
	}
	fields := make(map[string]json.RawMessage, 2)
	for decoder.More() {
		keyToken, tokenErr := decoder.Token()
		if tokenErr != nil {
			return service.OpenAIOutboundSessionIdentity{}, fmt.Errorf("decode OpenAI outbound session identity field: %w", tokenErr)
		}
		key, ok := keyToken.(string)
		if !ok {
			return service.OpenAIOutboundSessionIdentity{}, errors.New("decode OpenAI outbound session identity: non-string field name")
		}
		if key != "session_id" && key != "thread_id" {
			return service.OpenAIOutboundSessionIdentity{}, errors.New("decode OpenAI outbound session identity: unexpected fields")
		}
		if _, duplicate := fields[key]; duplicate {
			return service.OpenAIOutboundSessionIdentity{}, fmt.Errorf("decode OpenAI outbound session identity: duplicate field %q", key)
		}
		var value json.RawMessage
		if decodeErr := decoder.Decode(&value); decodeErr != nil {
			return service.OpenAIOutboundSessionIdentity{}, fmt.Errorf("decode OpenAI outbound session identity field %q: %w", key, decodeErr)
		}
		fields[key] = value
	}
	end, err := decoder.Token()
	if err != nil {
		return service.OpenAIOutboundSessionIdentity{}, fmt.Errorf("decode OpenAI outbound session identity: %w", err)
	}
	if delim, ok := end.(json.Delim); !ok || delim != '}' {
		return service.OpenAIOutboundSessionIdentity{}, errors.New("decode OpenAI outbound session identity: unterminated object")
	}
	var trailing json.RawMessage
	if trailingErr := decoder.Decode(&trailing); !errors.Is(trailingErr, io.EOF) {
		if trailingErr == nil {
			return service.OpenAIOutboundSessionIdentity{}, errors.New("decode OpenAI outbound session identity: trailing value")
		}
		return service.OpenAIOutboundSessionIdentity{}, fmt.Errorf("decode OpenAI outbound session identity trailing data: %w", trailingErr)
	}
	if len(fields) != 2 {
		return service.OpenAIOutboundSessionIdentity{}, errors.New("decode OpenAI outbound session identity: unexpected fields")
	}
	sessionRaw, ok := fields["session_id"]
	if !ok {
		return service.OpenAIOutboundSessionIdentity{}, errors.New("decode OpenAI outbound session identity: session_id is missing")
	}
	threadRaw, ok := fields["thread_id"]
	if !ok {
		return service.OpenAIOutboundSessionIdentity{}, errors.New("decode OpenAI outbound session identity: thread_id is missing")
	}
	var identity service.OpenAIOutboundSessionIdentity
	if err := json.Unmarshal(sessionRaw, &identity.SessionID); err != nil {
		return service.OpenAIOutboundSessionIdentity{}, fmt.Errorf("decode OpenAI outbound session identity session_id: %w", err)
	}
	if err := json.Unmarshal(threadRaw, &identity.ThreadID); err != nil {
		return service.OpenAIOutboundSessionIdentity{}, fmt.Errorf("decode OpenAI outbound session identity thread_id: %w", err)
	}
	if err := service.ValidateOpenAIOutboundSessionIdentity(identity); err != nil {
		return service.OpenAIOutboundSessionIdentity{}, fmt.Errorf("validate OpenAI outbound session identity: %w", err)
	}
	return identity, nil
}

func validOpenAIOutboundSessionIdentityMappingKey(mappingKey string) bool {
	if len(mappingKey) != sha256HexLength || strings.ToLower(mappingKey) != mappingKey {
		return false
	}
	_, err := hex.DecodeString(mappingKey)
	return err == nil
}

const sha256HexLength = 64

func openAIOutboundSessionIdentityRedisKey(mappingKey string) string {
	return OpenAIOutboundSessionIdentityKeyPrefix + strings.TrimSpace(mappingKey)
}

// OpenAIOutboundSessionIdentityRedisKey exposes key construction for
// integration tests and operational diagnostics without exposing identity
// values themselves.
func OpenAIOutboundSessionIdentityRedisKey(mappingKey string) string {
	if !validOpenAIOutboundSessionIdentityMappingKey(strings.TrimSpace(mappingKey)) {
		return ""
	}
	return openAIOutboundSessionIdentityRedisKey(mappingKey)
}

var _ service.OpenAIOutboundSessionIdentityStore = (*openAIOutboundSessionIdentityRedisStore)(nil)
var _ service.OpenAIOutboundSessionIdentityStore = (*gatewayCache)(nil)

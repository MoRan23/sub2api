package repository

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const (
	OpenAICodexTurnIdentityKeyPrefix = "openai-outbound-session:v2:"
	// Deprecated name retained for diagnostics; it now points exclusively to V2.
	OpenAIOutboundSessionIdentityKeyPrefix = OpenAICodexTurnIdentityKeyPrefix
	defaultOpenAICodexTurnIdentityTTL      = 30 * 24 * time.Hour
	sha256HexLength                        = 64
)

var openAICodexGetOrCreateScript = redis.NewScript(`
local key = KEYS[1]
local candidate = ARGV[1]
local ttl = tonumber(ARGV[2])
if ttl == nil or ttl < 1 then ttl = 1 end
local current = redis.call('GET', key)
if not current then
  redis.call('SET', key, candidate, 'EX', ttl, 'NX')
  current = redis.call('GET', key)
end
if current then
  redis.call('EXPIRE', key, ttl)
  return current
end
return candidate
`)

var openAICodexReconcileScript = redis.NewScript(`
local key = KEYS[1]
local expected = ARGV[1]
local candidate = ARGV[2]
local ttl = tonumber(ARGV[3])
local repair = ARGV[4]
if ttl == nil or ttl < 1 then ttl = 1 end
local current = redis.call('GET', key)
if current == expected then
  if repair == '1' then
    redis.call('SET', key, candidate, 'EX', ttl)
    return candidate
  end
  redis.call('EXPIRE', key, ttl)
  return current
end
if not current then
  redis.call('SET', key, candidate, 'EX', ttl, 'NX')
  current = redis.call('GET', key)
end
return current
`)

// Both keys are inspected in one script. The child is created/refreshed only
// while it is still owned by the exact session winner supplied by the caller.
var openAICodexThreadGetOrCreateScript = redis.NewScript(`
local session_key = KEYS[1]
local thread_key = KEYS[2]
local expected_session = ARGV[1]
local candidate = ARGV[2]
local ttl = tonumber(ARGV[3])
if ttl == nil or ttl < 1 then ttl = 1 end
local raw_session = redis.call('GET', session_key)
if not raw_session then return redis.error_reply('CODEX_SESSION_WINNER_CHANGED') end
local ok, decoded = pcall(cjson.decode, raw_session)
local field_count = 0
if ok and type(decoded) == 'table' then
  for _ in pairs(decoded) do field_count = field_count + 1 end
end
if not ok or type(decoded) ~= 'table' or field_count ~= 1 or type(decoded['session_id']) ~= 'string' or decoded['session_id'] ~= expected_session then
  return redis.error_reply('CODEX_SESSION_WINNER_CHANGED')
end
redis.call('EXPIRE', session_key, ttl)
local current = redis.call('GET', thread_key)
if not current then
  redis.call('SET', thread_key, candidate, 'EX', ttl, 'NX')
  current = redis.call('GET', thread_key)
end
if current then
  redis.call('EXPIRE', thread_key, ttl)
  return current
end
return candidate
`)

var openAICodexThreadReconcileScript = redis.NewScript(`
local session_key = KEYS[1]
local thread_key = KEYS[2]
local expected_session = ARGV[1]
local observed = ARGV[2]
local candidate = ARGV[3]
local ttl = tonumber(ARGV[4])
local repair = ARGV[5]
if ttl == nil or ttl < 1 then ttl = 1 end
local raw_session = redis.call('GET', session_key)
if not raw_session then return redis.error_reply('CODEX_SESSION_WINNER_CHANGED') end
local ok, decoded = pcall(cjson.decode, raw_session)
local field_count = 0
if ok and type(decoded) == 'table' then
  for _ in pairs(decoded) do field_count = field_count + 1 end
end
if not ok or type(decoded) ~= 'table' or field_count ~= 1 or type(decoded['session_id']) ~= 'string' or decoded['session_id'] ~= expected_session then
  return redis.error_reply('CODEX_SESSION_WINNER_CHANGED')
end
redis.call('EXPIRE', session_key, ttl)
local current = redis.call('GET', thread_key)
if current == observed then
  if repair == '1' then
    redis.call('SET', thread_key, candidate, 'EX', ttl)
    return candidate
  end
  redis.call('EXPIRE', thread_key, ttl)
  return current
end
if not current then
  redis.call('SET', thread_key, candidate, 'EX', ttl, 'NX')
  current = redis.call('GET', thread_key)
end
return current
`)

var openAICodexSessionAliasesScript = redis.NewScript(`
local candidate = ARGV[1]
local ttl = tonumber(ARGV[2])
if ttl == nil or ttl < 1 then ttl = 1 end
local function is_uuid_v7(value)
  return type(value) == 'string' and value == string.lower(value) and
    string.match(value, '^%x%x%x%x%x%x%x%x%-%x%x%x%x%-7%x%x%x%-[89ab]%x%x%x%-%x%x%x%x%x%x%x%x%x%x%x%x$') ~= nil
end
local winner = nil
local reused = 0
local claimed = 0
local conflicts = 0
for _, key in ipairs(KEYS) do
  local raw = redis.call('GET', key)
  if raw then
    local ok, decoded = pcall(cjson.decode, raw)
    local count = 0
    if ok and type(decoded) == 'table' then for _ in pairs(decoded) do count = count + 1 end end
    if ok and type(decoded) == 'table' and count == 1 and is_uuid_v7(decoded['session_id']) then
      if winner and winner ~= decoded['session_id'] then conflicts = conflicts + 1 end
      if not winner then winner = decoded['session_id'] end
      reused = 1
    end
  end
end
if not winner then
  local decoded = cjson.decode(candidate)
  winner = decoded['session_id']
end
local payload = cjson.encode({session_id=winner})
for _, key in ipairs(KEYS) do
  if not redis.call('GET', key) then claimed = claimed + 1 end
  redis.call('SET', key, payload, 'EX', ttl)
end
return {payload, tostring(reused), tostring(claimed), tostring(conflicts)}
`)

var openAICodexThreadAliasesScript = redis.NewScript(`
local expected_session = ARGV[1]
local candidate = ARGV[2]
local ttl = tonumber(ARGV[3])
if ttl == nil or ttl < 1 then ttl = 1 end
local function is_uuid_v7(value)
  return type(value) == 'string' and value == string.lower(value) and
    string.match(value, '^%x%x%x%x%x%x%x%x%-%x%x%x%x%-7%x%x%x%-[89ab]%x%x%x%-%x%x%x%x%x%x%x%x%x%x%x%x$') ~= nil
end
for index=1,#KEYS,2 do
  local raw_session = redis.call('GET', KEYS[index])
  if not raw_session then return redis.error_reply('CODEX_SESSION_WINNER_CHANGED') end
  local session_ok, session = pcall(cjson.decode, raw_session)
  local session_count = 0
  if session_ok and type(session) == 'table' then for _ in pairs(session) do session_count = session_count + 1 end end
  if not session_ok or type(session) ~= 'table' or session_count ~= 1 or
      not is_uuid_v7(session['session_id']) or session['session_id'] ~= expected_session then
    return redis.error_reply('CODEX_SESSION_WINNER_CHANGED')
  end
end
local winner = nil
local reused = 0
local claimed = 0
local conflicts = 0
for index=2,#KEYS,2 do
  local raw = redis.call('GET', KEYS[index])
  if raw then
    local ok, decoded = pcall(cjson.decode, raw)
    local count = 0
    if ok and type(decoded) == 'table' then for _ in pairs(decoded) do count = count + 1 end end
    if ok and type(decoded) == 'table' and count == 2 and decoded['session_id'] == expected_session and
        is_uuid_v7(decoded['session_id']) and is_uuid_v7(decoded['thread_id']) and decoded['thread_id'] ~= expected_session then
      if winner and winner ~= decoded['thread_id'] then conflicts = conflicts + 1 end
      if not winner then winner = decoded['thread_id'] end
      reused = 1
    end
  end
end
if not winner then
  local decoded = cjson.decode(candidate)
  winner = decoded['thread_id']
end
local payload = cjson.encode({session_id=expected_session,thread_id=winner})
for index=1,#KEYS,2 do redis.call('EXPIRE', KEYS[index], ttl) end
for index=2,#KEYS,2 do
  if not redis.call('GET', KEYS[index]) then claimed = claimed + 1 end
  redis.call('SET', KEYS[index], payload, 'EX', ttl)
end
return {payload, tostring(reused), tostring(claimed), tostring(conflicts)}
`)

type openAICodexTurnIdentityRedisStore struct{ rdb *redis.Client }
type openAIOutboundSessionIdentityRedisStore = openAICodexTurnIdentityRedisStore

func NewOpenAICodexTurnIdentityStore(rdb *redis.Client) service.OpenAICodexTurnIdentityStore {
	return &openAICodexTurnIdentityRedisStore{rdb: rdb}
}

// Deprecated constructors now return the V2 hierarchical store.
func NewOpenAIOutboundSessionIdentityStore(rdb *redis.Client) service.OpenAICodexTurnIdentityStore {
	return NewOpenAICodexTurnIdentityStore(rdb)
}
func NewRedisOpenAIOutboundSessionIdentityStore(rdb *redis.Client) service.OpenAICodexTurnIdentityStore {
	return NewOpenAICodexTurnIdentityStore(rdb)
}

func (s *openAICodexTurnIdentityRedisStore) GetOrCreateCodexSession(ctx context.Context, sessionMappingKey, candidateSessionID string, ttl time.Duration) (string, error) {
	if s == nil || s.rdb == nil {
		return "", errors.New("openai Codex identity Redis store is unavailable")
	}
	return getOrCreateOpenAICodexSession(ctx, s.rdb, sessionMappingKey, candidateSessionID, ttl)
}

func (s *openAICodexTurnIdentityRedisStore) GetOrCreateCodexThread(ctx context.Context, sessionMappingKey, threadMappingKey, sessionID, candidateThreadID string, ttl time.Duration) (service.OpenAICodexTurnIdentity, error) {
	if s == nil || s.rdb == nil {
		return service.OpenAICodexTurnIdentity{}, errors.New("openai Codex identity Redis store is unavailable")
	}
	return getOrCreateOpenAICodexThread(ctx, s.rdb, sessionMappingKey, threadMappingKey, sessionID, candidateThreadID, ttl)
}

func (s *openAICodexTurnIdentityRedisStore) GetOrCreateCodexSessionAliases(ctx context.Context, keys []string, candidate string, ttl time.Duration) (service.OpenAICodexAliasStoreResolution, error) {
	return getOrCreateOpenAICodexSessionAliases(ctx, s.rdb, keys, candidate, ttl)
}

func (s *openAICodexTurnIdentityRedisStore) GetOrCreateCodexThreadAliases(ctx context.Context, mappings []service.OpenAICodexThreadAliasMapping, sessionID, candidate string, ttl time.Duration) (service.OpenAICodexAliasStoreResolution, error) {
	return getOrCreateOpenAICodexThreadAliases(ctx, s.rdb, mappings, sessionID, candidate, ttl)
}

func (c *gatewayCache) GetOrCreateCodexSession(ctx context.Context, sessionMappingKey, candidateSessionID string, ttl time.Duration) (string, error) {
	if c == nil || c.rdb == nil {
		return "", errors.New("openai Codex identity Redis store is unavailable")
	}
	return getOrCreateOpenAICodexSession(ctx, c.rdb, sessionMappingKey, candidateSessionID, ttl)
}

func (c *gatewayCache) GetOrCreateCodexThread(ctx context.Context, sessionMappingKey, threadMappingKey, sessionID, candidateThreadID string, ttl time.Duration) (service.OpenAICodexTurnIdentity, error) {
	if c == nil || c.rdb == nil {
		return service.OpenAICodexTurnIdentity{}, errors.New("openai Codex identity Redis store is unavailable")
	}
	return getOrCreateOpenAICodexThread(ctx, c.rdb, sessionMappingKey, threadMappingKey, sessionID, candidateThreadID, ttl)
}

func (c *gatewayCache) GetOrCreateCodexSessionAliases(ctx context.Context, keys []string, candidate string, ttl time.Duration) (service.OpenAICodexAliasStoreResolution, error) {
	return getOrCreateOpenAICodexSessionAliases(ctx, c.rdb, keys, candidate, ttl)
}

func (c *gatewayCache) GetOrCreateCodexThreadAliases(ctx context.Context, mappings []service.OpenAICodexThreadAliasMapping, sessionID, candidate string, ttl time.Duration) (service.OpenAICodexAliasStoreResolution, error) {
	return getOrCreateOpenAICodexThreadAliases(ctx, c.rdb, mappings, sessionID, candidate, ttl)
}

func normalizedIdentityTTL(ttl time.Duration) int64 {
	if ttl <= 0 {
		ttl = defaultOpenAICodexTurnIdentityTTL
	}
	seconds := int64(ttl / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	return seconds
}

func getOrCreateOpenAICodexSessionAliases(ctx context.Context, rdb *redis.Client, mappingKeys []string, candidateSessionID string, ttl time.Duration) (service.OpenAICodexAliasStoreResolution, error) {
	if rdb == nil {
		return service.OpenAICodexAliasStoreResolution{}, errors.New("openai Codex identity Redis store is unavailable")
	}
	root := service.OpenAICodexTurnIdentity{SessionID: candidateSessionID, ThreadID: candidateSessionID, Relation: service.OpenAICodexTurnRelationRoot}
	if err := service.ValidateOpenAICodexTurnIdentity(root); err != nil {
		return service.OpenAICodexAliasStoreResolution{}, err
	}
	redisKeys := make([]string, 0, len(mappingKeys))
	seen := make(map[string]struct{}, len(mappingKeys))
	for _, mappingKey := range mappingKeys {
		if !validOpenAICodexMappingKey(mappingKey) {
			return service.OpenAICodexAliasStoreResolution{}, errors.New("openai Codex session mapping key must be a lowercase SHA-256 digest")
		}
		if _, ok := seen[mappingKey]; ok {
			continue
		}
		seen[mappingKey] = struct{}{}
		redisKeys = append(redisKeys, OpenAICodexSessionIdentityRedisKey(mappingKey))
	}
	if len(redisKeys) == 0 {
		return service.OpenAICodexAliasStoreResolution{}, errors.New("openai Codex session aliases are empty")
	}
	payload, _ := json.Marshal(struct {
		SessionID string `json:"session_id"`
	}{candidateSessionID})
	values, err := openAICodexSessionAliasesScript.Run(ctx, rdb, redisKeys, string(payload), normalizedIdentityTTL(ttl)).Slice()
	if err != nil {
		if strings.Contains(err.Error(), "CODEX_ALIAS_CONFLICT") {
			return service.OpenAICodexAliasStoreResolution{}, service.ErrOpenAICodexAliasConflict
		}
		return service.OpenAICodexAliasStoreResolution{}, fmt.Errorf("get or create OpenAI Codex session aliases: %w", err)
	}
	if len(values) != 4 {
		return service.OpenAICodexAliasStoreResolution{}, errors.New("OpenAI Codex session alias script returned invalid result")
	}
	raw, _ := values[0].(string)
	sessionID, err := decodeOpenAICodexSession(raw)
	if err != nil {
		return service.OpenAICodexAliasStoreResolution{}, err
	}
	return service.OpenAICodexAliasStoreResolution{
		Identity: service.OpenAICodexTurnIdentity{SessionID: sessionID, ThreadID: sessionID, Relation: service.OpenAICodexTurnRelationRoot},
		Reused:   fmt.Sprint(values[1]) == "1", AliasesClaimed: parseRedisInteger(values[2]),
		ConflictsResolved: parseRedisInteger(values[3]),
	}, nil
}

func getOrCreateOpenAICodexThreadAliases(ctx context.Context, rdb *redis.Client, mappings []service.OpenAICodexThreadAliasMapping, sessionID, candidateThreadID string, ttl time.Duration) (service.OpenAICodexAliasStoreResolution, error) {
	candidate := service.OpenAICodexTurnIdentity{SessionID: sessionID, ThreadID: candidateThreadID, Relation: service.OpenAICodexTurnRelationDescendant}
	if err := service.ValidateOpenAICodexTurnIdentity(candidate); err != nil {
		return service.OpenAICodexAliasStoreResolution{}, err
	}
	redisKeys := make([]string, 0, len(mappings)*2)
	seen := make(map[string]struct{}, len(mappings))
	for _, mapping := range mappings {
		if !validOpenAICodexMappingKey(mapping.SessionMappingKey) {
			return service.OpenAICodexAliasStoreResolution{}, errors.New("openai Codex session mapping key must be a lowercase SHA-256 digest")
		}
		if !validOpenAICodexMappingKey(mapping.ThreadMappingKey) {
			return service.OpenAICodexAliasStoreResolution{}, errors.New("openai Codex thread mapping key must be a lowercase SHA-256 digest")
		}
		pairKey := mapping.SessionMappingKey + "\x00" + mapping.ThreadMappingKey
		if _, ok := seen[pairKey]; ok {
			continue
		}
		seen[pairKey] = struct{}{}
		redisKeys = append(redisKeys,
			OpenAICodexSessionIdentityRedisKey(mapping.SessionMappingKey),
			OpenAICodexThreadIdentityRedisKey(mapping.SessionMappingKey, mapping.ThreadMappingKey),
		)
	}
	if len(redisKeys) == 0 {
		return service.OpenAICodexAliasStoreResolution{}, errors.New("openai Codex thread aliases are empty")
	}
	payload, _ := json.Marshal(struct {
		SessionID string `json:"session_id"`
		ThreadID  string `json:"thread_id"`
	}{sessionID, candidateThreadID})
	values, err := openAICodexThreadAliasesScript.Run(ctx, rdb, redisKeys, sessionID, string(payload), normalizedIdentityTTL(ttl)).Slice()
	if err != nil {
		if strings.Contains(err.Error(), "CODEX_ALIAS_CONFLICT") {
			return service.OpenAICodexAliasStoreResolution{}, service.ErrOpenAICodexAliasConflict
		}
		if strings.Contains(err.Error(), "CODEX_SESSION_WINNER_CHANGED") {
			return service.OpenAICodexAliasStoreResolution{}, service.ErrOpenAICodexSessionWinnerChanged
		}
		return service.OpenAICodexAliasStoreResolution{}, fmt.Errorf("get or create OpenAI Codex thread aliases: %w", err)
	}
	if len(values) != 4 {
		return service.OpenAICodexAliasStoreResolution{}, errors.New("OpenAI Codex thread alias script returned invalid result")
	}
	raw, _ := values[0].(string)
	identity, err := decodeOpenAICodexThread(raw, sessionID)
	if err != nil {
		return service.OpenAICodexAliasStoreResolution{}, err
	}
	return service.OpenAICodexAliasStoreResolution{Identity: identity, Reused: fmt.Sprint(values[1]) == "1", AliasesClaimed: parseRedisInteger(values[2]), ConflictsResolved: parseRedisInteger(values[3])}, nil
}

func parseRedisInteger(value any) int {
	parsed, _ := strconv.Atoi(fmt.Sprint(value))
	return parsed
}

func getOrCreateOpenAICodexSession(ctx context.Context, rdb *redis.Client, mappingKey, candidateSessionID string, ttl time.Duration) (string, error) {
	if !validOpenAICodexMappingKey(mappingKey) {
		return "", errors.New("openai Codex session mapping key must be a lowercase SHA-256 digest")
	}
	root := service.OpenAICodexTurnIdentity{SessionID: candidateSessionID, ThreadID: candidateSessionID, Relation: service.OpenAICodexTurnRelationRoot}
	if err := service.ValidateOpenAICodexTurnIdentity(root); err != nil {
		return "", err
	}
	payload, _ := json.Marshal(struct {
		SessionID string `json:"session_id"`
	}{candidateSessionID})
	if ctx == nil {
		ctx = context.Background()
	}
	key := OpenAICodexSessionIdentityRedisKey(mappingKey)
	observed, err := openAICodexGetOrCreateScript.Run(ctx, rdb, []string{key}, string(payload), normalizedIdentityTTL(ttl)).Text()
	if err != nil {
		return "", fmt.Errorf("get or create OpenAI Codex session: %w", err)
	}
	for attempt := 0; attempt < 6; attempt++ {
		sessionID, validationErr := decodeOpenAICodexSession(observed)
		repair := "0"
		if validationErr != nil {
			repair = "1"
		}
		next, reconcileErr := openAICodexReconcileScript.Run(ctx, rdb, []string{key}, observed, string(payload), normalizedIdentityTTL(ttl), repair).Text()
		if reconcileErr != nil {
			return "", fmt.Errorf("reconcile OpenAI Codex session: %w", reconcileErr)
		}
		if validationErr == nil && next == observed {
			return sessionID, nil
		}
		observed = next
	}
	if _, err := decodeOpenAICodexSession(observed); err != nil {
		return "", fmt.Errorf("%w after session CAS repair: %v", service.ErrOpenAIOutboundSessionIdentityStoredValueInvalid, err)
	}
	return decodeOpenAICodexSession(observed)
}

func getOrCreateOpenAICodexThread(ctx context.Context, rdb *redis.Client, sessionMappingKey, threadMappingKey, sessionID, candidateThreadID string, ttl time.Duration) (service.OpenAICodexTurnIdentity, error) {
	if !validOpenAICodexMappingKey(sessionMappingKey) || !validOpenAICodexMappingKey(threadMappingKey) {
		return service.OpenAICodexTurnIdentity{}, errors.New("openai Codex thread mapping keys must be lowercase SHA-256 digests")
	}
	candidate := service.OpenAICodexTurnIdentity{SessionID: sessionID, ThreadID: candidateThreadID, Relation: service.OpenAICodexTurnRelationDescendant}
	if err := service.ValidateOpenAICodexTurnIdentity(candidate); err != nil {
		return service.OpenAICodexTurnIdentity{}, err
	}
	payload, _ := json.Marshal(struct {
		SessionID string `json:"session_id"`
		ThreadID  string `json:"thread_id"`
	}{sessionID, candidateThreadID})
	if ctx == nil {
		ctx = context.Background()
	}
	keys := []string{OpenAICodexSessionIdentityRedisKey(sessionMappingKey), OpenAICodexThreadIdentityRedisKey(sessionMappingKey, threadMappingKey)}
	observed, err := openAICodexThreadGetOrCreateScript.Run(ctx, rdb, keys, sessionID, string(payload), normalizedIdentityTTL(ttl)).Text()
	if err != nil {
		return service.OpenAICodexTurnIdentity{}, mapOpenAICodexRedisError("get or create OpenAI Codex thread", err)
	}
	for attempt := 0; attempt < 6; attempt++ {
		identity, validationErr := decodeOpenAICodexThread(observed, sessionID)
		repair := "0"
		if validationErr != nil {
			repair = "1"
		}
		next, reconcileErr := openAICodexThreadReconcileScript.Run(ctx, rdb, keys, sessionID, observed, string(payload), normalizedIdentityTTL(ttl), repair).Text()
		if reconcileErr != nil {
			return service.OpenAICodexTurnIdentity{}, mapOpenAICodexRedisError("reconcile OpenAI Codex thread", reconcileErr)
		}
		if validationErr == nil && next == observed {
			return identity, nil
		}
		observed = next
	}
	identity, err := decodeOpenAICodexThread(observed, sessionID)
	if err != nil {
		return service.OpenAICodexTurnIdentity{}, fmt.Errorf("%w after thread CAS repair: %v", service.ErrOpenAIOutboundSessionIdentityStoredValueInvalid, err)
	}
	return identity, nil
}

func mapOpenAICodexRedisError(operation string, err error) error {
	if strings.Contains(err.Error(), "CODEX_SESSION_WINNER_CHANGED") {
		return fmt.Errorf("%s: %w", operation, service.ErrOpenAICodexSessionWinnerChanged)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func decodeStrictOpenAICodexObject(raw string, allowed map[string]struct{}) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	start, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := start.(json.Delim); !ok || delim != '{' {
		return nil, errors.New("expected object")
	}
	fields := make(map[string]json.RawMessage, len(allowed))
	for decoder.More() {
		nameToken, tokenErr := decoder.Token()
		if tokenErr != nil {
			return nil, tokenErr
		}
		name, ok := nameToken.(string)
		if !ok {
			return nil, errors.New("non-string field name")
		}
		if _, ok := allowed[name]; !ok {
			return nil, fmt.Errorf("unexpected field %q", name)
		}
		if _, duplicate := fields[name]; duplicate {
			return nil, fmt.Errorf("duplicate field %q", name)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		fields[name] = value
	}
	end, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := end.(json.Delim); !ok || delim != '}' {
		return nil, errors.New("unterminated object")
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("trailing value")
		}
		return nil, err
	}
	if len(fields) != len(allowed) {
		return nil, errors.New("missing or unexpected fields")
	}
	return fields, nil
}

func decodeOpenAICodexSession(raw string) (string, error) {
	fields, err := decodeStrictOpenAICodexObject(raw, map[string]struct{}{"session_id": {}})
	if err != nil {
		return "", err
	}
	var sessionID string
	if err := json.Unmarshal(fields["session_id"], &sessionID); err != nil {
		return "", err
	}
	identity := service.OpenAICodexTurnIdentity{SessionID: sessionID, ThreadID: sessionID, Relation: service.OpenAICodexTurnRelationRoot}
	if err := service.ValidateOpenAICodexTurnIdentity(identity); err != nil {
		return "", err
	}
	return sessionID, nil
}

func decodeOpenAICodexThread(raw, expectedSessionID string) (service.OpenAICodexTurnIdentity, error) {
	fields, err := decodeStrictOpenAICodexObject(raw, map[string]struct{}{"session_id": {}, "thread_id": {}})
	if err != nil {
		return service.OpenAICodexTurnIdentity{}, err
	}
	identity := service.OpenAICodexTurnIdentity{Relation: service.OpenAICodexTurnRelationDescendant}
	if err := json.Unmarshal(fields["session_id"], &identity.SessionID); err != nil {
		return service.OpenAICodexTurnIdentity{}, err
	}
	if err := json.Unmarshal(fields["thread_id"], &identity.ThreadID); err != nil {
		return service.OpenAICodexTurnIdentity{}, err
	}
	if identity.SessionID != expectedSessionID {
		return service.OpenAICodexTurnIdentity{}, errors.New("thread belongs to a different session")
	}
	if err := service.ValidateOpenAICodexTurnIdentity(identity); err != nil {
		return service.OpenAICodexTurnIdentity{}, err
	}
	return identity, nil
}

func validOpenAICodexMappingKey(mappingKey string) bool {
	mappingKey = strings.TrimSpace(mappingKey)
	if len(mappingKey) != sha256HexLength || strings.ToLower(mappingKey) != mappingKey {
		return false
	}
	_, err := hex.DecodeString(mappingKey)
	return err == nil
}

func OpenAICodexSessionIdentityRedisKey(sessionMappingKey string) string {
	if !validOpenAICodexMappingKey(sessionMappingKey) {
		return ""
	}
	return OpenAICodexTurnIdentityKeyPrefix + strings.TrimSpace(sessionMappingKey) + ":session"
}

func OpenAICodexThreadIdentityRedisKey(sessionMappingKey, threadMappingKey string) string {
	if !validOpenAICodexMappingKey(sessionMappingKey) || !validOpenAICodexMappingKey(threadMappingKey) {
		return ""
	}
	return OpenAICodexTurnIdentityKeyPrefix + strings.TrimSpace(sessionMappingKey) + ":thread:" + strings.TrimSpace(threadMappingKey)
}

func OpenAIOutboundSessionIdentityRedisKey(mappingKey string) string {
	return OpenAICodexSessionIdentityRedisKey(mappingKey)
}

var _ service.OpenAICodexTurnIdentityStore = (*openAICodexTurnIdentityRedisStore)(nil)
var _ service.OpenAICodexTurnIdentityStore = (*gatewayCache)(nil)

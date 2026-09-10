package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const OpenAICodexDownstreamIdentityKeyPrefix = "openai-downstream-identity:v1:"

// Identity records contain only UUIDs and an optional account namespace. Parse
// their complete wire shape, rather than allowing cjson to discard duplicate
// fields, before a script can claim or refresh any keys.
const openAICodexDownstreamRecordLua = `
local function is_uuid_v7(value)
  return type(value) == 'string' and value == string.lower(value) and
    string.match(value, '^%x%x%x%x%x%x%x%x%-%x%x%x%x%-7%x%x%x%-[89ab]%x%x%x%-%x%x%x%x%x%x%x%x%x%x%x%x$') ~= nil
end
local function is_namespace(value)
  if value == '' then return true end
  if type(value) ~= 'string' then return false end
  local id = string.match(value, '^account:([1-9]%d*)$')
  return id ~= nil and (#id < 19 or (#id == 19 and id <= '9223372036854775807'))
end
local function session_record(raw)
  local session, namespace = string.match(raw, '^%s*{%s*"session_id"%s*:%s*"([^"]*)"%s*,%s*"legacy_namespace"%s*:%s*"([^"]*)"%s*}%s*$')
  if not session then
    namespace, session = string.match(raw, '^%s*{%s*"legacy_namespace"%s*:%s*"([^"]*)"%s*,%s*"session_id"%s*:%s*"([^"]*)"%s*}%s*$')
  end
  if not is_uuid_v7(session) or not is_namespace(namespace) then return nil end
  return {session_id=session, legacy_namespace=namespace}
end
local function legacy_session_record(raw)
  local session = string.match(raw, '^%s*{%s*"session_id"%s*:%s*"([^"]*)"%s*}%s*$')
  if not is_uuid_v7(session) then return nil end
  return session
end
local function thread_record(raw)
  local session, thread = string.match(raw, '^%s*{%s*"session_id"%s*:%s*"([^"]*)"%s*,%s*"thread_id"%s*:%s*"([^"]*)"%s*}%s*$')
  if not session then
    thread, session = string.match(raw, '^%s*{%s*"thread_id"%s*:%s*"([^"]*)"%s*,%s*"session_id"%s*:%s*"([^"]*)"%s*}%s*$')
  end
  if not is_uuid_v7(session) or not is_uuid_v7(thread) or session == thread then return nil end
  return {session_id=session, thread_id=thread}
end
`

var openAICodexDownstreamSessionScript = redis.NewScript(openAICodexDownstreamRecordLua + `
local candidate = ARGV[1]
local source_namespace = ARGV[2]
local ttl = tonumber(ARGV[3])
local canonical_count = tonumber(ARGV[4])
local winner = nil
local reused = 0
for index=1,canonical_count do
  local raw = redis.call('GET', KEYS[index])
  if raw then
    local current = session_record(raw)
    if not current then return redis.error_reply('CODEX_STORED_VALUE_INVALID') end
    if winner and (winner.session_id ~= current.session_id or winner.legacy_namespace ~= current.legacy_namespace) then
      return redis.error_reply('CODEX_ALIAS_CONFLICT')
    end
    winner = current
    reused = 1
  end
end
if not winner then
  for index=canonical_count+1,#KEYS do
    local raw = redis.call('GET', KEYS[index])
    if raw then
      local session = legacy_session_record(raw)
      if not session then return redis.error_reply('CODEX_STORED_VALUE_INVALID') end
      if not winner then winner = {session_id=session, legacy_namespace=source_namespace} end
      reused = 1
    end
  end
end
if not winner then winner = {session_id=candidate, legacy_namespace=''} end
local payload = cjson.encode(winner)
for index=1,canonical_count do redis.call('SET', KEYS[index], payload, 'EX', ttl) end
return {payload, tostring(reused)}
`)

var openAICodexDownstreamThreadScript = redis.NewScript(openAICodexDownstreamRecordLua + `
local expected_session = ARGV[1]
local expected_namespace = ARGV[2]
local candidate = ARGV[3]
local ttl = tonumber(ARGV[4])
local session_count = tonumber(ARGV[5])
local thread_count = tonumber(ARGV[6])
for index=1,session_count do
  local raw = redis.call('GET', KEYS[index])
  if not raw then return redis.error_reply('CODEX_SESSION_WINNER_CHANGED') end
  local current = session_record(raw)
  if not current then return redis.error_reply('CODEX_STORED_VALUE_INVALID') end
  if current.session_id ~= expected_session or current.legacy_namespace ~= expected_namespace then
    return redis.error_reply('CODEX_SESSION_WINNER_CHANGED')
  end
end
local winner = nil
for index=session_count+1,session_count+thread_count do
  local raw = redis.call('GET', KEYS[index])
  if raw then
    local current = thread_record(raw)
    if not current or current.session_id ~= expected_session then return redis.error_reply('CODEX_STORED_VALUE_INVALID') end
    if winner and winner ~= current.thread_id then return redis.error_reply('CODEX_ALIAS_CONFLICT') end
    winner = current.thread_id
  end
end
if not winner then
  for index=session_count+thread_count+1,#KEYS,2 do
    local raw_session = redis.call('GET', KEYS[index])
    if raw_session then
      local session = legacy_session_record(raw_session)
      if not session then return redis.error_reply('CODEX_STORED_VALUE_INVALID') end
      -- An expired/recreated legacy source is no longer the migrated session.
      if session == expected_session then
        local raw = redis.call('GET', KEYS[index+1])
        if raw then
          local current = thread_record(raw)
          if not current or current.session_id ~= expected_session then return redis.error_reply('CODEX_STORED_VALUE_INVALID') end
          if not winner then winner = current.thread_id end
        end
      end
    end
  end
end
if not winner then winner = candidate end
local payload = cjson.encode({session_id=expected_session, thread_id=winner})
for index=1,session_count do redis.call('EXPIRE', KEYS[index], ttl) end
for index=session_count+1,session_count+thread_count do redis.call('SET', KEYS[index], payload, 'EX', ttl) end
return payload
`)

type openAICodexDownstreamIdentityRedisStore struct{ rdb *redis.Client }

func NewOpenAICodexDownstreamIdentityStore(rdb *redis.Client) service.OpenAICodexDownstreamIdentityStore {
	return &openAICodexDownstreamIdentityRedisStore{rdb: rdb}
}

func (c *gatewayCache) ResolveCodexDownstreamSession(ctx context.Context, request service.OpenAICodexDownstreamSessionRequest, ttl time.Duration) (service.OpenAICodexDownstreamSessionResolution, error) {
	if c == nil {
		return service.OpenAICodexDownstreamSessionResolution{}, errors.New("openai Codex downstream identity Redis store is unavailable")
	}
	return (&openAICodexDownstreamIdentityRedisStore{rdb: c.rdb}).ResolveCodexDownstreamSession(ctx, request, ttl)
}

func (c *gatewayCache) ResolveCodexDownstreamThread(ctx context.Context, request service.OpenAICodexDownstreamThreadRequest, ttl time.Duration) (service.OpenAICodexTurnIdentity, error) {
	if c == nil {
		return service.OpenAICodexTurnIdentity{}, errors.New("openai Codex downstream identity Redis store is unavailable")
	}
	return (&openAICodexDownstreamIdentityRedisStore{rdb: c.rdb}).ResolveCodexDownstreamThread(ctx, request, ttl)
}

func (s *openAICodexDownstreamIdentityRedisStore) ResolveCodexDownstreamSession(ctx context.Context, request service.OpenAICodexDownstreamSessionRequest, ttl time.Duration) (service.OpenAICodexDownstreamSessionResolution, error) {
	if s == nil || s.rdb == nil {
		return service.OpenAICodexDownstreamSessionResolution{}, errors.New("openai Codex downstream identity Redis store is unavailable")
	}
	root := service.OpenAICodexTurnIdentity{SessionID: request.CandidateSessionID, ThreadID: request.CandidateSessionID, Relation: service.OpenAICodexTurnRelationRoot}
	if request.CandidateSessionID != strings.TrimSpace(request.CandidateSessionID) {
		return service.OpenAICodexDownstreamSessionResolution{}, errors.New("openai Codex downstream session candidate must be a canonical UUIDv7")
	}
	if err := service.ValidateOpenAICodexTurnIdentity(root); err != nil {
		return service.OpenAICodexDownstreamSessionResolution{}, err
	}
	if !validOpenAICodexLegacyNamespace(request.LegacyNamespace) || (len(request.LegacySessionMappingKeys) > 0 && request.LegacyNamespace == "") {
		return service.OpenAICodexDownstreamSessionResolution{}, errors.New("openai Codex downstream identity has invalid legacy namespace")
	}
	canonical, err := downstreamSessionRedisKeys(request.SessionMappingKeys)
	if err != nil {
		return service.OpenAICodexDownstreamSessionResolution{}, err
	}
	keys := append([]string(nil), canonical...)
	seenLegacy := make(map[string]struct{}, len(request.LegacySessionMappingKeys))
	for _, mapping := range request.LegacySessionMappingKeys {
		if !validOpenAICodexDownstreamMappingKey(mapping) {
			return service.OpenAICodexDownstreamSessionResolution{}, errors.New("openai Codex legacy session mapping key must be a lowercase SHA-256 digest")
		}
		if _, seen := seenLegacy[mapping]; !seen {
			keys = append(keys, OpenAICodexSessionIdentityRedisKey(mapping))
			seenLegacy[mapping] = struct{}{}
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	values, err := openAICodexDownstreamSessionScript.Run(ctx, s.rdb, keys, request.CandidateSessionID, request.LegacyNamespace, normalizedIdentityTTL(ttl), len(canonical)).Slice()
	if err != nil {
		return service.OpenAICodexDownstreamSessionResolution{}, mapOpenAICodexDownstreamRedisError("resolve OpenAI Codex downstream session", err)
	}
	if len(values) != 2 {
		return service.OpenAICodexDownstreamSessionResolution{}, errors.New("openai Codex downstream session script returned invalid result")
	}
	raw, ok := values[0].(string)
	if !ok {
		return service.OpenAICodexDownstreamSessionResolution{}, service.ErrOpenAIOutboundSessionIdentityStoredValueInvalid
	}
	result, err := decodeOpenAICodexDownstreamSession(raw)
	if err != nil {
		return service.OpenAICodexDownstreamSessionResolution{}, err
	}
	result.Reused = fmt.Sprint(values[1]) == "1"
	return result, nil
}

func (s *openAICodexDownstreamIdentityRedisStore) ResolveCodexDownstreamThread(ctx context.Context, request service.OpenAICodexDownstreamThreadRequest, ttl time.Duration) (service.OpenAICodexTurnIdentity, error) {
	if s == nil || s.rdb == nil {
		return service.OpenAICodexTurnIdentity{}, errors.New("openai Codex downstream identity Redis store is unavailable")
	}
	candidate := service.OpenAICodexTurnIdentity{SessionID: request.SessionID, ThreadID: request.CandidateThreadID, Relation: service.OpenAICodexTurnRelationDescendant}
	if request.SessionID != strings.TrimSpace(request.SessionID) || request.CandidateThreadID != strings.TrimSpace(request.CandidateThreadID) {
		return service.OpenAICodexTurnIdentity{}, errors.New("openai Codex downstream thread candidate must contain canonical UUIDv7 values")
	}
	if err := service.ValidateOpenAICodexTurnIdentity(candidate); err != nil {
		return service.OpenAICodexTurnIdentity{}, err
	}
	if !validOpenAICodexLegacyNamespace(request.LegacyNamespace) || (len(request.LegacyThreadMappings) > 0 && request.LegacyNamespace == "") {
		return service.OpenAICodexTurnIdentity{}, errors.New("openai Codex downstream thread has invalid legacy namespace")
	}
	sessions, err := downstreamSessionRedisKeys(request.SessionMappingKeys)
	if err != nil {
		return service.OpenAICodexTurnIdentity{}, err
	}
	knownSessions := make(map[string]struct{}, len(sessions))
	for _, key := range sessions {
		knownSessions[key] = struct{}{}
	}
	threads := make([]string, 0, len(request.ThreadMappings))
	seenThreads := make(map[string]struct{}, len(request.ThreadMappings))
	for _, mapping := range request.ThreadMappings {
		if !validOpenAICodexDownstreamMappingKey(mapping.SessionMappingKey) || !validOpenAICodexDownstreamMappingKey(mapping.ThreadMappingKey) {
			return service.OpenAICodexTurnIdentity{}, errors.New("openai Codex downstream thread mapping keys must be lowercase SHA-256 digests")
		}
		if _, ok := knownSessions[OpenAICodexDownstreamSessionIdentityRedisKey(mapping.SessionMappingKey)]; !ok {
			return service.OpenAICodexTurnIdentity{}, errors.New("openai Codex downstream thread alias has no corresponding session alias")
		}
		key := OpenAICodexDownstreamThreadIdentityRedisKey(mapping.SessionMappingKey, mapping.ThreadMappingKey)
		if _, seen := seenThreads[key]; !seen {
			threads = append(threads, key)
			seenThreads[key] = struct{}{}
		}
	}
	if len(threads) == 0 {
		return service.OpenAICodexTurnIdentity{}, errors.New("openai Codex downstream thread aliases are empty")
	}
	keys := append(append([]string(nil), sessions...), threads...)
	seenLegacy := make(map[string]struct{}, len(request.LegacyThreadMappings))
	for _, mapping := range request.LegacyThreadMappings {
		if !validOpenAICodexDownstreamMappingKey(mapping.SessionMappingKey) || !validOpenAICodexDownstreamMappingKey(mapping.ThreadMappingKey) {
			return service.OpenAICodexTurnIdentity{}, errors.New("openai Codex legacy thread mapping keys must be lowercase SHA-256 digests")
		}
		key := OpenAICodexThreadIdentityRedisKey(mapping.SessionMappingKey, mapping.ThreadMappingKey)
		if _, seen := seenLegacy[key]; !seen {
			keys = append(keys, OpenAICodexSessionIdentityRedisKey(mapping.SessionMappingKey), key)
			seenLegacy[key] = struct{}{}
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	raw, err := openAICodexDownstreamThreadScript.Run(ctx, s.rdb, keys, request.SessionID, request.LegacyNamespace, request.CandidateThreadID, normalizedIdentityTTL(ttl), len(sessions), len(threads)).Text()
	if err != nil {
		return service.OpenAICodexTurnIdentity{}, mapOpenAICodexDownstreamRedisError("resolve OpenAI Codex downstream thread", err)
	}
	identity, err := decodeOpenAICodexThread(raw, request.SessionID)
	if err != nil {
		return service.OpenAICodexTurnIdentity{}, fmt.Errorf("%w: %v", service.ErrOpenAIOutboundSessionIdentityStoredValueInvalid, err)
	}
	return identity, nil
}

func downstreamSessionRedisKeys(mappings []string) ([]string, error) {
	keys := make([]string, 0, len(mappings))
	seen := make(map[string]struct{}, len(mappings))
	for _, mapping := range mappings {
		if !validOpenAICodexDownstreamMappingKey(mapping) {
			return nil, errors.New("openai Codex downstream session mapping key must be a lowercase SHA-256 digest")
		}
		if _, exists := seen[mapping]; !exists {
			keys = append(keys, OpenAICodexDownstreamSessionIdentityRedisKey(mapping))
			seen[mapping] = struct{}{}
		}
	}
	if len(keys) == 0 {
		return nil, errors.New("openai Codex downstream session aliases are empty")
	}
	return keys, nil
}

func validOpenAICodexDownstreamMappingKey(key string) bool {
	return key == strings.TrimSpace(key) && validOpenAICodexMappingKey(key)
}

func validOpenAICodexLegacyNamespace(namespace string) bool {
	if namespace == "" {
		return true
	}
	if !strings.HasPrefix(namespace, "account:") {
		return false
	}
	id := strings.TrimPrefix(namespace, "account:")
	n, err := strconv.ParseInt(id, 10, 64)
	return err == nil && n > 0 && strconv.FormatInt(n, 10) == id
}

func decodeOpenAICodexDownstreamSession(raw string) (service.OpenAICodexDownstreamSessionResolution, error) {
	fields, err := decodeStrictOpenAICodexObject(raw, map[string]struct{}{"session_id": {}, "legacy_namespace": {}})
	result := service.OpenAICodexDownstreamSessionResolution{}
	if err == nil {
		err = json.Unmarshal(fields["session_id"], &result.SessionID)
	}
	if err == nil {
		err = json.Unmarshal(fields["legacy_namespace"], &result.LegacyNamespace)
	}
	if err == nil {
		err = service.ValidateOpenAICodexTurnIdentity(service.OpenAICodexTurnIdentity{SessionID: result.SessionID, ThreadID: result.SessionID, Relation: service.OpenAICodexTurnRelationRoot})
	}
	if err == nil && !validOpenAICodexLegacyNamespace(result.LegacyNamespace) {
		err = errors.New("invalid legacy namespace")
	}
	if err != nil {
		return service.OpenAICodexDownstreamSessionResolution{}, fmt.Errorf("%w: %v", service.ErrOpenAIOutboundSessionIdentityStoredValueInvalid, err)
	}
	return result, nil
}

func mapOpenAICodexDownstreamRedisError(operation string, err error) error {
	for marker, mapped := range map[string]error{
		"CODEX_STORED_VALUE_INVALID":   service.ErrOpenAIOutboundSessionIdentityStoredValueInvalid,
		"WRONGTYPE":                    service.ErrOpenAIOutboundSessionIdentityStoredValueInvalid,
		"CODEX_ALIAS_CONFLICT":         service.ErrOpenAICodexAliasConflict,
		"CODEX_SESSION_WINNER_CHANGED": service.ErrOpenAICodexSessionWinnerChanged,
	} {
		if strings.Contains(err.Error(), marker) {
			return fmt.Errorf("%s: %w", operation, mapped)
		}
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func OpenAICodexDownstreamSessionIdentityRedisKey(sessionMappingKey string) string {
	if !validOpenAICodexDownstreamMappingKey(sessionMappingKey) {
		return ""
	}
	return OpenAICodexDownstreamIdentityKeyPrefix + "session:" + sessionMappingKey
}

func OpenAICodexDownstreamThreadIdentityRedisKey(sessionMappingKey, threadMappingKey string) string {
	if !validOpenAICodexDownstreamMappingKey(sessionMappingKey) || !validOpenAICodexDownstreamMappingKey(threadMappingKey) {
		return ""
	}
	return OpenAICodexDownstreamIdentityKeyPrefix + "thread:" + sessionMappingKey + ":" + threadMappingKey
}

var _ service.OpenAICodexDownstreamIdentityStore = (*openAICodexDownstreamIdentityRedisStore)(nil)
var _ service.OpenAICodexDownstreamIdentityStore = (*gatewayCache)(nil)

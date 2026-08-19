package repository

import (
	"context"
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

const OpenAICodexWindowKeyPrefix = "openai:codex:window:v1:"

var openAICodexWindowResolveScript = redis.NewScript(`
local key = KEYS[1]
local expected_thread = ARGV[1]
local candidate_raw = ARGV[2]
local ttl = tonumber(ARGV[3])
if ttl == nil or ttl < 1 then ttl = 1 end

local function is_lower_digest(value)
  return type(value) == 'string' and string.len(value) == 64 and
    value == string.lower(value) and string.match(value, '^%x+$') ~= nil
end

local function decode_snapshot(raw)
  local ok, value = pcall(cjson.decode, raw)
  if not ok or type(value) ~= 'table' then return nil end
  local count = 0
  for _ in pairs(value) do count = count + 1 end
  if count ~= 3 or type(value['thread_id']) ~= 'string' or
      type(value['window_number']) ~= 'number' or
      value['window_number'] < 0 or value['window_number'] > 9007199254740991 or
      value['window_number'] ~= math.floor(value['window_number']) or
      type(value['last_compact_digest']) ~= 'string' then
    return nil
  end
  if value['window_number'] == 0 and value['last_compact_digest'] ~= '' then return nil end
  if value['window_number'] > 0 and not is_lower_digest(value['last_compact_digest']) then return nil end
  return value
end

local candidate = decode_snapshot(candidate_raw)
if not candidate or candidate['thread_id'] ~= expected_thread then
  return redis.error_reply('CODEX_WINDOW_INVALID_CANDIDATE')
end
local current_raw = redis.call('GET', key)
if not current_raw then
  redis.call('SET', key, candidate_raw, 'EX', ttl)
  return candidate_raw
end
local current = decode_snapshot(current_raw)
if not current or current['thread_id'] ~= expected_thread then
  return redis.error_reply('CODEX_WINDOW_INVALID_STORED_VALUE')
end
if candidate['window_number'] > current['window_number'] then
  redis.call('SET', key, candidate_raw, 'EX', ttl)
  return candidate_raw
end
redis.call('EXPIRE', key, ttl)
return current_raw
`)

var openAICodexWindowCommitScript = redis.NewScript(`
local key = KEYS[1]
local expected_thread = ARGV[1]
local expected_number = tonumber(ARGV[2])
local compact_digest = ARGV[3]
local ttl = tonumber(ARGV[4])
if ttl == nil or ttl < 1 then ttl = 1 end
if expected_number == nil or expected_number < 0 or expected_number >= 9007199254740991 or
    expected_number ~= math.floor(expected_number) then
  return redis.error_reply('CODEX_WINDOW_INVALID_EXPECTED')
end

local function is_lower_digest(value)
  return type(value) == 'string' and string.len(value) == 64 and
    value == string.lower(value) and string.match(value, '^%x+$') ~= nil
end

local function decode_snapshot(raw)
  local ok, value = pcall(cjson.decode, raw)
  if not ok or type(value) ~= 'table' then return nil end
  local count = 0
  for _ in pairs(value) do count = count + 1 end
  if count ~= 3 or type(value['thread_id']) ~= 'string' or
      type(value['window_number']) ~= 'number' or
      value['window_number'] < 0 or value['window_number'] > 9007199254740991 or
      value['window_number'] ~= math.floor(value['window_number']) or
      type(value['last_compact_digest']) ~= 'string' then
    return nil
  end
  if value['window_number'] == 0 and value['last_compact_digest'] ~= '' then return nil end
  if value['window_number'] > 0 and not is_lower_digest(value['last_compact_digest']) then return nil end
  return value
end

if not is_lower_digest(compact_digest) then
  return redis.error_reply('CODEX_WINDOW_INVALID_COMPACT_DIGEST')
end
local current_raw = redis.call('GET', key)
if not current_raw then
  local advanced = cjson.encode({
    thread_id=expected_thread,
    window_number=expected_number + 1,
    last_compact_digest=compact_digest
  })
  redis.call('SET', key, advanced, 'EX', ttl)
  return {advanced, 'advanced'}
end
local current = decode_snapshot(current_raw)
if not current or current['thread_id'] ~= expected_thread then
  return redis.error_reply('CODEX_WINDOW_INVALID_STORED_VALUE')
end
redis.call('EXPIRE', key, ttl)
if current['last_compact_digest'] == compact_digest then
  return {current_raw, 'already_committed'}
end
if current['window_number'] ~= expected_number then
  return {current_raw, 'stale'}
end
local advanced = cjson.encode({
  thread_id=expected_thread,
  window_number=expected_number + 1,
  last_compact_digest=compact_digest
})
redis.call('SET', key, advanced, 'EX', ttl)
return {advanced, 'advanced'}
`)

type openAICodexWindowRedisStore struct{ rdb *redis.Client }

func NewOpenAICodexWindowStore(rdb *redis.Client) service.OpenAICodexWindowStore {
	return &openAICodexWindowRedisStore{rdb: rdb}
}

func (s *openAICodexWindowRedisStore) ResolveOpenAICodexWindow(ctx context.Context, mappingKey string, candidate service.OpenAICodexWindowSnapshot, ttl time.Duration) (service.OpenAICodexWindowSnapshot, error) {
	if s == nil || s.rdb == nil {
		return service.OpenAICodexWindowSnapshot{}, service.ErrOpenAICodexWindowStoreUnavailable
	}
	return resolveOpenAICodexWindow(ctx, s.rdb, mappingKey, candidate, ttl)
}

func (s *openAICodexWindowRedisStore) CommitOpenAICodexWindow(ctx context.Context, mappingKey, threadID string, expected uint64, compactDigest string, ttl time.Duration) (service.OpenAICodexWindowCommitResult, error) {
	if s == nil || s.rdb == nil {
		return service.OpenAICodexWindowCommitResult{}, service.ErrOpenAICodexWindowStoreUnavailable
	}
	return commitOpenAICodexWindow(ctx, s.rdb, mappingKey, threadID, expected, compactDigest, ttl)
}

func (c *gatewayCache) ResolveOpenAICodexWindow(ctx context.Context, mappingKey string, candidate service.OpenAICodexWindowSnapshot, ttl time.Duration) (service.OpenAICodexWindowSnapshot, error) {
	if c == nil || c.rdb == nil {
		return service.OpenAICodexWindowSnapshot{}, service.ErrOpenAICodexWindowStoreUnavailable
	}
	return resolveOpenAICodexWindow(ctx, c.rdb, mappingKey, candidate, ttl)
}

func (c *gatewayCache) CommitOpenAICodexWindow(ctx context.Context, mappingKey, threadID string, expected uint64, compactDigest string, ttl time.Duration) (service.OpenAICodexWindowCommitResult, error) {
	if c == nil || c.rdb == nil {
		return service.OpenAICodexWindowCommitResult{}, service.ErrOpenAICodexWindowStoreUnavailable
	}
	return commitOpenAICodexWindow(ctx, c.rdb, mappingKey, threadID, expected, compactDigest, ttl)
}

func resolveOpenAICodexWindow(ctx context.Context, rdb *redis.Client, mappingKey string, candidate service.OpenAICodexWindowSnapshot, ttl time.Duration) (service.OpenAICodexWindowSnapshot, error) {
	redisKey, err := OpenAICodexWindowRedisKey(mappingKey)
	if err != nil {
		return service.OpenAICodexWindowSnapshot{}, err
	}
	if err := service.ValidateOpenAICodexWindowSnapshot(candidate); err != nil {
		return service.OpenAICodexWindowSnapshot{}, err
	}
	payload, err := json.Marshal(candidate)
	if err != nil {
		return service.OpenAICodexWindowSnapshot{}, err
	}
	raw, err := openAICodexWindowResolveScript.Run(ctx, rdb, []string{redisKey}, candidate.ThreadID, string(payload), normalizedOpenAICodexWindowTTLSeconds(ttl)).Text()
	if err != nil {
		return service.OpenAICodexWindowSnapshot{}, fmt.Errorf("resolve openai Codex window: %w", err)
	}
	winner, err := decodeStrictOpenAICodexWindowSnapshot([]byte(raw))
	if err != nil || winner.ThreadID != candidate.ThreadID {
		return service.OpenAICodexWindowSnapshot{}, service.ErrOpenAICodexWindowStoredInvalid
	}
	return winner, nil
}

func commitOpenAICodexWindow(ctx context.Context, rdb *redis.Client, mappingKey, threadID string, expected uint64, compactDigest string, ttl time.Duration) (service.OpenAICodexWindowCommitResult, error) {
	redisKey, err := OpenAICodexWindowRedisKey(mappingKey)
	if err != nil {
		return service.OpenAICodexWindowCommitResult{}, err
	}
	// Reuse service validation without manufacturing a persisted intermediate
	// snapshot: a missing Redis key may validly resume at expected > 0 after a
	// local fallback period.
	if expected >= service.OpenAICodexWindowMaxNumber || !validOpenAICodexMappingKey(compactDigest) {
		return service.OpenAICodexWindowCommitResult{}, errors.New("invalid openai Codex window commit")
	}
	initial := service.OpenAICodexWindowSnapshot{ThreadID: threadID}
	if err := service.ValidateOpenAICodexWindowSnapshot(initial); err != nil {
		return service.OpenAICodexWindowCommitResult{}, err
	}
	result, err := openAICodexWindowCommitScript.Run(ctx, rdb, []string{redisKey}, threadID, strconv.FormatUint(expected, 10), compactDigest, normalizedOpenAICodexWindowTTLSeconds(ttl)).Slice()
	if err != nil {
		return service.OpenAICodexWindowCommitResult{}, fmt.Errorf("commit openai Codex window: %w", err)
	}
	if len(result) != 2 {
		return service.OpenAICodexWindowCommitResult{}, service.ErrOpenAICodexWindowStoredInvalid
	}
	raw, ok := result[0].(string)
	if !ok {
		return service.OpenAICodexWindowCommitResult{}, service.ErrOpenAICodexWindowStoredInvalid
	}
	statusRaw, ok := result[1].(string)
	if !ok {
		return service.OpenAICodexWindowCommitResult{}, service.ErrOpenAICodexWindowStoredInvalid
	}
	snapshot, err := decodeStrictOpenAICodexWindowSnapshot([]byte(raw))
	if err != nil || snapshot.ThreadID != threadID {
		return service.OpenAICodexWindowCommitResult{}, service.ErrOpenAICodexWindowStoredInvalid
	}
	status := service.OpenAICodexWindowCommitStatus(statusRaw)
	switch status {
	case service.OpenAICodexWindowCommitAdvanced, service.OpenAICodexWindowCommitAlreadyCommitted, service.OpenAICodexWindowCommitStale:
	default:
		return service.OpenAICodexWindowCommitResult{}, service.ErrOpenAICodexWindowStoredInvalid
	}
	return service.OpenAICodexWindowCommitResult{Snapshot: snapshot, Status: status}, nil
}

func decodeStrictOpenAICodexWindowSnapshot(raw []byte) (service.OpenAICodexWindowSnapshot, error) {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var snapshot service.OpenAICodexWindowSnapshot
	if err := decoder.Decode(&snapshot); err != nil {
		return service.OpenAICodexWindowSnapshot{}, err
	}
	if err := ensureOpenAICodexWindowJSONEOF(decoder); err != nil {
		return service.OpenAICodexWindowSnapshot{}, err
	}
	if err := service.ValidateOpenAICodexWindowSnapshot(snapshot); err != nil {
		return service.OpenAICodexWindowSnapshot{}, err
	}
	return snapshot, nil
}

func ensureOpenAICodexWindowJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("openai Codex window JSON contains trailing data")
	}
	return err
}

func OpenAICodexWindowRedisKey(mappingKey string) (string, error) {
	trimmed := strings.TrimSpace(mappingKey)
	if trimmed != mappingKey || !validOpenAICodexMappingKey(mappingKey) {
		return "", errors.New("openai Codex window mapping key must be a lowercase SHA-256 digest")
	}
	return OpenAICodexWindowKeyPrefix + mappingKey, nil
}

func normalizedOpenAICodexWindowTTLSeconds(ttl time.Duration) int64 {
	if ttl <= 0 {
		ttl = service.OpenAICodexWindowTTL
	}
	seconds := int64(ttl / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	return seconds
}

var _ service.OpenAICodexWindowStore = (*openAICodexWindowRedisStore)(nil)
var _ service.OpenAICodexWindowStore = (*gatewayCache)(nil)

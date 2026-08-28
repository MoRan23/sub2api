package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
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

local function is_uuid_v7(value)
  return type(value) == 'string' and string.len(value) == 36 and
    value == string.lower(value) and
    string.match(value, '^%x%x%x%x%x%x%x%x%-%x%x%x%x%-7%x%x%x%-[89ab]%x%x%x%-%x%x%x%x%x%x%x%x%x%x%x%x$') ~= nil
end

local function decode_snapshot(raw, allow_legacy)
  local ok, value = pcall(cjson.decode, raw)
  if not ok or type(value) ~= 'table' then return nil end
  local count = 0
  for _ in pairs(value) do count = count + 1 end
  if (count ~= 4 and (not allow_legacy or count ~= 3)) or
      type(value['thread_id']) ~= 'string' or
      type(value['window_number']) ~= 'number' or
      value['window_number'] < 0 or value['window_number'] > 9007199254740991 or
      value['window_number'] ~= math.floor(value['window_number']) or
      type(value['last_compact_digest']) ~= 'string' then
    return nil
  end
  if value['window_number'] == 0 and value['last_compact_digest'] ~= '' then return nil end
  if value['window_number'] > 0 and not is_lower_digest(value['last_compact_digest']) then return nil end
  if count == 4 and not is_uuid_v7(value['context_window_id']) then return nil end
  return value, count == 3
end

local candidate, candidate_legacy = decode_snapshot(candidate_raw, false)
if not candidate or candidate_legacy or candidate['thread_id'] ~= expected_thread then
  return redis.error_reply('CODEX_WINDOW_INVALID_CANDIDATE')
end
local current_raw = redis.call('GET', key)
if not current_raw then
  redis.call('SET', key, candidate_raw, 'EX', ttl)
  return candidate_raw
end
local current, current_legacy = decode_snapshot(current_raw, true)
if not current or current['thread_id'] ~= expected_thread then
  return redis.error_reply('CODEX_WINDOW_INVALID_STORED_VALUE')
end
if candidate['window_number'] > current['window_number'] then
  redis.call('SET', key, candidate_raw, 'EX', ttl)
  return candidate_raw
end
if current_legacy then
  current['context_window_id'] = candidate['context_window_id']
  current_raw = cjson.encode(current)
  redis.call('SET', key, current_raw, 'EX', ttl)
else
  redis.call('EXPIRE', key, ttl)
end
return current_raw
`)

var openAICodexWindowCommitScript = redis.NewScript(`
local key = KEYS[1]
local expected_thread = ARGV[1]
local expected_number = tonumber(ARGV[2])
local expected_context_window_id = ARGV[3]
local compact_digest = ARGV[4]
local proposed_context_window_id = ARGV[5]
local ttl = tonumber(ARGV[6])
if ttl == nil or ttl < 1 then ttl = 1 end
if expected_number == nil or expected_number < 0 or expected_number >= 9007199254740991 or
    expected_number ~= math.floor(expected_number) then
  return redis.error_reply('CODEX_WINDOW_INVALID_EXPECTED')
end

local function is_lower_digest(value)
  return type(value) == 'string' and string.len(value) == 64 and
    value == string.lower(value) and string.match(value, '^%x+$') ~= nil
end

local function is_uuid_v7(value)
  return type(value) == 'string' and string.len(value) == 36 and
    value == string.lower(value) and
    string.match(value, '^%x%x%x%x%x%x%x%x%-%x%x%x%x%-7%x%x%x%-[89ab]%x%x%x%-%x%x%x%x%x%x%x%x%x%x%x%x$') ~= nil
end

local function decode_snapshot(raw)
  local ok, value = pcall(cjson.decode, raw)
  if not ok or type(value) ~= 'table' then return nil end
  local count = 0
  for _ in pairs(value) do count = count + 1 end
  if (count ~= 3 and count ~= 4) or type(value['thread_id']) ~= 'string' or
      type(value['window_number']) ~= 'number' or
      value['window_number'] < 0 or value['window_number'] > 9007199254740991 or
      value['window_number'] ~= math.floor(value['window_number']) or
      type(value['last_compact_digest']) ~= 'string' then
    return nil
  end
  if value['window_number'] == 0 and value['last_compact_digest'] ~= '' then return nil end
  if value['window_number'] > 0 and not is_lower_digest(value['last_compact_digest']) then return nil end
  if count == 4 and not is_uuid_v7(value['context_window_id']) then return nil end
  return value, count == 3
end

if not is_lower_digest(compact_digest) then
  return redis.error_reply('CODEX_WINDOW_INVALID_COMPACT_DIGEST')
end
if not is_uuid_v7(expected_context_window_id) then
  return redis.error_reply('CODEX_WINDOW_INVALID_EXPECTED_CONTEXT_WINDOW_ID')
end
if not is_uuid_v7(proposed_context_window_id) then
  return redis.error_reply('CODEX_WINDOW_INVALID_CONTEXT_WINDOW_ID')
end
if proposed_context_window_id == expected_context_window_id then
  return redis.error_reply('CODEX_WINDOW_CONTEXT_WINDOW_ID_NOT_ROTATED')
end
local current_raw = redis.call('GET', key)
if not current_raw then
  local advanced = cjson.encode({
    thread_id=expected_thread,
    window_number=expected_number + 1,
    context_window_id=proposed_context_window_id,
    last_compact_digest=compact_digest
  })
  redis.call('SET', key, advanced, 'EX', ttl)
  return {advanced, 'advanced'}
end
local current, current_legacy = decode_snapshot(current_raw)
if not current or current['thread_id'] ~= expected_thread then
  return redis.error_reply('CODEX_WINDOW_INVALID_STORED_VALUE')
end
if current_legacy then
  return redis.error_reply('CODEX_WINDOW_LEGACY_REQUIRES_RESOLVE')
end
if current['last_compact_digest'] == compact_digest then
  if current['window_number'] ~= expected_number + 1 or current['context_window_id'] == expected_context_window_id then
    return redis.error_reply('CODEX_WINDOW_INVALID_STORED_VALUE')
  end
  redis.call('EXPIRE', key, ttl)
  return {current_raw, 'already_committed'}
end
if current['window_number'] ~= expected_number or current['context_window_id'] ~= expected_context_window_id then
  redis.call('EXPIRE', key, ttl)
  return {current_raw, 'stale'}
end
local advanced = cjson.encode({
  thread_id=expected_thread,
  window_number=expected_number + 1,
  context_window_id=proposed_context_window_id,
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

func (s *openAICodexWindowRedisStore) CommitOpenAICodexWindow(ctx context.Context, mappingKey string, expected service.OpenAICodexWindowSnapshot, compactDigest, proposedNextContextWindowID string, ttl time.Duration) (service.OpenAICodexWindowCommitResult, error) {
	if s == nil || s.rdb == nil {
		return service.OpenAICodexWindowCommitResult{}, service.ErrOpenAICodexWindowStoreUnavailable
	}
	return commitOpenAICodexWindow(ctx, s.rdb, mappingKey, expected, compactDigest, proposedNextContextWindowID, ttl)
}

func (c *gatewayCache) ResolveOpenAICodexWindow(ctx context.Context, mappingKey string, candidate service.OpenAICodexWindowSnapshot, ttl time.Duration) (service.OpenAICodexWindowSnapshot, error) {
	if c == nil || c.rdb == nil {
		return service.OpenAICodexWindowSnapshot{}, service.ErrOpenAICodexWindowStoreUnavailable
	}
	return resolveOpenAICodexWindow(ctx, c.rdb, mappingKey, candidate, ttl)
}

func (c *gatewayCache) CommitOpenAICodexWindow(ctx context.Context, mappingKey string, expected service.OpenAICodexWindowSnapshot, compactDigest, proposedNextContextWindowID string, ttl time.Duration) (service.OpenAICodexWindowCommitResult, error) {
	if c == nil || c.rdb == nil {
		return service.OpenAICodexWindowCommitResult{}, service.ErrOpenAICodexWindowStoreUnavailable
	}
	return commitOpenAICodexWindow(ctx, c.rdb, mappingKey, expected, compactDigest, proposedNextContextWindowID, ttl)
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
		return service.OpenAICodexWindowSnapshot{}, classifyOpenAICodexWindowRedisError("resolve openai Codex window", err)
	}
	winner, err := decodeStrictOpenAICodexWindowSnapshot([]byte(raw))
	if err != nil || winner.ThreadID != candidate.ThreadID {
		return service.OpenAICodexWindowSnapshot{}, service.ErrOpenAICodexWindowStoredInvalid
	}
	return winner, nil
}

func commitOpenAICodexWindow(ctx context.Context, rdb *redis.Client, mappingKey string, expected service.OpenAICodexWindowSnapshot, compactDigest, proposedNextContextWindowID string, ttl time.Duration) (service.OpenAICodexWindowCommitResult, error) {
	redisKey, err := OpenAICodexWindowRedisKey(mappingKey)
	if err != nil {
		return service.OpenAICodexWindowCommitResult{}, err
	}
	// Reuse service validation without manufacturing a persisted intermediate
	// snapshot: a missing Redis key may validly resume at expected > 0 after a
	// local fallback period.
	if expected.Number >= service.OpenAICodexWindowMaxNumber || !validOpenAICodexMappingKey(compactDigest) {
		return service.OpenAICodexWindowCommitResult{}, errors.New("invalid openai Codex window commit")
	}
	if err := service.ValidateOpenAICodexWindowSnapshot(expected); err != nil {
		return service.OpenAICodexWindowCommitResult{}, err
	}
	proposal := service.OpenAICodexWindowSnapshot{ThreadID: expected.ThreadID, ContextWindowID: proposedNextContextWindowID}
	if err := service.ValidateOpenAICodexWindowSnapshot(proposal); err != nil {
		return service.OpenAICodexWindowCommitResult{}, err
	}
	if proposedNextContextWindowID == expected.ContextWindowID {
		return service.OpenAICodexWindowCommitResult{}, errors.New("openai Codex proposed context_window_id must differ from the expected context window")
	}
	result, err := openAICodexWindowCommitScript.Run(ctx, rdb, []string{redisKey}, expected.ThreadID, strconv.FormatUint(expected.Number, 10), expected.ContextWindowID, compactDigest, proposedNextContextWindowID, normalizedOpenAICodexWindowTTLSeconds(ttl)).Slice()
	if err != nil {
		return service.OpenAICodexWindowCommitResult{}, classifyOpenAICodexWindowRedisError("commit openai Codex window", err)
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
	if err != nil || snapshot.ThreadID != expected.ThreadID {
		return service.OpenAICodexWindowCommitResult{}, service.ErrOpenAICodexWindowStoredInvalid
	}
	status := service.OpenAICodexWindowCommitStatus(statusRaw)
	switch status {
	case service.OpenAICodexWindowCommitAdvanced:
		if snapshot.Number != expected.Number+1 || snapshot.ContextWindowID != proposedNextContextWindowID || snapshot.LastCompactDigest != compactDigest {
			return service.OpenAICodexWindowCommitResult{}, service.ErrOpenAICodexWindowStoredInvalid
		}
	case service.OpenAICodexWindowCommitAlreadyCommitted:
		if snapshot.Number != expected.Number+1 || snapshot.ContextWindowID == expected.ContextWindowID || snapshot.LastCompactDigest != compactDigest {
			return service.OpenAICodexWindowCommitResult{}, service.ErrOpenAICodexWindowStoredInvalid
		}
	case service.OpenAICodexWindowCommitStale:
		if snapshot.LastCompactDigest == compactDigest ||
			(snapshot.Number == expected.Number && snapshot.ContextWindowID == expected.ContextWindowID) {
			return service.OpenAICodexWindowCommitResult{}, service.ErrOpenAICodexWindowStoredInvalid
		}
	default:
		return service.OpenAICodexWindowCommitResult{}, service.ErrOpenAICodexWindowStoredInvalid
	}
	return service.OpenAICodexWindowCommitResult{Snapshot: snapshot, Status: status}, nil
}

func classifyOpenAICodexWindowRedisError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if redis.HasErrorPrefix(err, "CODEX_WINDOW_LEGACY_REQUIRES_RESOLVE") {
		return fmt.Errorf("%s: %w (%v)", operation, service.ErrOpenAICodexWindowLegacyRequiresResolve, err)
	}
	if redis.HasErrorPrefix(err, "CODEX_WINDOW_") {
		return fmt.Errorf("%s: %w (%v)", operation, service.ErrOpenAICodexWindowStoredInvalid, err)
	}
	if redis.HasErrorPrefix(err, "WRONGTYPE") {
		return fmt.Errorf("%s: %w (%v)", operation, service.ErrOpenAICodexWindowStoredInvalid, err)
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	var serverErr redis.Error
	if errors.As(err, &serverErr) {
		if strings.Contains(strings.ToUpper(serverErr.Error()), "WRONGTYPE") {
			return fmt.Errorf("%s: %w (%v)", operation, service.ErrOpenAICodexWindowStoredInvalid, err)
		}
		// Redis protocol replies are fail-closed. Authentication, permission,
		// read-only, configuration, and unknown server errors must not create a
		// parallel process-local window lineage.
		return fmt.Errorf("%s: %w", operation, err)
	}
	if errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, redis.ErrClosed) || errors.Is(err, redis.ErrPoolTimeout) ||
		errors.Is(err, redis.ErrPoolExhausted) {
		return fmt.Errorf("%s: %w (%v)", operation, service.ErrOpenAICodexWindowStoreUnavailable, err)
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return fmt.Errorf("%s: %w (%v)", operation, service.ErrOpenAICodexWindowStoreUnavailable, err)
	}
	// Unknown local/programming errors are not evidence that Redis is
	// unavailable. Preserve them without enabling fallback.
	return fmt.Errorf("%s: %w", operation, err)
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

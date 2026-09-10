package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// An alias preserves a migrated thread's main window and client sidecar under
// their original key. Client tokens were hashed with that key, so copying the
// records to the new key would break rollover and history restoration.
const OpenAICodexWindowMappingKeyPrefix = "openai:codex:window-mapping:v1:"

func (s *openAICodexWindowRedisStore) ResolveOpenAICodexWindowMapping(ctx context.Context, canonicalKey, legacyKey, threadID string, ttl time.Duration) (string, error) {
	if s == nil || s.rdb == nil {
		return "", service.ErrOpenAICodexWindowStoreUnavailable
	}
	return resolveOpenAICodexWindowMapping(ctx, s.rdb, canonicalKey, legacyKey, threadID, ttl)
}

func (c *gatewayCache) ResolveOpenAICodexWindowMapping(ctx context.Context, canonicalKey, legacyKey, threadID string, ttl time.Duration) (string, error) {
	if c == nil || c.rdb == nil {
		return "", service.ErrOpenAICodexWindowStoreUnavailable
	}
	return resolveOpenAICodexWindowMapping(ctx, c.rdb, canonicalKey, legacyKey, threadID, ttl)
}

func resolveOpenAICodexWindowMapping(ctx context.Context, rdb *redis.Client, canonicalKey, legacyKey, threadID string, ttl time.Duration) (string, error) {
	if _, err := OpenAICodexWindowRedisKey(canonicalKey); err != nil {
		return "", err
	}
	if legacyKey != "" {
		if _, err := OpenAICodexWindowRedisKey(legacyKey); err != nil {
			return "", err
		}
	}
	if err := service.ValidateOpenAICodexWindowSnapshot(service.OpenAICodexWindowSnapshot{ThreadID: threadID, ContextWindowID: threadID}); err != nil {
		return "", err
	}
	ttl = time.Duration(normalizedOpenAICodexWindowTTLSeconds(ttl)) * time.Second
	aliasKey := OpenAICodexWindowMappingKeyPrefix + canonicalKey
	const operation = "resolve openai Codex window mapping"
	for attempt := 0; attempt < 128; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", classifyOpenAICodexWindowRedisError(operation, err)
		}
		// Discover the previously selected key before WATCH, then read the alias
		// again with all records. A changed alias retries without creating a new
		// lineage. This also permits old aliases when the caller has no legacy key.
		observed, err := rdb.Get(ctx, aliasKey).Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			return "", classifyOpenAICodexWindowRedisError(operation, err)
		}
		aliasPresent := err == nil
		if aliasPresent {
			if _, err := OpenAICodexWindowRedisKey(observed); err != nil {
				return "", service.ErrOpenAICodexWindowStoredInvalid
			}
		}
		mappingKeys := []string{canonicalKey}
		for _, key := range []string{legacyKey, observed} {
			if key == "" {
				continue
			}
			duplicate := false
			for _, existing := range mappingKeys {
				duplicate = duplicate || existing == key
			}
			if !duplicate {
				mappingKeys = append(mappingKeys, key)
			}
		}
		keys := []string{aliasKey}
		for _, key := range mappingKeys {
			keys = append(keys, OpenAICodexWindowKeyPrefix+key, OpenAICodexClientWindowKeyPrefix+key)
		}
		var selected string
		err = rdb.Watch(ctx, func(tx *redis.Tx) error {
			values, err := tx.MGet(ctx, keys...).Result()
			if err != nil {
				return err
			}
			if len(values) != len(keys) {
				return service.ErrOpenAICodexWindowStoredInvalid
			}
			// MGET represents wrong-type keys as nil, so distinguish corruption
			// from absence before allowing a new alias/window to be created.
			validateMissingType := func(index int) error {
				if values[index] != nil {
					return nil
				}
				kind, err := tx.Type(ctx, keys[index]).Result()
				if err != nil {
					return err
				}
				if kind == "string" {
					return redis.TxFailedErr
				}
				if kind != "none" {
					return service.ErrOpenAICodexWindowStoredInvalid
				}
				return nil
			}
			if err := validateMissingType(0); err != nil {
				return err
			}
			if (values[0] != nil) != aliasPresent {
				return redis.TxFailedErr
			}
			if aliasPresent {
				raw, ok := redisMGetBytes(values[0])
				if !ok || string(raw) != observed {
					return redis.TxFailedErr
				}
				selected = observed
			}
			for i, key := range mappingKeys {
				if selected != "" && selected != key {
					continue
				}
				if err := validateMissingType(1 + i*2); err != nil {
					return err
				}
				if err := validateMissingType(2 + i*2); err != nil {
					return err
				}
				main, sidecar := values[1+i*2], values[2+i*2]
				exists, err := validateOpenAICodexWindowMappingRecords(main, sidecar, threadID)
				if err != nil {
					return err
				}
				if selected != "" || exists {
					selected = key
					break
				}
			}
			if selected == "" {
				selected = canonicalKey
			}
			_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.Set(ctx, aliasKey, selected, ttl)
				pipe.Expire(ctx, OpenAICodexWindowKeyPrefix+selected, ttl)
				pipe.Expire(ctx, OpenAICodexClientWindowKeyPrefix+selected, ttl)
				return nil
			})
			return err
		}, keys...)
		if errors.Is(err, redis.TxFailedErr) {
			continue
		}
		if err != nil {
			return "", classifyOpenAICodexWindowRedisError(operation, err)
		}
		return selected, nil
	}
	return "", errors.New("openai Codex window mapping transaction contention limit reached")
}

func validateOpenAICodexWindowMappingRecords(main, sidecar any, threadID string) (bool, error) {
	if main == nil {
		if sidecar != nil {
			return false, service.ErrOpenAICodexWindowStoredInvalid
		}
		return false, nil
	}
	mainRaw, ok := redisMGetBytes(main)
	if !ok {
		return false, service.ErrOpenAICodexWindowStoredInvalid
	}
	snapshot, lacksContext, err := decodeOpenAICodexWindowMappingSnapshot(mainRaw)
	if err != nil || snapshot.ThreadID != threadID {
		return false, service.ErrOpenAICodexWindowStoredInvalid
	}
	if sidecar != nil {
		if lacksContext {
			return false, service.ErrOpenAICodexWindowStoredInvalid
		}
		raw, ok := redisMGetBytes(sidecar)
		if !ok {
			return false, service.ErrOpenAICodexWindowStoredInvalid
		}
		binding, err := decodeStrictOpenAICodexClientWindowBinding(raw)
		if err != nil || binding.Server.ThreadID != threadID {
			return false, service.ErrOpenAICodexWindowStoredInvalid
		}
		if err := service.ValidateOpenAICodexWindowMappingPair(snapshot, &binding); err != nil {
			return false, service.ErrOpenAICodexWindowStoredInvalid
		}
	}
	return true, nil
}

// Earlier window records have three or four fields. Validate them without
// rewriting bytes: the existing Resolve operation owns their in-place upgrade
// and supplies the frozen request's context UUID when the oldest format lacks it.
func decodeOpenAICodexWindowMappingSnapshot(raw []byte) (service.OpenAICodexWindowSnapshot, bool, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return service.OpenAICodexWindowSnapshot{}, false, service.ErrOpenAICodexWindowStoredInvalid
	}
	if len(fields) == 6 {
		snapshot, err := decodeStrictOpenAICodexWindowSnapshot(raw)
		return snapshot, false, err
	}
	if len(fields) != 3 && len(fields) != 4 {
		return service.OpenAICodexWindowSnapshot{}, false, service.ErrOpenAICodexWindowStoredInvalid
	}
	for _, name := range []string{"thread_id", "window_number", "last_compact_digest"} {
		if fields[name] == nil || strings.TrimSpace(string(fields[name])) == "null" {
			return service.OpenAICodexWindowSnapshot{}, false, service.ErrOpenAICodexWindowStoredInvalid
		}
	}
	for name, value := range fields {
		switch name {
		case "thread_id", "last_compact_digest", "context_window_id":
			var decoded any
			if err := json.Unmarshal(value, &decoded); err != nil {
				return service.OpenAICodexWindowSnapshot{}, false, service.ErrOpenAICodexWindowStoredInvalid
			}
			if _, ok := decoded.(string); !ok {
				return service.OpenAICodexWindowSnapshot{}, false, service.ErrOpenAICodexWindowStoredInvalid
			}
		case "window_number":
		default:
			return service.OpenAICodexWindowSnapshot{}, false, service.ErrOpenAICodexWindowStoredInvalid
		}
	}
	var snapshot service.OpenAICodexWindowSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return snapshot, false, service.ErrOpenAICodexWindowStoredInvalid
	}
	lacksContext := len(fields) == 3
	if lacksContext {
		// Placeholder for validation only; never persists or leaves this helper.
		snapshot.ContextWindowID = snapshot.ThreadID
	}
	if snapshot.Number == 0 {
		snapshot.FirstContextWindowID = snapshot.ContextWindowID
	}
	if err := service.ValidateOpenAICodexWindowSnapshot(snapshot); err != nil {
		return snapshot, lacksContext, service.ErrOpenAICodexWindowStoredInvalid
	}
	return snapshot, lacksContext, nil
}

var _ service.OpenAICodexWindowMappingStore = (*openAICodexWindowRedisStore)(nil)
var _ service.OpenAICodexWindowMappingStore = (*gatewayCache)(nil)

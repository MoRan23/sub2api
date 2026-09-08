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

const OpenAICodexClientWindowKeyPrefix = "openai:codex:client-window:v1:"

func (s *openAICodexWindowRedisStore) ResolveOpenAICodexClientWindow(ctx context.Context, mappingKey string, transition service.OpenAICodexClientWindowTransition, ttl time.Duration) (service.OpenAICodexClientWindowResult, error) {
	if s == nil || s.rdb == nil {
		return service.OpenAICodexClientWindowResult{}, service.ErrOpenAICodexWindowStoreUnavailable
	}
	return resolveOpenAICodexClientWindow(ctx, s.rdb, mappingKey, transition, ttl)
}

func (c *gatewayCache) ResolveOpenAICodexClientWindow(ctx context.Context, mappingKey string, transition service.OpenAICodexClientWindowTransition, ttl time.Duration) (service.OpenAICodexClientWindowResult, error) {
	if c == nil || c.rdb == nil {
		return service.OpenAICodexClientWindowResult{}, service.ErrOpenAICodexWindowStoreUnavailable
	}
	return resolveOpenAICodexClientWindow(ctx, c.rdb, mappingKey, transition, ttl)
}

// WATCH covers both records so normal compact Lua commits and client rollover
// transactions participate in the same main-window CAS. The shared Go state
// machine keeps single-process and Redis deployments semantically identical.
func resolveOpenAICodexClientWindow(ctx context.Context, rdb *redis.Client, mappingKey string, transition service.OpenAICodexClientWindowTransition, ttl time.Duration) (service.OpenAICodexClientWindowResult, error) {
	if err := service.ValidateOpenAICodexClientWindowTransition(mappingKey, transition); err != nil {
		return service.OpenAICodexClientWindowResult{}, err
	}
	windowKey, err := OpenAICodexWindowRedisKey(mappingKey)
	if err != nil {
		return service.OpenAICodexClientWindowResult{}, err
	}
	clientKey := OpenAICodexClientWindowKeyPrefix + mappingKey
	ttl = time.Duration(normalizedOpenAICodexWindowTTLSeconds(ttl)) * time.Second
	var result service.OpenAICodexClientWindowResult
	for attempt := 0; attempt < 128; attempt++ {
		if err := ctx.Err(); err != nil {
			return service.OpenAICodexClientWindowResult{}, classifyOpenAICodexWindowRedisError("resolve openai Codex client window", err)
		}
		err = rdb.Watch(ctx, func(tx *redis.Tx) error {
			mainRaw, err := tx.Get(ctx, windowKey).Bytes()
			if errors.Is(err, redis.Nil) {
				return service.ErrOpenAICodexWindowStoredInvalid
			}
			if err != nil {
				return err
			}
			current, err := decodeStrictOpenAICodexWindowSnapshot(mainRaw)
			if err != nil {
				return service.ErrOpenAICodexWindowStoredInvalid
			}
			var binding *service.OpenAICodexClientWindowBinding
			bindingRaw, err := tx.Get(ctx, clientKey).Bytes()
			if err == nil {
				decoded, err := decodeStrictOpenAICodexClientWindowBinding(bindingRaw)
				if err != nil {
					return service.ErrOpenAICodexWindowStoredInvalid
				}
				binding = &decoded
			} else if !errors.Is(err, redis.Nil) {
				return err
			}
			result, binding, err = service.ApplyOpenAICodexClientWindowTransition(current, binding, transition)
			if err != nil {
				return err
			}
			bindingRaw, err = json.Marshal(binding)
			if err != nil {
				return err
			}
			if result.Status == service.OpenAICodexClientWindowAdvanced {
				mainRaw, err = json.Marshal(result.Snapshot)
				if err != nil {
					return err
				}
			}
			_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				if result.Status == service.OpenAICodexClientWindowAdvanced {
					pipe.Set(ctx, windowKey, mainRaw, ttl)
				} else {
					pipe.Expire(ctx, windowKey, ttl)
				}
				pipe.Set(ctx, clientKey, bindingRaw, ttl)
				return nil
			})
			return err
		}, windowKey, clientKey)
		if errors.Is(err, redis.TxFailedErr) {
			continue
		}
		if err != nil {
			return result, classifyOpenAICodexWindowRedisError("resolve openai Codex client window", err)
		}
		return result, nil
	}
	return service.OpenAICodexClientWindowResult{}, errors.New("openai Codex client window transaction contention limit reached")
}

func decodeStrictOpenAICodexClientWindowBinding(raw []byte) (service.OpenAICodexClientWindowBinding, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return service.OpenAICodexClientWindowBinding{}, err
	}
	if len(fields) != 2 || fields["client"] == nil || fields["server"] == nil {
		return service.OpenAICodexClientWindowBinding{}, service.ErrOpenAICodexWindowStoredInvalid
	}
	if _, err := decodeStrictOpenAICodexWindowSnapshot(fields["server"]); err != nil {
		return service.OpenAICodexClientWindowBinding{}, err
	}
	var clientFields map[string]json.RawMessage
	if err := json.Unmarshal(fields["client"], &clientFields); err != nil {
		return service.OpenAICodexClientWindowBinding{}, err
	}
	if len(clientFields) != 4 {
		return service.OpenAICodexClientWindowBinding{}, service.ErrOpenAICodexWindowStoredInvalid
	}
	for _, field := range []string{"number", "first_token", "current_token", "previous_token"} {
		if clientFields[field] == nil || string(clientFields[field]) == "null" {
			return service.OpenAICodexClientWindowBinding{}, service.ErrOpenAICodexWindowStoredInvalid
		}
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var binding service.OpenAICodexClientWindowBinding
	if err := decoder.Decode(&binding); err != nil {
		return binding, err
	}
	if err := ensureOpenAICodexWindowJSONEOF(decoder); err != nil {
		return binding, err
	}
	if err := service.ValidateOpenAICodexClientWindowBinding(binding); err != nil {
		return binding, err
	}
	return binding, nil
}

var _ service.OpenAICodexClientWindowStore = (*openAICodexWindowRedisStore)(nil)
var _ service.OpenAICodexClientWindowStore = (*gatewayCache)(nil)

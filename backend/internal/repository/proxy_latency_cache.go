package repository

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const proxyLatencyKeyPrefix = "proxy:latency:"

// GeoResultUnixMs orders observations, not the last successful location retained
// by a failed probe. Compare and write atomically: service-side read/merge cannot
// prevent an earlier probe finishing after a newer result has already committed.
var setProxyLatencyIfNewerScript = redis.NewScript(`
local incoming = cjson.decode(ARGV[1])
local raw = redis.call('GET', KEYS[1])
if raw then
    local valid, current = pcall(cjson.decode, raw)
    if valid and type(current) == 'table' then
        local route = incoming.route_key
        if type(route) == 'string' and route ~= '' and current.route_key == route then
            local current_time = tonumber(current.geo_result_unix_ms) or 0
            local incoming_time = tonumber(incoming.geo_result_unix_ms) or 0
            if current_time > incoming_time then
                return 0
            end
        end
    end
end
redis.call('SET', KEYS[1], ARGV[1])
return 1
`)

func proxyLatencyKey(proxyID int64) string {
	return fmt.Sprintf("%s%d", proxyLatencyKeyPrefix, proxyID)
}

type proxyLatencyCache struct {
	rdb *redis.Client
}

func NewProxyLatencyCache(rdb *redis.Client) service.ProxyLatencyCache {
	return &proxyLatencyCache{rdb: rdb}
}

func (c *proxyLatencyCache) GetProxyLatencies(ctx context.Context, proxyIDs []int64) (map[int64]*service.ProxyLatencyInfo, error) {
	results := make(map[int64]*service.ProxyLatencyInfo)
	if len(proxyIDs) == 0 {
		return results, nil
	}

	keys := make([]string, 0, len(proxyIDs))
	for _, id := range proxyIDs {
		keys = append(keys, proxyLatencyKey(id))
	}

	values, err := c.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return results, err
	}

	for i, raw := range values {
		if raw == nil {
			continue
		}
		var payload []byte
		switch v := raw.(type) {
		case string:
			payload = []byte(v)
		case []byte:
			payload = v
		default:
			continue
		}
		var info service.ProxyLatencyInfo
		if err := json.Unmarshal(payload, &info); err != nil {
			continue
		}
		results[proxyIDs[i]] = &info
	}

	return results, nil
}

func (c *proxyLatencyCache) SetProxyLatency(ctx context.Context, proxyID int64, info *service.ProxyLatencyInfo) error {
	if info == nil {
		return nil
	}
	payload, err := json.Marshal(info)
	if err != nil {
		return err
	}
	return setProxyLatencyIfNewerScript.Run(ctx, c.rdb, []string{proxyLatencyKey(proxyID)}, payload).Err()
}

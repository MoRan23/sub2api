package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/lib/pq"
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
	db  *sql.DB
}

func NewProxyLatencyCache(rdb *redis.Client, db *sql.DB) service.ProxyLatencyCache {
	return &proxyLatencyCache{rdb: rdb, db: db}
}

func (c *proxyLatencyCache) GetProxyLatencies(ctx context.Context, proxyIDs []int64) (map[int64]*service.ProxyLatencyInfo, error) {
	if c.db == nil {
		return c.getLegacyProxyLatencies(ctx, proxyIDs)
	}
	results, err := c.getPersistentProxyLatencies(ctx, proxyIDs)
	if err != nil || len(proxyIDs) == 0 {
		return results, err
	}
	missing := make([]int64, 0, len(proxyIDs))
	for _, id := range proxyIDs {
		if results[id] == nil {
			missing = append(missing, id)
		}
	}
	if len(missing) == 0 || c.rdb == nil {
		return results, nil
	}
	// A rolling upgrade adopts previously saved Redis observations once. Every
	// import verifies the current proxy identity under its database row lock;
	// Redis is never a fallback when the authoritative database is unavailable.
	legacy, err := c.getLegacyProxyLatencies(ctx, missing)
	if err != nil {
		return results, nil
	}
	for id, info := range legacy {
		if err := c.SetProxyLatency(ctx, id, info); err != nil {
			return results, err
		}
	}
	if len(legacy) == 0 {
		return results, nil
	}
	// A competing writer may have won while the legacy snapshot was imported.
	// Read its committed value instead of returning the candidate from Redis.
	imported, err := c.getPersistentProxyLatencies(ctx, missing)
	for id, info := range imported {
		results[id] = info
	}
	return results, err
}

func (c *proxyLatencyCache) getPersistentProxyLatencies(ctx context.Context, proxyIDs []int64) (map[int64]*service.ProxyLatencyInfo, error) {
	results := make(map[int64]*service.ProxyLatencyInfo)
	if len(proxyIDs) == 0 {
		return results, nil
	}
	rows, err := c.db.QueryContext(ctx, `
		SELECT p.id, p.protocol, p.host, p.port, COALESCE(p.username, ''), COALESCE(p.password, ''), s.payload
		FROM proxy_geo_snapshots s JOIN proxies p ON p.id = s.proxy_id
		WHERE p.id = ANY($1) AND p.deleted_at IS NULL`, pq.Array(proxyIDs))
	if err != nil {
		return results, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var proxy service.Proxy
		var payload []byte
		if err := rows.Scan(&proxy.ID, &proxy.Protocol, &proxy.Host, &proxy.Port, &proxy.Username, &proxy.Password, &payload); err != nil {
			return results, err
		}
		var info service.ProxyLatencyInfo
		if err := json.Unmarshal(payload, &info); err != nil {
			return results, fmt.Errorf("decode proxy geo snapshot: %w", err)
		}
		if info.RouteKey == service.ProxyGeoRouteKey(&proxy) {
			results[proxy.ID] = &info
		}
	}
	return results, rows.Err()
}

func (c *proxyLatencyCache) getLegacyProxyLatencies(ctx context.Context, proxyIDs []int64) (map[int64]*service.ProxyLatencyInfo, error) {
	results := make(map[int64]*service.ProxyLatencyInfo)
	if len(proxyIDs) == 0 || c.rdb == nil {
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
	if c.db != nil {
		return c.setPersistentProxyLatency(ctx, proxyID, info)
	}
	payload, err := json.Marshal(info)
	if err != nil {
		return err
	}
	return setProxyLatencyIfNewerScript.Run(ctx, c.rdb, []string{proxyLatencyKey(proxyID)}, payload).Err()
}

func (c *proxyLatencyCache) setPersistentProxyLatency(ctx context.Context, proxyID int64, info *service.ProxyLatencyInfo) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var proxy service.Proxy
	err = tx.QueryRowContext(ctx, `
		SELECT id, protocol, host, port, COALESCE(username, ''), COALESCE(password, '')
		FROM proxies WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`, proxyID).
		Scan(&proxy.ID, &proxy.Protocol, &proxy.Host, &proxy.Port, &proxy.Username, &proxy.Password)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if info.RouteKey == "" || info.RouteKey != service.ProxyGeoRouteKey(&proxy) {
		return nil
	}
	var payload []byte
	var existing *service.ProxyLatencyInfo
	err = tx.QueryRowContext(ctx, `SELECT payload FROM proxy_geo_snapshots WHERE proxy_id = $1 FOR UPDATE`, proxyID).Scan(&payload)
	if err == nil {
		existing = new(service.ProxyLatencyInfo)
		if err := json.Unmarshal(payload, existing); err != nil {
			return fmt.Errorf("decode proxy geo snapshot: %w", err)
		}
		if existing.RouteKey == info.RouteKey && existing.GeoResultUnixMs > info.GeoResultUnixMs {
			return nil
		}
		if existing.RouteKey != info.RouteKey {
			existing = nil
		}
	} else if err != sql.ErrNoRows {
		return err
	}
	// Merge while holding the proxy lock, so failures from another instance
	// retain the last successful location without a read/modify/write race.
	merged := service.MergeProxyLatencySnapshot(info, existing, time.Now())
	payload, err = json.Marshal(merged)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO proxy_geo_snapshots (proxy_id, route_key, geo_result_unix_ms, payload, updated_at)
		VALUES ($1, $2, $3, $4::jsonb, NOW())
		ON CONFLICT (proxy_id) DO UPDATE SET route_key = EXCLUDED.route_key,
			geo_result_unix_ms = EXCLUDED.geo_result_unix_ms, payload = EXCLUDED.payload, updated_at = NOW()`,
		proxyID, merged.RouteKey, merged.GeoResultUnixMs, string(payload))
	if err != nil {
		return err
	}
	return tx.Commit()
}

var releaseProxyProbeScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
    return redis.call('DEL', KEYS[1])
end
return 0
`)

// AcquireProxyProbe serializes automatic probes across instances. The lease is
// scoped to a proxy ID, including while its configured route changes.
func (c *proxyLatencyCache) AcquireProxyProbe(ctx context.Context, proxyID int64, routeKey string, ttl time.Duration) (func(), bool, error) {
	if c.rdb == nil {
		return nil, false, fmt.Errorf("proxy probe lease storage unavailable")
	}
	if proxyID <= 0 || routeKey == "" || ttl <= 0 {
		return nil, false, fmt.Errorf("invalid proxy probe lease")
	}
	key := fmt.Sprintf("proxy:geo:probe:%d", proxyID)
	owner := routeKey + ":" + uuid.NewString()
	acquired, err := c.rdb.SetNX(ctx, key, owner, ttl).Result()
	if err != nil || !acquired {
		return nil, acquired, err
	}
	return func() {
		// Probe contexts are normally canceled when work ends. Release must still
		// succeed, but may never delete a lease held by a subsequent worker.
		releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = releaseProxyProbeScript.Run(releaseCtx, c.rdb, []string{key}, owner).Err()
	}, true, nil
}

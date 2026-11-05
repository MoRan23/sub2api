//go:build integration

package repository

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// These counters observe the database/sql driver boundary. Returned bytes count
// string/[]byte values (including JSON), not protocol framing or scalar values.
// This deliberately uses the same workload before and after the incremental API.
type telemetryBenchCounters struct{ queries, rows, bytes atomic.Int64 }
type telemetryBenchConnector struct {
	driver.Connector
	counts *telemetryBenchCounters
}
type telemetryBenchConn struct {
	driver.Conn
	counts *telemetryBenchCounters
}
type telemetryBenchRows struct {
	driver.Rows
	counts *telemetryBenchCounters
}

func (c telemetryBenchConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return telemetryBenchConn{conn, c.counts}, nil
}
func (c telemetryBenchConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}
func (c telemetryBenchConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.counts.queries.Add(1)
	rows, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
	if err != nil {
		return nil, err
	}
	return telemetryBenchRows{rows, c.counts}, nil
}
func (c telemetryBenchConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.counts.queries.Add(1)
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}
func (r telemetryBenchRows) Next(values []driver.Value) error {
	if err := r.Rows.Next(values); err != nil {
		return err
	}
	r.counts.rows.Add(1)
	for _, value := range values {
		switch v := value.(type) {
		case string:
			r.counts.bytes.Add(int64(len(v)))
		case []byte:
			r.counts.bytes.Add(int64(len(v)))
		}
	}
	return nil
}

// BenchmarkCodexTelemetryPostgresHistory isolates one small runtime mutation
// against permanent marker history. 32 transient rows and due dates are fixed;
// setup, EXPLAIN and fixture deletion are outside the measured interval.
func BenchmarkCodexTelemetryPostgresHistory(b *testing.B) {
	for _, markers := range []int{1000, 10000, 100000} {
		b.Run(fmt.Sprintf("markers_%d", markers), func(b *testing.B) {
			_, key, now := telemetryStoreFixture(b)
			ctx := context.Background()
			connector, err := pq.NewConnector(integrationDSN)
			require.NoError(b, err)
			counts := &telemetryBenchCounters{}
			db := sql.OpenDB(telemetryBenchConnector{connector, counts})
			b.Cleanup(func() { _ = db.Close() })
			store := NewCodexTelemetryStore(db)
			pool, err := store.TransactPool(ctx, key, now, func(*service.CodexTelemetryPoolTransaction) error { return nil })
			require.NoError(b, err)
			_, err = integrationDB.ExecContext(ctx, `INSERT INTO codex_telemetry_activities(pool_id,kind,activity_key,state,updated_at)
				SELECT $1::uuid,CASE WHEN n%2=0 THEN 'thread' ELSE 'metrics' END,'history-'||n,
				'{"initialized":true,"samples":1}'::jsonb,$2 FROM generate_series(1,$3) n`, pool.ID, now, markers)
			require.NoError(b, err)
			_, err = integrationDB.ExecContext(ctx, `INSERT INTO codex_telemetry_activities(pool_id,kind,activity_key,state,due_at,updated_at)
				SELECT $1::uuid,'turn','runtime-'||n,'{"sealed":false,"incomplete":false,"inflight":{},"attempts":0}'::jsonb,$2,$3
				FROM generate_series(1,32) n`, pool.ID, now.Add(time.Minute), now)
			require.NoError(b, err)
			_, err = integrationDB.ExecContext(ctx, `ANALYZE codex_telemetry_activities`)
			require.NoError(b, err)
			telemetryBenchExplain(b, pool.ID, now)
			counts.queries.Store(0)
			counts.rows.Store(0)
			counts.bytes.Store(0)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, err := store.TransactPool(ctx, key, now.Add(time.Duration(i+1)*time.Microsecond), func(tx *service.CodexTelemetryPoolTransaction) error {
					activity, _, err := tx.Activities.Get("turn", "runtime-1")
					if err != nil {
						return err
					}
					activity.Data = json.RawMessage(fmt.Sprintf(`{"sealed":false,"incomplete":false,"inflight":{},"attempts":%d}`, i+1))
					return tx.Activities.Put(activity)
				})
				if err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(counts.queries.Load())/float64(b.N), "queries/op")
			b.ReportMetric(float64(counts.rows.Load())/float64(b.N), "rows/op")
			b.ReportMetric(float64(counts.bytes.Load())/float64(b.N), "returned-B/op")
		})
	}
}

func telemetryBenchExplain(b *testing.B, poolID string, now time.Time) {
	b.Helper()
	queries := []struct {
		name, sql string
		args      []any
	}{
		{"all-history", `SELECT kind,activity_key,state,due_at,updated_at FROM codex_telemetry_activities WHERE pool_id=$1::uuid`, []any{poolID}},
		{"point", `SELECT kind,activity_key,state,due_at,updated_at FROM codex_telemetry_activities WHERE pool_id=$1::uuid AND kind='turn' AND activity_key='runtime-1'`, []any{poolID}},
		{"due", `SELECT kind,activity_key,state,due_at,updated_at FROM codex_telemetry_activities WHERE pool_id=$1::uuid AND due_at <= $2 ORDER BY due_at,kind COLLATE "C",activity_key COLLATE "C" LIMIT 128`, []any{poolID, now.Add(time.Minute)}},
	}
	for _, query := range queries {
		rows, err := integrationDB.QueryContext(context.Background(), "EXPLAIN (ANALYZE, BUFFERS, FORMAT TEXT) "+query.sql, query.args...)
		require.NoError(b, err)
		for rows.Next() {
			var line string
			require.NoError(b, rows.Scan(&line))
			b.Logf("EXPLAIN %s: %s", query.name, line)
		}
		require.NoError(b, rows.Err())
		require.NoError(b, rows.Close())
	}
}

type telemetryRuntimeMeasurement struct {
	queries, rows, bytes int64
	err                  error
}

// Only producer transactions use the counted connection. Dispatcher/ticker
// reads are inert so timing cannot add unrelated SQL to Begin/Finish counts.
type telemetryMeasuredRuntimeStore struct {
	service.CodexTelemetryStore
	producer  service.CodexTelemetryStore
	counts    *telemetryBenchCounters
	completed chan telemetryRuntimeMeasurement
}

func (s *telemetryMeasuredRuntimeStore) TransactPool(ctx context.Context, key service.CodexTelemetryPoolKey, now time.Time, fn func(*service.CodexTelemetryPoolTransaction) error) (*service.CodexTelemetryPool, error) {
	before := telemetryRuntimeMeasurement{queries: s.counts.queries.Load(), rows: s.counts.rows.Load(), bytes: s.counts.bytes.Load()}
	pool, err := s.producer.TransactPool(ctx, key, now, fn)
	s.completed <- telemetryRuntimeMeasurement{queries: s.counts.queries.Load() - before.queries,
		rows: s.counts.rows.Load() - before.rows, bytes: s.counts.bytes.Load() - before.bytes, err: err}
	return pool, err
}
func (*telemetryMeasuredRuntimeStore) ListDuePools(context.Context, time.Time, int) ([]service.CodexTelemetryPoolKey, error) {
	return nil, nil
}
func (*telemetryMeasuredRuntimeStore) ClaimBatches(context.Context, time.Time, int) ([]service.CodexTelemetryBatch, error) {
	return nil, nil
}

// Exercise the public asynchronous producer API, including retention and
// occupancy, with fixed transient state. Assertions concern data movement and
// query counts only; machine timing is deliberately excluded.
func TestCodexTelemetryPostgresRuntimeHistoryCost(t *testing.T) {
	t.Setenv("CODEX_TELEMETRY_ENABLED", "true")
	var baseline telemetryRuntimeMeasurement
	for _, markers := range []int{1000, 100000} {
		t.Run(fmt.Sprintf("markers_%d", markers), func(t *testing.T) {
			plain, key, now := telemetryStoreFixture(t)
			ctx := context.Background()
			pool, err := plain.TransactPool(ctx, key, now, func(*service.CodexTelemetryPoolTransaction) error { return nil })
			require.NoError(t, err)
			_, err = integrationDB.ExecContext(ctx, `INSERT INTO codex_telemetry_activities(pool_id,kind,activity_key,state,updated_at)
				SELECT $1::uuid,CASE WHEN n%2=0 THEN 'thread' ELSE 'metrics' END,'history-'||n,
				'{"initialized":true,"samples":1}'::jsonb,$2 FROM generate_series(1,$3) n`, pool.ID, now, markers)
			require.NoError(t, err)
			_, err = integrationDB.ExecContext(ctx, `INSERT INTO codex_telemetry_activities(pool_id,kind,activity_key,state,updated_at)
				SELECT $1::uuid,'sampling','fixed-'||n,'{"completed":true}'::jsonb,$2 FROM generate_series(1,32) n`, pool.ID, now)
			require.NoError(t, err)
			connector, err := pq.NewConnector(integrationDSN)
			require.NoError(t, err)
			counts := &telemetryBenchCounters{}
			db := sql.OpenDB(telemetryBenchConnector{connector, counts})
			defer db.Close()
			measured := &telemetryMeasuredRuntimeStore{CodexTelemetryStore: plain, producer: NewCodexTelemetryStore(db), counts: counts, completed: make(chan telemetryRuntimeMeasurement, 8)}
			runtime := service.NewCodexTelemetryService(nil)
			defer runtime.Stop()
			require.NoError(t, runtime.SetStore(measured))
			attempt := runtime.Begin(ctx, service.CodexTelemetryInput{AccountID: key.OwnerAccountID, OwnerAccountID: key.OwnerAccountID,
				OSFamily: key.OSFamily, InstallationID: key.InstallationID, AccessToken: "local-test-token", ChatGPTAccountID: "local-test-workspace",
				UserAgent: "codex_cli_rs/0.155.1 (Windows 10.0.26200; x86_64)", Originator: "codex_cli_rs", Version: "0.155.1",
				SessionID: "session-fixed", ThreadID: "thread-fixed", TurnID: "turn-fixed", SamplingID: "sampling-fixed", Model: "gpt-6-astra", StartedAt: now})
			require.NotNil(t, attempt)
			attempt.Finish(service.CodexTelemetryResult{Status: "completed", HTTPStatus: 200, InputTokens: 7, OutputTokens: 3, FinishedAt: now.Add(time.Second)})
			var total telemetryRuntimeMeasurement
			for range 2 {
				select {
				case sample := <-measured.completed:
					require.NoError(t, sample.err)
					total.queries += sample.queries
					total.rows += sample.rows
					total.bytes += sample.bytes
				case <-time.After(10 * time.Second):
					t.Fatal("producer did not complete Begin and Finish")
				}
			}
			runtime.Stop()
			var finished int
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM codex_telemetry_activities WHERE pool_id=$1::uuid AND kind='attempt' AND state->'finished'='true'::jsonb`, pool.ID).Scan(&finished))
			require.Equal(t, 1, finished)
			t.Logf("markers=%d Begin+Finish queries=%d rows=%d returned-B=%d", markers, total.queries, total.rows, total.bytes)
			if markers == 1000 {
				baseline = total
			} else {
				require.InDelta(t, baseline.queries, total.queries, 4, "permanent history must not add SQL calls")
				require.InDelta(t, baseline.rows, total.rows, 4, "permanent history must not add returned rows")
				require.LessOrEqual(t, total.bytes, baseline.bytes+4096, "permanent history must not be returned to the producer")
			}
		})
	}
}

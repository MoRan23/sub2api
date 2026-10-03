package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

type attributionRepository struct{ db *sql.DB }

func NewAttributionRepository(db *sql.DB) service.AttributionRepository {
	return &attributionRepository{db: db}
}

func (r *attributionRepository) transaction(ctx context.Context) (*sql.Tx, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	// Serializes capacity allocation, config changes and result application across
	// replicas. No upstream or ModelTrace calls occur inside this transaction.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(263,1)`); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
}

type attributionQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func attributionConfig(ctx context.Context, q attributionQuery) (service.AttributionConfig, error) {
	c := service.DefaultAttributionConfig()
	var b []byte
	var version int64
	if err := q.QueryRowContext(ctx, `SELECT version,config FROM model_attribution_config WHERE id=1`).Scan(&version, &b); err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	c.Version = version
	return c, nil
}
func (r *attributionRepository) Config(ctx context.Context) (service.AttributionConfig, error) {
	return attributionConfig(ctx, r.db)
}
func (r *attributionRepository) SaveConfig(ctx context.Context, c service.AttributionConfig) (service.AttributionConfig, error) {
	var err error
	c, err = service.NormalizeAttributionConfig(c)
	if err != nil {
		return c, err
	}
	tx, err := r.transaction(ctx)
	if err != nil {
		return c, err
	}
	defer func() { _ = tx.Rollback() }()
	previous, err := attributionConfig(ctx, tx)
	if err != nil {
		return c, err
	}
	for _, g := range c.Groups {
		var exists bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM groups WHERE id=$1 AND deleted_at IS NULL)`, g.GroupID).Scan(&exists); err != nil {
			return c, err
		}
		if !exists {
			return c, service.ErrAttributionInvalid
		}
	}
	b, err := json.Marshal(c)
	if err != nil {
		return c, err
	}
	err = tx.QueryRowContext(ctx, `UPDATE model_attribution_config SET version=version+1,config=$1,updated_at=NOW() WHERE id=1 AND version=$2 RETURNING version`, b, c.Version).Scan(&c.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return c, service.ErrAttributionConflict
	}
	if err != nil {
		return c, err
	}
	if c.Enabled && !previous.Enabled {
		if _, err = tx.ExecContext(ctx, `UPDATE model_attribution_state SET next_due_at=NOW()`); err != nil {
			return c, err
		}
	}
	return c, tx.Commit()
}

// Load and lock the authoritative routing row and authorization metadata. The
// digest excludes rotating OAuth tokens and includes explicit authorization generation.
func attributionSnapshot(ctx context.Context, tx *sql.Tx, id int64, c service.AttributionConfig) (*service.Account, service.AttributionSnapshot, string, error) {
	a := &service.Account{}
	var credentials, extra []byte
	var deleted *time.Time
	err := tx.QueryRowContext(ctx, `SELECT id,name,platform,type,status,schedulable,expires_at,parent_account_id,proxy_id,credentials,extra,deleted_at FROM accounts WHERE id=$1 FOR UPDATE`, id).Scan(&a.ID, &a.Name, &a.Platform, &a.Type, &a.Status, &a.Schedulable, &a.ExpiresAt, &a.ParentAccountID, &a.ProxyID, &credentials, &extra, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.AttributionSnapshot{}, "account_missing", nil
	}
	if err != nil {
		return nil, service.AttributionSnapshot{}, "", err
	}
	if err = json.Unmarshal(credentials, &a.Credentials); err != nil {
		return nil, service.AttributionSnapshot{}, "", err
	}
	if len(extra) > 0 {
		if err = json.Unmarshal(extra, &a.Extra); err != nil {
			return nil, service.AttributionSnapshot{}, "", err
		}
	}
	var generation, status string
	err = tx.QueryRowContext(ctx, `SELECT authorization_generation::text,status FROM account_openai_oauth_credentials WHERE account_id=$1 FOR UPDATE`, id).Scan(&generation, &status)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, service.AttributionSnapshot{}, "", err
	}
	rows, err := tx.QueryContext(ctx, `SELECT ag.group_id,ag.priority,g.updated_at FROM account_groups ag JOIN groups g ON g.id=ag.group_id WHERE ag.account_id=$1 AND g.deleted_at IS NULL ORDER BY ag.priority,ag.group_id FOR SHARE OF ag,g`, id)
	if err != nil {
		return nil, service.AttributionSnapshot{}, "", err
	}
	var groupVersions []time.Time
	for rows.Next() {
		var g service.AccountGroup
		var updated time.Time
		if err = rows.Scan(&g.GroupID, &g.Priority, &updated); err != nil {
			_ = rows.Close()
			return nil, service.AttributionSnapshot{}, "", err
		}
		a.AccountGroups = append(a.AccountGroups, g)
		a.GroupIDs = append(a.GroupIDs, g.GroupID)
		groupVersions = append(groupVersions, updated)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, service.AttributionSnapshot{}, "", err
	}
	identity := map[string]any{"generation": generation, "status": status, "id": id, "platform": a.Platform, "type": a.Type}
	for _, key := range []string{"chatgpt_account_id", "chatgpt_user_id", "organization_id", "auth_mode", "openai_auth_mode"} {
		identity[key] = a.Credentials[key]
	}
	if a.IsOpenAIPersonalAccessToken() || a.IsOpenAIAgentIdentity() || generation == "" {
		for _, key := range []string{"access_token", "agent_private_key", "agent_runtime_id"} {
			identity[key] = a.Credentials[key]
		}
	}
	s := service.AttributionSnapshot{ConfigVersion: c.Version}
	s.Policy, s.GroupID = service.ResolveAttributionPolicy(c, a.AccountGroups)
	s.Authorization = service.AttributionDigest(identity)
	s.Fence = service.AttributionDigest([]any{s.Authorization, a.Credentials["model_mapping"], a.ProxyID, a.AccountGroups, groupVersions, a.Status, a.Schedulable, a.ExpiresAt, a.ParentAccountID, a.IsOpenAIPassthroughEnabled()})
	reason := service.AttributionSkipReason(a, time.Now())
	if deleted != nil {
		reason = "account_missing"
	}
	if !c.Enabled {
		reason = "disabled"
	}
	return a, s, reason, nil
}

const attributionColumns = `id,account_id,account_name,source,status,reason,snapshot,authorization_digest,fence_digest,result,COALESCE(claim_id::text,''),created_at,started_at,finished_at`

func scanAttribution(row scannable) (*service.AttributionJob, error) {
	j := &service.AttributionJob{}
	var snap, result []byte
	var auth, fence string
	if err := row.Scan(&j.ID, &j.AccountID, &j.AccountName, &j.Source, &j.Status, &j.Reason, &snap, &auth, &fence, &result, &j.Claim, &j.CreatedAt, &j.StartedAt, &j.FinishedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(snap, &j.Snapshot); err != nil {
		return nil, err
	}
	j.Snapshot.Authorization = auth
	j.Snapshot.Fence = fence
	if err := json.Unmarshal(result, &j.Result); err != nil {
		return nil, err
	}
	return j, nil
}
func attributionRows(rows *sql.Rows) ([]*service.AttributionJob, error) {
	out := []*service.AttributionJob{}
	for rows.Next() {
		j, err := scanAttribution(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func pruneAttribution(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM model_attribution_jobs WHERE id IN (SELECT id FROM (SELECT id,row_number() OVER(PARTITION BY account_id ORDER BY id DESC) AS rn FROM model_attribution_jobs WHERE finished_at IS NOT NULL) history WHERE rn>100)`)
	return err
}

func (r *attributionRepository) Enqueue(ctx context.Context, ids []int64, manual bool) ([]*service.AttributionJob, error) {
	tx, err := r.transaction(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	c, err := attributionConfig(ctx, tx)
	if err != nil {
		return nil, err
	}
	if !c.Enabled {
		return nil, service.ErrAttributionDisabled
	}
	if !manual {
		rows, e := tx.QueryContext(ctx, `SELECT a.id FROM accounts a LEFT JOIN model_attribution_state s ON s.account_id=a.id WHERE a.deleted_at IS NULL AND a.platform='openai' AND a.type='oauth' AND a.parent_account_id IS NULL AND a.status='active' AND a.schedulable AND (a.expires_at IS NULL OR a.expires_at>NOW()) AND COALESCE(a.extra->'openai_passthrough',a.extra->'openai_oauth_passthrough','false'::jsonb) <> 'true'::jsonb AND (s.next_due_at IS NULL OR s.next_due_at<=NOW()) AND NOT EXISTS(SELECT 1 FROM account_initial_tests initial WHERE initial.account_id=a.id AND initial.processed_at IS NULL) AND NOT EXISTS(SELECT 1 FROM model_attribution_jobs j WHERE j.account_id=a.id AND j.status IN ('queued','running')) ORDER BY s.next_due_at NULLS FIRST,a.id LIMIT 500`)
		if e != nil {
			return nil, e
		}
		ids = nil
		for rows.Next() {
			var id int64
			if e = rows.Scan(&id); e != nil {
				_ = rows.Close()
				return nil, e
			}
			ids = append(ids, id)
		}
		e = rows.Err()
		_ = rows.Close()
		if e != nil {
			return nil, e
		}
	}
	out := []*service.AttributionJob{}
	for _, id := range uniquePositiveInt64s(ids) {
		source := "scheduled"
		if manual {
			source = "manual"
		}
		j, e := enqueueAttribution(ctx, tx, id, c, source, "")
		if e != nil {
			return nil, e
		}
		out = append(out, j)
	}
	if len(out) > 0 {
		if err = pruneAttribution(ctx, tx); err != nil {
			return nil, err
		}
	}
	return out, tx.Commit()
}

func enqueueAttribution(ctx context.Context, tx *sql.Tx, id int64, c service.AttributionConfig, source, model string) (*service.AttributionJob, error) {
	existing, err := scanAttribution(tx.QueryRowContext(ctx, `SELECT `+attributionColumns+` FROM model_attribution_jobs WHERE account_id=$1 AND status IN ('queued','running')`, id))
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	a, s, skip, err := attributionSnapshot(ctx, tx, id, c)
	if err != nil {
		return nil, err
	}
	if a == nil {
		return nil, service.ErrAttributionNotFound
	}
	if model != "" {
		s.Policy.Model = model
	}
	state := "queued"
	if skip != "" {
		state = "skipped"
	}
	b, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	j, err := scanAttribution(tx.QueryRowContext(ctx, `INSERT INTO model_attribution_jobs(account_id,account_name,source,status,reason,snapshot,authorization_digest,fence_digest,finished_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,CASE WHEN $4::text='skipped' THEN NOW() ELSE NULL END) RETURNING `+attributionColumns, id, a.Name, source, state, skip, b, s.Authorization, s.Fence))
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO model_attribution_state(account_id,next_due_at) VALUES($1,NOW()+interval '10 minutes') ON CONFLICT(account_id) DO UPDATE SET next_due_at=EXCLUDED.next_due_at`, id)
	return j, err
}

func (r *attributionRepository) Claim(ctx context.Context) (*service.AttributionJob, error) {
	tx, err := r.transaction(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	expired, err := tx.ExecContext(ctx, `UPDATE model_attribution_jobs SET status='failed',reason=CASE WHEN started_at<=NOW()-interval '10 minutes' THEN 'timeout' ELSE 'interrupted' END,finished_at=NOW(),lease_until=NULL WHERE status='running' AND (lease_until<NOW() OR started_at<NOW()-interval '10 minutes')`)
	if err != nil {
		return nil, err
	}
	c, err := attributionConfig(ctx, tx)
	if err != nil {
		return nil, err
	}
	var disabledCount int64
	if !c.Enabled {
		disabled, e := tx.ExecContext(ctx, `UPDATE model_attribution_jobs SET status='skipped',reason='disabled',finished_at=NOW() WHERE status='queued'`)
		if e != nil {
			return nil, e
		}
		if disabledCount, err = disabled.RowsAffected(); err != nil {
			return nil, err
		}
	}
	n, err := expired.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n > 0 || disabledCount > 0 {
		if err = pruneAttribution(ctx, tx); err != nil {
			return nil, err
		}
	}
	if !c.Enabled {
		return nil, tx.Commit()
	}
	j, err := scanAttribution(tx.QueryRowContext(ctx, `UPDATE model_attribution_jobs SET status='running',started_at=NOW(),claim_id=$1,lease_until=NOW()+interval '45 seconds' WHERE id=(SELECT id FROM model_attribution_jobs WHERE status='queued' ORDER BY id LIMIT 1) RETURNING `+attributionColumns, uuid.NewString()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	return j, tx.Commit()
}
func (r *attributionRepository) Heartbeat(ctx context.Context, j *service.AttributionJob) (bool, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE model_attribution_jobs SET lease_until=LEAST(NOW()+interval '45 seconds',started_at+interval '10 minutes') WHERE id=$1 AND claim_id=$2 AND status='running' AND lease_until>NOW() AND started_at>NOW()-interval '10 minutes'`, j.ID, j.Claim)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}
func validAttributionSnapshot(old, current service.AttributionSnapshot, skip string) bool {
	return skip == "" && old.ConfigVersion == current.ConfigVersion && old.Fence == current.Fence
}
func (r *attributionRepository) Validate(ctx context.Context, j *service.AttributionJob) (bool, error) {
	tx, err := r.transaction(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	c, err := attributionConfig(ctx, tx)
	if err != nil {
		return false, err
	}
	_, s, skip, err := attributionSnapshot(ctx, tx, j.AccountID, c)
	if err != nil {
		return false, err
	}
	return validAttributionSnapshot(j.Snapshot, s, skip), nil
}

func (r *attributionRepository) Finish(ctx context.Context, j *service.AttributionJob) error {
	tx, err := r.transaction(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var owned bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM model_attribution_jobs WHERE id=$1 AND claim_id=$2 AND status='running' AND lease_until>NOW())`, j.ID, j.Claim).Scan(&owned); err != nil {
		return err
	}
	if !owned {
		return nil
	}
	if j.Status == "passed" || j.Status == "mismatch" {
		c, e := attributionConfig(ctx, tx)
		if e != nil {
			return e
		}
		a, s, skip, e := attributionSnapshot(ctx, tx, j.AccountID, c)
		if e != nil {
			return e
		}
		if !validAttributionSnapshot(j.Snapshot, s, skip) {
			j.Result.Action = "stale"
			j.Reason = "configuration_or_authorization_changed"
		} else {
			var version int64
			var auth, verdict, policy string
			e = tx.QueryRowContext(ctx, `SELECT config_version,authorization_digest,verdict,policy_digest FROM model_attribution_state WHERE account_id=$1`, j.AccountID).Scan(&version, &auth, &verdict, &policy)
			if e != nil {
				return e
			}
			j.Result.Action = "unchanged"
			// Initial tests may use a different probe from the periodic group policy.
			// Keep that baseline distinct so the next periodic result synchronizes it.
			currentPolicy := service.AttributionDigest([]any{j.Snapshot.GroupID, j.Snapshot.Policy})
			if j.Status == "mismatch" || verdict != "passed" || version != s.ConfigVersion || auth != s.Authorization || policy != currentPolicy {
				models := s.Policy.HighModels
				action := "high"
				if j.Status == "mismatch" {
					models = s.Policy.LowModels
					action = "low"
				}
				current, _ := a.Credentials["model_mapping"].(map[string]any)
				next := service.AttributionModelMapping(current, models)
				if !reflect.DeepEqual(current, next) {
					b, e := json.Marshal(next)
					if e != nil {
						return e
					}
					if _, e = tx.ExecContext(ctx, `UPDATE accounts SET credentials=jsonb_set(COALESCE(credentials,'{}'::jsonb),'{model_mapping}',$2::jsonb),updated_at=NOW() WHERE id=$1`, a.ID, b); e != nil {
						return e
					}
					if e = enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &a.ID, nil, buildSchedulerGroupPayload(a.GroupIDs)); e != nil {
						return e
					}
					j.Result.Action = action
					j.Result.Before = current
					j.Result.After = next
				}
			}
			if _, e = tx.ExecContext(ctx, `UPDATE model_attribution_state SET config_version=$2,authorization_digest=$3,verdict=$4,policy_digest=$5 WHERE account_id=$1`, j.AccountID, s.ConfigVersion, s.Authorization, j.Status, currentPolicy); e != nil {
				return e
			}
		}
	}
	if j.Result.Action == "" {
		j.Result.Action = "none"
	}
	b, err := json.Marshal(j.Result)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE model_attribution_jobs SET status=$3,reason=$4,result=$5,finished_at=NOW(),lease_until=NULL WHERE id=$1 AND claim_id=$2 AND status='running'`, j.ID, j.Claim, j.Status, j.Reason, b); err != nil {
		return err
	}
	if err = pruneAttribution(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}
func (r *attributionRepository) Get(ctx context.Context, id int64) (*service.AttributionJob, error) {
	j, err := scanAttribution(r.db.QueryRowContext(ctx, `SELECT `+attributionColumns+` FROM model_attribution_jobs WHERE id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrAttributionNotFound
	}
	return j, err
}
func (r *attributionRepository) List(ctx context.Context, accountID int64, page, size int) (*service.AttributionPage, error) {
	p := &service.AttributionPage{Page: page, PageSize: size}
	if err := r.db.QueryRowContext(ctx, `SELECT count(*),count(*) FILTER(WHERE status='queued'),count(*) FILTER(WHERE status='running') FROM model_attribution_jobs WHERE ($1::bigint=0 OR account_id=$1)`, accountID).Scan(&p.Total, &p.Queued, &p.Running); err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT `+attributionColumns+` FROM model_attribution_jobs WHERE ($1::bigint=0 OR account_id=$1) ORDER BY id DESC LIMIT $2 OFFSET $3`, accountID, size, (page-1)*size)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	p.Items, err = attributionRows(rows)
	return p, err
}
func (r *attributionRepository) Summaries(ctx context.Context, ids []int64) (map[int64]*service.AttributionSummary, error) {
	out := map[int64]*service.AttributionSummary{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.db.QueryContext(ctx, `SELECT `+attributionColumns+` FROM (SELECT DISTINCT ON(account_id,(finished_at IS NULL)) * FROM model_attribution_jobs WHERE account_id=ANY($1) ORDER BY account_id,(finished_at IS NULL),id DESC) latest`, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items, err := attributionRows(rows)
	if err != nil {
		return nil, err
	}
	for _, j := range items {
		if out[j.AccountID] == nil {
			out[j.AccountID] = &service.AttributionSummary{}
		}
		j.Result.Before = nil
		j.Result.After = nil
		if j.Result.Analysis != nil {
			j.Result.Analysis.Results = nil
			j.Result.Analysis.Diagnostics = nil
		}
		j.Result.Usage = nil
		j.Result.ActualModels = nil
		j.Result.UpstreamModels = nil
		j.Snapshot.Policy.HighModels = nil
		j.Snapshot.Policy.LowModels = nil
		if j.FinishedAt == nil {
			out[j.AccountID].Active = j
		} else {
			out[j.AccountID].Latest = j
		}
	}
	return out, nil
}

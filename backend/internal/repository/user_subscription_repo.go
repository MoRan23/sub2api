package repository

import (
	"context"
	"database/sql"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/group"
	"github.com/Wei-Shaw/sub2api/ent/usersubscription"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

type userSubscriptionRepository struct {
	client *dbent.Client
}

func NewUserSubscriptionRepository(client *dbent.Client) service.UserSubscriptionRepository {
	return &userSubscriptionRepository{client: client}
}

func (r *userSubscriptionRepository) Create(ctx context.Context, sub *service.UserSubscription) error {
	if sub == nil {
		return service.ErrSubscriptionNilInput
	}

	client := clientFromContext(ctx, r.client)
	builder := client.UserSubscription.Create().
		SetUserID(sub.UserID).
		SetGroupID(sub.GroupID).
		SetExpiresAt(sub.ExpiresAt).
		SetNillableDailyWindowStart(sub.DailyWindowStart).
		SetNillableWeeklyWindowStart(sub.WeeklyWindowStart).
		SetNillableMonthlyWindowStart(sub.MonthlyWindowStart).
		SetDailyUsageUsd(sub.DailyUsageUSD).
		SetWeeklyUsageUsd(sub.WeeklyUsageUSD).
		SetMonthlyUsageUsd(sub.MonthlyUsageUSD).
		SetNillableAssignedBy(sub.AssignedBy)

	if sub.StartsAt.IsZero() {
		builder.SetStartsAt(time.Now())
	} else {
		builder.SetStartsAt(sub.StartsAt)
	}
	if sub.Status != "" {
		builder.SetStatus(sub.Status)
	}
	if !sub.AssignedAt.IsZero() {
		builder.SetAssignedAt(sub.AssignedAt)
	}
	// Keep compatibility with historical behavior: always store notes as a string value.
	builder.SetNotes(sub.Notes)

	created, err := builder.Save(ctx)
	if err == nil {
		applyUserSubscriptionEntityToService(sub, created)
	}
	return translatePersistenceError(err, nil, service.ErrSubscriptionAlreadyExists)
}

func (r *userSubscriptionRepository) GetByID(ctx context.Context, id int64) (*service.UserSubscription, error) {
	client := clientFromContext(ctx, r.client)
	m, err := client.UserSubscription.Query().
		Where(usersubscription.IDEQ(id)).
		WithUser().
		WithGroup().
		WithAssignedByUser().
		Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
	}
	sub := userSubscriptionEntityToService(m)
	if sub == nil {
		return nil, service.ErrSubscriptionNotFound
	}
	if sub.Group != nil && sub.Group.IsTotalQuotaSubscriptionType() {
		summary, err := r.GetQuotaSummary(ctx, sub.ID, time.Now())
		if err != nil {
			return nil, err
		}
		applyQuotaSummaryToSubscription(sub, summary)
	}
	return sub, nil
}

func (r *userSubscriptionRepository) GetByUserIDAndGroupID(ctx context.Context, userID, groupID int64) (*service.UserSubscription, error) {
	client := clientFromContext(ctx, r.client)
	m, err := client.UserSubscription.Query().
		Where(usersubscription.UserIDEQ(userID), usersubscription.GroupIDEQ(groupID)).
		WithGroup().
		Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
	}
	sub := userSubscriptionEntityToService(m)
	if sub == nil {
		return nil, service.ErrSubscriptionNotFound
	}
	if sub.Group != nil && sub.Group.IsTotalQuotaSubscriptionType() {
		summary, err := r.GetQuotaSummary(ctx, sub.ID, time.Now())
		if err != nil {
			return nil, err
		}
		applyQuotaSummaryToSubscription(sub, summary)
	}
	return sub, nil
}

func (r *userSubscriptionRepository) GetActiveByUserIDAndGroupID(ctx context.Context, userID, groupID int64) (*service.UserSubscription, error) {
	client := clientFromContext(ctx, r.client)
	m, err := client.UserSubscription.Query().
		Where(
			usersubscription.UserIDEQ(userID),
			usersubscription.GroupIDEQ(groupID),
			usersubscription.StatusEQ(service.SubscriptionStatusActive),
			usersubscription.ExpiresAtGT(time.Now()),
		).
		WithGroup().
		Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
	}
	sub := userSubscriptionEntityToService(m)
	if sub == nil {
		return nil, service.ErrSubscriptionNotFound
	}
	if sub.Group != nil && sub.Group.IsTotalQuotaSubscriptionType() {
		summary, err := r.GetQuotaSummary(ctx, sub.ID, time.Now())
		if err != nil {
			return nil, err
		}
		applyQuotaSummaryToSubscription(sub, summary)
	}
	return sub, nil
}

func (r *userSubscriptionRepository) Update(ctx context.Context, sub *service.UserSubscription) error {
	if sub == nil {
		return service.ErrSubscriptionNilInput
	}

	client := clientFromContext(ctx, r.client)
	builder := client.UserSubscription.UpdateOneID(sub.ID).
		SetUserID(sub.UserID).
		SetGroupID(sub.GroupID).
		SetStartsAt(sub.StartsAt).
		SetExpiresAt(sub.ExpiresAt).
		SetStatus(sub.Status).
		SetNillableDailyWindowStart(sub.DailyWindowStart).
		SetNillableWeeklyWindowStart(sub.WeeklyWindowStart).
		SetNillableMonthlyWindowStart(sub.MonthlyWindowStart).
		SetDailyUsageUsd(sub.DailyUsageUSD).
		SetWeeklyUsageUsd(sub.WeeklyUsageUSD).
		SetMonthlyUsageUsd(sub.MonthlyUsageUSD).
		SetNillableAssignedBy(sub.AssignedBy).
		SetAssignedAt(sub.AssignedAt).
		SetNotes(sub.Notes)

	updated, err := builder.Save(ctx)
	if err == nil {
		applyUserSubscriptionEntityToService(sub, updated)
		return nil
	}
	return translatePersistenceError(err, service.ErrSubscriptionNotFound, service.ErrSubscriptionAlreadyExists)
}

func (r *userSubscriptionRepository) Delete(ctx context.Context, id int64) error {
	// Match GORM semantics: deleting a missing row is not an error.
	client := clientFromContext(ctx, r.client)
	_, err := client.UserSubscription.Delete().Where(usersubscription.IDEQ(id)).Exec(ctx)
	return err
}

func (r *userSubscriptionRepository) ListByUserID(ctx context.Context, userID int64) ([]service.UserSubscription, error) {
	client := clientFromContext(ctx, r.client)
	subs, err := client.UserSubscription.Query().
		Where(usersubscription.UserIDEQ(userID)).
		WithGroup().
		Order(dbent.Desc(usersubscription.FieldCreatedAt)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := userSubscriptionEntitiesToService(subs)
	if err := r.populateQuotaSummaries(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *userSubscriptionRepository) ListActiveByUserID(ctx context.Context, userID int64) ([]service.UserSubscription, error) {
	client := clientFromContext(ctx, r.client)
	subs, err := client.UserSubscription.Query().
		Where(
			usersubscription.UserIDEQ(userID),
			usersubscription.StatusEQ(service.SubscriptionStatusActive),
			usersubscription.ExpiresAtGT(time.Now()),
		).
		WithGroup().
		Order(dbent.Desc(usersubscription.FieldCreatedAt)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := userSubscriptionEntitiesToService(subs)
	if err := r.populateQuotaSummaries(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *userSubscriptionRepository) ListByGroupID(ctx context.Context, groupID int64, params pagination.PaginationParams) ([]service.UserSubscription, *pagination.PaginationResult, error) {
	client := clientFromContext(ctx, r.client)
	q := client.UserSubscription.Query().Where(usersubscription.GroupIDEQ(groupID))

	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, nil, err
	}

	subs, err := q.
		WithUser().
		WithGroup().
		Order(dbent.Desc(usersubscription.FieldCreatedAt)).
		Offset(params.Offset()).
		Limit(params.Limit()).
		All(ctx)
	if err != nil {
		return nil, nil, err
	}

	out := userSubscriptionEntitiesToService(subs)
	if err := r.populateQuotaSummaries(ctx, out); err != nil {
		return nil, nil, err
	}
	return out, paginationResultFromTotal(int64(total), params), nil
}

func (r *userSubscriptionRepository) List(ctx context.Context, params pagination.PaginationParams, userID, groupID *int64, status, platform, sortBy, sortOrder string) ([]service.UserSubscription, *pagination.PaginationResult, error) {
	client := clientFromContext(ctx, r.client)
	q := client.UserSubscription.Query()
	if userID != nil {
		q = q.Where(usersubscription.UserIDEQ(*userID))
	}
	if groupID != nil {
		q = q.Where(usersubscription.GroupIDEQ(*groupID))
	}
	if platform != "" {
		q = q.Where(usersubscription.HasGroupWith(group.PlatformEQ(platform)))
	}

	// Status filtering with real-time expiration check
	now := time.Now()
	switch status {
	case service.SubscriptionStatusActive:
		// Active: status is active AND not yet expired
		q = q.Where(
			usersubscription.StatusEQ(service.SubscriptionStatusActive),
			usersubscription.ExpiresAtGT(now),
		)
	case service.SubscriptionStatusExpired:
		// Expired: status is expired OR (status is active but already expired)
		q = q.Where(
			usersubscription.Or(
				usersubscription.StatusEQ(service.SubscriptionStatusExpired),
				usersubscription.And(
					usersubscription.StatusEQ(service.SubscriptionStatusActive),
					usersubscription.ExpiresAtLTE(now),
				),
			),
		)
	case "":
		// No filter
	default:
		// Other status (e.g., revoked)
		q = q.Where(usersubscription.StatusEQ(status))
	}

	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, nil, err
	}

	// Apply sorting
	q = q.WithUser().WithGroup().WithAssignedByUser()

	// Determine sort field
	var field string
	switch sortBy {
	case "expires_at":
		field = usersubscription.FieldExpiresAt
	case "status":
		field = usersubscription.FieldStatus
	default:
		field = usersubscription.FieldCreatedAt
	}

	// Determine sort order (default: desc)
	if sortOrder == "asc" && sortBy != "" {
		q = q.Order(dbent.Asc(field))
	} else {
		q = q.Order(dbent.Desc(field))
	}

	subs, err := q.
		Offset(params.Offset()).
		Limit(params.Limit()).
		All(ctx)
	if err != nil {
		return nil, nil, err
	}

	out := userSubscriptionEntitiesToService(subs)
	if err := r.populateQuotaSummaries(ctx, out); err != nil {
		return nil, nil, err
	}
	return out, paginationResultFromTotal(int64(total), params), nil
}

func (r *userSubscriptionRepository) ExistsByUserIDAndGroupID(ctx context.Context, userID, groupID int64) (bool, error) {
	client := clientFromContext(ctx, r.client)
	return client.UserSubscription.Query().
		Where(usersubscription.UserIDEQ(userID), usersubscription.GroupIDEQ(groupID)).
		Exist(ctx)
}

func (r *userSubscriptionRepository) ExtendExpiry(ctx context.Context, subscriptionID int64, newExpiresAt time.Time) error {
	client := clientFromContext(ctx, r.client)
	_, err := client.UserSubscription.UpdateOneID(subscriptionID).
		SetExpiresAt(newExpiresAt).
		Save(ctx)
	return translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
}

func (r *userSubscriptionRepository) UpdateStatus(ctx context.Context, subscriptionID int64, status string) error {
	client := clientFromContext(ctx, r.client)
	_, err := client.UserSubscription.UpdateOneID(subscriptionID).
		SetStatus(status).
		Save(ctx)
	return translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
}

func (r *userSubscriptionRepository) UpdateNotes(ctx context.Context, subscriptionID int64, notes string) error {
	client := clientFromContext(ctx, r.client)
	_, err := client.UserSubscription.UpdateOneID(subscriptionID).
		SetNotes(notes).
		Save(ctx)
	return translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
}

func (r *userSubscriptionRepository) ActivateWindows(ctx context.Context, id int64, start time.Time) error {
	client := clientFromContext(ctx, r.client)
	_, err := client.UserSubscription.UpdateOneID(id).
		SetDailyWindowStart(start).
		SetWeeklyWindowStart(start).
		SetMonthlyWindowStart(start).
		Save(ctx)
	return translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
}

func (r *userSubscriptionRepository) ResetDailyUsage(ctx context.Context, id int64, newWindowStart time.Time) error {
	client := clientFromContext(ctx, r.client)
	_, err := client.UserSubscription.UpdateOneID(id).
		SetDailyUsageUsd(0).
		SetDailyWindowStart(newWindowStart).
		Save(ctx)
	return translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
}

func (r *userSubscriptionRepository) ResetWeeklyUsage(ctx context.Context, id int64, newWindowStart time.Time) error {
	client := clientFromContext(ctx, r.client)
	_, err := client.UserSubscription.UpdateOneID(id).
		SetWeeklyUsageUsd(0).
		SetWeeklyWindowStart(newWindowStart).
		Save(ctx)
	return translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
}

func (r *userSubscriptionRepository) ResetMonthlyUsage(ctx context.Context, id int64, newWindowStart time.Time) error {
	client := clientFromContext(ctx, r.client)
	_, err := client.UserSubscription.UpdateOneID(id).
		SetMonthlyUsageUsd(0).
		SetMonthlyWindowStart(newWindowStart).
		Save(ctx)
	return translatePersistenceError(err, service.ErrSubscriptionNotFound, nil)
}

// IncrementUsage 原子性地累加订阅用量。
// 限额检查已在请求前由 BillingCacheService.CheckBillingEligibility 完成，
// 此处仅负责记录实际消费，确保消费数据的完整性。
func (r *userSubscriptionRepository) IncrementUsage(ctx context.Context, id int64, costUSD float64) error {
	client := clientFromContext(ctx, r.client)
	return incrementSubscriptionUsage(ctx, client, id, costUSD)
}

func (r *userSubscriptionRepository) CreateQuotaEvent(ctx context.Context, event *service.UserSubscriptionQuotaEvent) error {
	if event == nil {
		return service.ErrSubscriptionNilInput
	}
	client := clientFromContext(ctx, r.client)
	builder := client.UserSubscriptionQuotaEvent.Create().
		SetUserSubscriptionID(event.UserSubscriptionID).
		SetQuotaTotalUsd(event.QuotaTotalUSD).
		SetQuotaUsedUsd(event.QuotaUsedUSD).
		SetStartsAt(event.StartsAt).
		SetExpiresAt(event.ExpiresAt).
		SetSourceKind(event.SourceKind)
	if strings.TrimSpace(event.SourceRef) != "" {
		builder.SetSourceRef(event.SourceRef)
	}
	created, err := builder.Save(ctx)
	if err != nil {
		return err
	}
	event.ID = created.ID
	event.CreatedAt = created.CreatedAt
	event.UpdatedAt = created.UpdatedAt
	return nil
}

func (r *userSubscriptionRepository) GetQuotaSummary(ctx context.Context, subscriptionID int64, now time.Time) (*service.UserSubscriptionQuotaSummary, error) {
	summaries, err := r.GetQuotaSummaryBatch(ctx, []int64{subscriptionID}, now)
	if err != nil {
		return nil, err
	}
	if summary, ok := summaries[subscriptionID]; ok {
		return summary, nil
	}
	return &service.UserSubscriptionQuotaSummary{}, nil
}

func (r *userSubscriptionRepository) GetQuotaSummaryBatch(ctx context.Context, subscriptionIDs []int64, now time.Time) (map[int64]*service.UserSubscriptionQuotaSummary, error) {
	result := make(map[int64]*service.UserSubscriptionQuotaSummary)
	if r == nil || len(subscriptionIDs) == 0 {
		return result, nil
	}

	normalized := make([]int64, 0, len(subscriptionIDs))
	seen := make(map[int64]struct{}, len(subscriptionIDs))
	for _, id := range subscriptionIDs {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		normalized = append(normalized, id)
	}
	if len(normalized) == 0 {
		return result, nil
	}

	exec := clientFromContext(ctx, r.client)
	rows, err := exec.QueryContext(ctx, `
		WITH active AS (
			SELECT
				user_subscription_id,
				quota_total_usd,
				quota_used_usd,
				expires_at,
				GREATEST(quota_total_usd - quota_used_usd, 0) AS remaining
			FROM user_subscription_quota_events
			WHERE user_subscription_id = ANY($1)
			  AND expires_at > $2
		),
		totals AS (
			SELECT
				user_subscription_id,
				COALESCE(SUM(quota_total_usd), 0) AS total_limit_usd,
				COALESCE(SUM(quota_used_usd), 0) AS total_used_usd,
				COALESCE(SUM(remaining), 0) AS total_remaining_usd,
				MIN(CASE WHEN remaining > 0 THEN expires_at END) AS next_quota_expire_at
			FROM active
			GROUP BY user_subscription_id
		)
		SELECT
			t.user_subscription_id,
			t.total_limit_usd,
			t.total_used_usd,
			t.total_remaining_usd,
			t.next_quota_expire_at,
			COALESCE(SUM(a.remaining) FILTER (
				WHERE a.remaining > 0 AND a.expires_at = t.next_quota_expire_at
			), 0) AS next_expiring_quota_usd
		FROM totals t
		LEFT JOIN active a ON a.user_subscription_id = t.user_subscription_id
		GROUP BY
			t.user_subscription_id,
			t.total_limit_usd,
			t.total_used_usd,
			t.total_remaining_usd,
			t.next_quota_expire_at
	`, pq.Array(normalized), now.UTC())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			subscriptionID         int64
			totalLimitUSD          float64
			totalUsedUSD           float64
			totalRemainingUSD      float64
			nextQuotaExpireAt      sql.NullTime
			nextExpiringQuotaUSD   float64
		)
		if err := rows.Scan(
			&subscriptionID,
			&totalLimitUSD,
			&totalUsedUSD,
			&totalRemainingUSD,
			&nextQuotaExpireAt,
			&nextExpiringQuotaUSD,
		); err != nil {
			return nil, err
		}
		summary := &service.UserSubscriptionQuotaSummary{
			TotalLimitUSD:        totalLimitUSD,
			TotalUsedUSD:         totalUsedUSD,
			TotalRemainingUSD:    totalRemainingUSD,
			NextExpiringQuotaUSD: nextExpiringQuotaUSD,
		}
		if nextQuotaExpireAt.Valid {
			t := nextQuotaExpireAt.Time
			summary.NextQuotaExpireAt = &t
		}
		result[subscriptionID] = summary
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return result, nil
}

func (r *userSubscriptionRepository) GetQuotaSpendSnapshot(ctx context.Context, subscriptionID int64, now time.Time) (*service.TotalQuotaSpendSnapshot, error) {
	exec := clientFromContext(ctx, r.client)
	rows, err := exec.QueryContext(ctx, `
		SELECT id
		FROM user_subscription_quota_events
		WHERE user_subscription_id = $1
		  AND expires_at > $2
		ORDER BY expires_at ASC, id ASC
	`, subscriptionID, now.UTC())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	eventIDs := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		eventIDs = append(eventIDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(eventIDs) == 0 {
		return nil, nil
	}

	return &service.TotalQuotaSpendSnapshot{
		SubscriptionID:  subscriptionID,
		EventIDs:        eventIDs,
		OverflowEventID: eventIDs[len(eventIDs)-1],
		TakenAt:         now.UTC(),
	}, nil
}

func (r *userSubscriptionRepository) BatchUpdateExpiredStatus(ctx context.Context) (int64, error) {
	client := clientFromContext(ctx, r.client)
	n, err := client.UserSubscription.Update().
		Where(
			usersubscription.StatusEQ(service.SubscriptionStatusActive),
			usersubscription.ExpiresAtLTE(time.Now()),
		).
		SetStatus(service.SubscriptionStatusExpired).
		Save(ctx)
	return int64(n), err
}

// Extra repository helpers (currently used only by integration tests).

func (r *userSubscriptionRepository) ListExpired(ctx context.Context) ([]service.UserSubscription, error) {
	client := clientFromContext(ctx, r.client)
	subs, err := client.UserSubscription.Query().
		Where(
			usersubscription.StatusEQ(service.SubscriptionStatusActive),
			usersubscription.ExpiresAtLTE(time.Now()),
		).
		All(ctx)
	if err != nil {
		return nil, err
	}
	return userSubscriptionEntitiesToService(subs), nil
}

func (r *userSubscriptionRepository) CountByGroupID(ctx context.Context, groupID int64) (int64, error) {
	client := clientFromContext(ctx, r.client)
	count, err := client.UserSubscription.Query().Where(usersubscription.GroupIDEQ(groupID)).Count(ctx)
	return int64(count), err
}

func (r *userSubscriptionRepository) CountActiveByGroupID(ctx context.Context, groupID int64) (int64, error) {
	client := clientFromContext(ctx, r.client)
	count, err := client.UserSubscription.Query().
		Where(
			usersubscription.GroupIDEQ(groupID),
			usersubscription.StatusEQ(service.SubscriptionStatusActive),
			usersubscription.ExpiresAtGT(time.Now()),
		).
		Count(ctx)
	return int64(count), err
}

func (r *userSubscriptionRepository) DeleteByGroupID(ctx context.Context, groupID int64) (int64, error) {
	client := clientFromContext(ctx, r.client)
	n, err := client.UserSubscription.Delete().Where(usersubscription.GroupIDEQ(groupID)).Exec(ctx)
	return int64(n), err
}

func userSubscriptionEntityToService(m *dbent.UserSubscription) *service.UserSubscription {
	if m == nil {
		return nil
	}
	out := &service.UserSubscription{
		ID:                 m.ID,
		UserID:             m.UserID,
		GroupID:            m.GroupID,
		StartsAt:           m.StartsAt,
		ExpiresAt:          m.ExpiresAt,
		Status:             m.Status,
		DailyWindowStart:   m.DailyWindowStart,
		WeeklyWindowStart:  m.WeeklyWindowStart,
		MonthlyWindowStart: m.MonthlyWindowStart,
		DailyUsageUSD:      m.DailyUsageUsd,
		WeeklyUsageUSD:     m.WeeklyUsageUsd,
		MonthlyUsageUSD:    m.MonthlyUsageUsd,
		AssignedBy:         m.AssignedBy,
		AssignedAt:         m.AssignedAt,
		Notes:              derefString(m.Notes),
		CreatedAt:          m.CreatedAt,
		UpdatedAt:          m.UpdatedAt,
	}
	if m.Edges.User != nil {
		out.User = userEntityToService(m.Edges.User)
	}
	if m.Edges.Group != nil {
		out.Group = groupEntityToService(m.Edges.Group)
	}
	if m.Edges.AssignedByUser != nil {
		out.AssignedByUser = userEntityToService(m.Edges.AssignedByUser)
	}
	if len(m.Edges.QuotaEvents) > 0 {
		out.QuotaEvents = make([]service.UserSubscriptionQuotaEvent, 0, len(m.Edges.QuotaEvents))
		for i := range m.Edges.QuotaEvents {
			out.QuotaEvents = append(out.QuotaEvents, quotaEventEntityToService(m.Edges.QuotaEvents[i]))
		}
	}
	return out
}

func userSubscriptionEntitiesToService(models []*dbent.UserSubscription) []service.UserSubscription {
	out := make([]service.UserSubscription, 0, len(models))
	for i := range models {
		if s := userSubscriptionEntityToService(models[i]); s != nil {
			out = append(out, *s)
		}
	}
	return out
}

func applyUserSubscriptionEntityToService(dst *service.UserSubscription, src *dbent.UserSubscription) {
	if dst == nil || src == nil {
		return
	}
	dst.ID = src.ID
	dst.CreatedAt = src.CreatedAt
	dst.UpdatedAt = src.UpdatedAt
}

func quotaEventEntityToService(m *dbent.UserSubscriptionQuotaEvent) service.UserSubscriptionQuotaEvent {
	return service.UserSubscriptionQuotaEvent{
		ID:                 m.ID,
		UserSubscriptionID: m.UserSubscriptionID,
		QuotaTotalUSD:      m.QuotaTotalUsd,
		QuotaUsedUSD:       m.QuotaUsedUsd,
		StartsAt:           m.StartsAt,
		ExpiresAt:          m.ExpiresAt,
		SourceKind:         m.SourceKind,
		SourceRef:          derefString(m.SourceRef),
		CreatedAt:          m.CreatedAt,
		UpdatedAt:          m.UpdatedAt,
	}
}

func applyQuotaSummaryToSubscription(sub *service.UserSubscription, summary *service.UserSubscriptionQuotaSummary) {
	if sub == nil {
		return
	}
	sub.TotalLimitUSD = 0
	sub.TotalUsedUSD = 0
	sub.TotalRemainingUSD = 0
	sub.NextQuotaExpireAt = nil
	sub.NextExpiringQuotaUSD = 0
	if summary == nil {
		return
	}
	sub.TotalLimitUSD = summary.TotalLimitUSD
	sub.TotalUsedUSD = summary.TotalUsedUSD
	sub.TotalRemainingUSD = summary.TotalRemainingUSD
	sub.NextQuotaExpireAt = summary.NextQuotaExpireAt
	sub.NextExpiringQuotaUSD = summary.NextExpiringQuotaUSD
}

func (r *userSubscriptionRepository) populateQuotaSummaries(ctx context.Context, subs []service.UserSubscription) error {
	if len(subs) == 0 {
		return nil
	}

	ids := make([]int64, 0, len(subs))
	for i := range subs {
		if subs[i].Group != nil && subs[i].Group.IsTotalQuotaSubscriptionType() {
			ids = append(ids, subs[i].ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}

	summaries, err := r.GetQuotaSummaryBatch(ctx, ids, time.Now())
	if err != nil {
		return err
	}
	for i := range subs {
		if subs[i].Group != nil && subs[i].Group.IsTotalQuotaSubscriptionType() {
			applyQuotaSummaryToSubscription(&subs[i], summaries[subs[i].ID])
		}
	}
	return nil
}

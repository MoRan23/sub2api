package repository

import (
	"context"
	"fmt"
	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	dbapikey "github.com/Wei-Shaw/sub2api/ent/apikey"
	dbgroup "github.com/Wei-Shaw/sub2api/ent/group"
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
	dbuser "github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"strings"
)

func (r *usageLogRepository) PrepareUsageExport(ctx context.Context, spec service.UsageExportSpec) (maxID, total int64, err error) {
	conditions, args := usageLogFilterConditions(spec.Filters)
	err = scanSingleRow(ctx, r.sql, "SELECT COALESCE(MAX(id),0),COUNT(*) FROM usage_logs "+buildWhere(conditions), args, &maxID, &total)
	return
}

func (r *usageLogRepository) ReadUsageExport(ctx context.Context, spec service.UsageExportSpec, maxID int64, cursor *service.UsageExportCursor, limit int, restricted bool) ([]service.UsageLog, *service.UsageExportCursor, error) {
	conditions, args := usageLogFilterConditions(spec.Filters)
	conditions = append(conditions, fmt.Sprintf("id <= $%d", len(args)+1))
	args = append(args, maxID)
	column := "id"
	switch spec.SortBy {
	case "created_at":
		column = "created_at"
	case "model":
		column = "COALESCE(NULLIF(TRIM(requested_model), ''), model)"
	}
	direction, op := "DESC", "<"
	if strings.EqualFold(spec.SortOrder, "asc") {
		direction, op = "ASC", ">"
	}
	if cursor != nil {
		if column == "id" {
			conditions = append(conditions, fmt.Sprintf("id %s $%d", op, len(args)+1))
			args = append(args, cursor.ID)
		} else {
			var value any = cursor.CreatedAt
			if spec.SortBy == "model" {
				value = cursor.Model
			}
			conditions = append(conditions, fmt.Sprintf("(%s,id) %s ($%d,$%d)", column, op, len(args)+1, len(args)+2))
			args = append(args, value, cursor.ID)
		}
	}
	if limit < 1 || limit > 1000 {
		limit = 1000
	}
	order := column + " " + direction
	if column != "id" {
		order += ",id " + direction
	}
	query := fmt.Sprintf("SELECT %s FROM usage_logs %s ORDER BY %s LIMIT $%d", usageLogSelectColumns, buildWhere(conditions), order, len(args)+1)
	args = append(args, limit)
	var logs []service.UsageLog
	var modelCursor string
	var err error
	if spec.SortBy == "model" {
		logs, modelCursor, err = r.queryUsageExportModels(ctx, query, args...)
	} else {
		logs, err = r.queryUsageLogs(ctx, query, args...)
	}
	if err != nil {
		return nil, nil, err
	}
	if !restricted {
		if err = r.hydrateUsageExportLabels(ctx, logs); err != nil {
			return nil, nil, err
		}
	}
	if len(logs) == 0 {
		return logs, nil, nil
	}
	last := logs[len(logs)-1]
	return logs, &service.UsageExportCursor{ID: last.ID, CreatedAt: last.CreatedAt, Model: modelCursor}, nil
}

func (r *usageLogRepository) queryUsageExportModels(ctx context.Context, query string, args ...any) (logs []service.UsageLog, model string, err error) {
	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			logs = nil
			err = closeErr
		}
	}()
	logs = make([]service.UsageLog, 0)
	for rows.Next() {
		var log *service.UsageLog
		log, err = scanUsageLog(rows, &model)
		if err != nil {
			return nil, "", err
		}
		logs = append(logs, *log)
	}
	if err = rows.Err(); err != nil {
		return nil, "", err
	}
	return
}

func usageExportModelCursor(requested, fallback string) string {
	// SQL TRIM/BTRIM without an explicit character set only removes ASCII space.
	model := strings.Trim(requested, " ")
	if model == "" {
		model = fallback
	}
	return model
}

// Exports need four labels, never credentials, user metadata, subscriptions or
// the nested account graph loaded for interactive usage detail responses.
func (r *usageLogRepository) hydrateUsageExportLabels(ctx context.Context, logs []service.UsageLog) error {
	if len(logs) == 0 {
		return nil
	}
	ids := collectUsageLogIDs(logs)
	users, e := r.client.User.Query().Where(dbuser.IDIn(ids.userIDs...)).Select(dbuser.FieldID, dbuser.FieldEmail).All(mixins.SkipSoftDelete(ctx))
	if e != nil {
		return e
	}
	keys, e := r.client.APIKey.Query().Where(dbapikey.IDIn(ids.apiKeyIDs...)).Select(dbapikey.FieldID, dbapikey.FieldName).All(ctx)
	if e != nil {
		return e
	}
	accounts, e := r.client.Account.Query().Where(dbaccount.IDIn(ids.accountIDs...)).Select(dbaccount.FieldID, dbaccount.FieldName).All(ctx)
	if e != nil {
		return e
	}
	groups, e := r.client.Group.Query().Where(dbgroup.IDIn(ids.groupIDs...)).Select(dbgroup.FieldID, dbgroup.FieldName).All(ctx)
	if e != nil {
		return e
	}
	um := make(map[int64]*service.User, len(users))
	for _, v := range users {
		um[v.ID] = &service.User{ID: v.ID, Email: v.Email}
	}
	km := make(map[int64]*service.APIKey, len(keys))
	for _, v := range keys {
		km[v.ID] = &service.APIKey{ID: v.ID, Name: v.Name}
	}
	am := make(map[int64]*service.Account, len(accounts))
	for _, v := range accounts {
		am[v.ID] = &service.Account{ID: v.ID, Name: v.Name}
	}
	gm := make(map[int64]*service.Group, len(groups))
	for _, v := range groups {
		gm[v.ID] = &service.Group{ID: v.ID, Name: v.Name}
	}
	for i := range logs {
		v := &logs[i]
		v.User = um[v.UserID]
		v.APIKey = km[v.APIKeyID]
		v.Account = am[v.AccountID]
		if v.GroupID != nil {
			v.Group = gm[*v.GroupID]
		}
	}
	return nil
}

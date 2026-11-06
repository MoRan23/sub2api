package repository

import (
	"context"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/userplatformquota"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// LoadCurrentUserPlatformQuota gives Redis and transactional Engine increments
// one authoritative window anchor, including the rolling thirty-day window.
func (a *userPlatformQuotaServiceAdapter) LoadCurrentUserPlatformQuota(ctx context.Context, userID int64, platform string, now time.Time) (*service.UserPlatformQuotaRecord, error) {
	var record *UserPlatformQuotaRecord
	now = now.Truncate(time.Microsecond)
	err := a.inner.withTx(ctx, func(txCtx context.Context, client *dbent.Client) error {
		row, err := client.UserPlatformQuota.Query().Where(userplatformquota.UserIDEQ(userID), userplatformquota.PlatformEQ(platform), userplatformquota.DeletedAtIsNil()).ForUpdate().Only(txCtx)
		if dbent.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		day, week := timezone.StartOfDay(now), timezone.StartOfWeek(now)
		update := row.Update()
		changed := false
		if row.DailyWindowStart == nil || row.DailyWindowStart.Before(day) {
			update.SetDailyUsageUsd(0).SetEngineDailyUsageUsd(0).SetDailyWindowStart(day)
			changed = true
		}
		if row.WeeklyWindowStart == nil || row.WeeklyWindowStart.Before(week) {
			update.SetWeeklyUsageUsd(0).SetEngineWeeklyUsageUsd(0).SetWeeklyWindowStart(week)
			changed = true
		}
		if row.MonthlyWindowStart == nil || now.Sub(*row.MonthlyWindowStart) >= 30*24*time.Hour {
			update.SetMonthlyUsageUsd(0).SetEngineMonthlyUsageUsd(0).SetMonthlyWindowStart(now)
			changed = true
		}
		if changed {
			row, err = update.Save(txCtx)
			if err != nil {
				return err
			}
		}
		record = entQuotaToRecord(row)
		return nil
	})
	if err != nil || record == nil {
		return nil, err
	}
	return toServiceRecord(record), nil
}

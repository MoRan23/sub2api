package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"sync"
	"time"
)

const officialStatusHistoryDays = 91

var officialStatusHistoryGroupIDs = [3]string{
	"01K5H8S53SY1KMS4GQMNMQM1K5",
	"01K5H8S53SY1KMS4GQMNMZXTR1",
	"01KMKF9EBTCD8BN9PG8DJZXRSQ",
}

type OfficialStatusDay struct {
	Date   string `json:"date"`
	Status string `json:"status"`
}

// Days belongs to the published snapshot; responses only copy this metadata.
type OfficialStatusHistory struct {
	Days          []OfficialStatusDay `json:"days"`
	UptimePercent *float64            `json:"uptime_percent"`
	FetchedAt     *time.Time          `json:"fetched_at"`
	Stale         bool                `json:"stale"`
}

type officialHistoryComponent struct {
	ComponentID   string `json:"component_id"`
	DisplayUptime bool   `json:"display_uptime"`
}

type officialHistorySummary struct {
	Summary struct {
		DataAvailableSince time.Time `json:"data_available_since"`
		Structure          struct {
			Items []struct {
				Group *struct {
					ID                      string                     `json:"id"`
					DisplayAggregatedUptime bool                       `json:"display_aggregated_uptime"`
					Components              []officialHistoryComponent `json:"components"`
				} `json:"group"`
				Component *officialHistoryComponent `json:"component"`
			} `json:"items"`
		} `json:"structure"`
	} `json:"summary"`
}

type officialHistoryImpact struct {
	ComponentID string     `json:"component_id"`
	StartAt     time.Time  `json:"start_at"`
	EndAt       *time.Time `json:"end_at"`
	Status      string     `json:"status"`
}

type officialHistoryUptime struct {
	ComponentID        string    `json:"component_id"`
	GroupID            string    `json:"status_page_component_group_id"`
	DataAvailableSince time.Time `json:"data_available_since"`
	Uptime             string    `json:"uptime"`
}

type officialHistoryEnvelope struct {
	Impacts []officialHistoryImpact `json:"component_impacts"`
	Uptimes []officialHistoryUptime `json:"component_uptimes"`
}

func officialStatusUTCDay(at time.Time) time.Time {
	return at.UTC().Truncate(24 * time.Hour)
}

func (s *OfficialStatusService) fetchHistory(ctx context.Context, now time.Time) ([3]OfficialStatusHistory, error) {
	var result [3]OfficialStatusHistory
	endpoint, err := url.Parse(s.historyURL)
	if err != nil {
		return result, err
	}
	// Match the official 90-days-ago through today view, including today's bar.
	end := officialStatusUTCDay(now).AddDate(0, 0, 1).Add(-time.Millisecond)
	query := endpoint.Query()
	query.Set("start_at", end.AddDate(0, 0, -officialStatusHistoryDays).Format(time.RFC3339Nano))
	query.Set("end_at", end.Format(time.RFC3339Nano))
	endpoint.RawQuery = query.Encode()
	var summary, impacts []byte
	var summaryErr, impactsErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); summary, summaryErr = s.fetch(ctx, s.summaryURL) }()
	go func() { defer wg.Done(); impacts, impactsErr = s.fetch(ctx, endpoint.String()) }()
	wg.Wait()
	if summaryErr != nil || impactsErr != nil {
		return result, errors.Join(summaryErr, impactsErr)
	}
	return parseOfficialStatusHistory(summary, impacts, now)
}

func parseOfficialStatusHistory(summaryBody, historyBody []byte, now time.Time) ([3]OfficialStatusHistory, error) {
	var result [3]OfficialStatusHistory
	var summary officialHistorySummary
	var envelope officialHistoryEnvelope
	if err := json.Unmarshal(summaryBody, &summary); err != nil {
		return result, err
	}
	if err := json.Unmarshal(historyBody, &envelope); err != nil {
		return result, err
	}
	items := summary.Summary.Structure.Items
	if now.IsZero() || summary.Summary.DataAvailableSince.IsZero() || len(items) == 0 || len(items) > 64 || envelope.Impacts == nil || len(envelope.Impacts) > 5000 || envelope.Uptimes == nil || len(envelope.Uptimes) > 512 {
		return result, errors.New("incomplete official status history")
	}
	groups := make(map[string]int, 3)
	for i, id := range officialStatusHistoryGroupIDs {
		groups[id] = i
	}
	components := make(map[string]int)
	expected := [3]int{}
	seenGroups := [3]bool{}
	for _, item := range items {
		index := -1
		var members []officialHistoryComponent
		if item.Group != nil {
			group := item.Group
			if i, ok := groups[group.ID]; ok {
				if seenGroups[i] || !group.DisplayAggregatedUptime {
					return result, errors.New("invalid official history group")
				}
				seenGroups[i], index = true, i
			}
			members = group.Components
		}
		if item.Component != nil {
			members = append(members, *item.Component)
		}
		for _, member := range members {
			if member.ComponentID == "" || len(member.ComponentID) > 128 || len(components) >= 256 {
				return result, errors.New("invalid official history component")
			}
			if _, exists := components[member.ComponentID]; exists {
				return result, errors.New("duplicate official history component")
			}
			components[member.ComponentID] = -1
			if index >= 0 && member.DisplayUptime {
				components[member.ComponentID] = index
				expected[index]++
			}
		}
	}
	for i := range expected {
		if !seenGroups[i] || expected[i] == 0 {
			return result, errors.New("missing official history group")
		}
	}
	covered := [3]int{}
	availableSince := [3]time.Time{}
	seenUptimes := make(map[string]bool, len(envelope.Uptimes))
	for _, uptime := range envelope.Uptimes {
		if (uptime.GroupID == "") == (uptime.ComponentID == "") || uptime.DataAvailableSince.IsZero() {
			return result, errors.New("invalid official history uptime identity")
		}
		percent, err := strconv.ParseFloat(uptime.Uptime, 64)
		if err != nil || !validOfficialUptime(percent) {
			return result, errors.New("invalid official history uptime percentage")
		}
		key := uptime.GroupID + ":" + uptime.ComponentID
		if seenUptimes[key] {
			return result, errors.New("duplicate official history uptime")
		}
		seenUptimes[key] = true
		if uptime.GroupID != "" {
			if i, ok := groups[uptime.GroupID]; ok {
				result[i].UptimePercent = &percent
				availableSince[i] = officialStatusUTCDay(uptime.DataAvailableSince)
			}
			continue
		}
		if i, ok := components[uptime.ComponentID]; ok && i >= 0 {
			covered[i]++
		}
	}
	start := officialStatusUTCDay(now).AddDate(0, 0, -(officialStatusHistoryDays - 1))
	var dates [officialStatusHistoryDays]string
	for day := range dates {
		dates[day] = start.Add(time.Duration(day) * 24 * time.Hour).Format(time.DateOnly)
	}
	summaryStart := officialStatusUTCDay(summary.Summary.DataAvailableSince)
	for i := range result {
		result[i].Days = make([]OfficialStatusDay, officialStatusHistoryDays)
		result[i].FetchedAt = &now
		complete := covered[i] == expected[i] && !availableSince[i].IsZero()
		for day := range result[i].Days {
			at := start.Add(time.Duration(day) * 24 * time.Hour)
			status := "unknown"
			if complete && !at.Before(availableSince[i]) && !at.Before(summaryStart) {
				status = "operational"
			}
			result[i].Days[day] = OfficialStatusDay{Date: dates[day], Status: status}
		}
	}
	for _, impact := range envelope.Impacts {
		if impact.ComponentID == "" || len(impact.ComponentID) > 128 {
			return result, errors.New("invalid official history impact component")
		}
		index, known := components[impact.ComponentID]
		// The official page projects history through current displayed membership.
		// Retired components can still occur in historical incidents.
		if !known || index < 0 {
			continue
		}
		status := impact.Status
		if status == "full_outage" {
			status = "major_outage"
		}
		if impact.StartAt.IsZero() || officialStatusRank(status) < 0 || status == "unknown" || (impact.EndAt != nil && (impact.EndAt.IsZero() || impact.EndAt.Before(impact.StartAt))) {
			return result, errors.New("invalid official history impact")
		}
		end := now
		if impact.EndAt != nil && impact.EndAt.Before(end) {
			end = *impact.EndAt
		}
		impactStart := impact.StartAt
		if impactStart.Before(start) {
			impactStart = start
		}
		if !end.After(impactStart) {
			continue
		}
		first := int(impactStart.Sub(start) / (24 * time.Hour))
		last := int(end.Add(-time.Nanosecond).Sub(start) / (24 * time.Hour))
		for day := first; day <= last; day++ {
			if officialStatusRank(status) > officialStatusRank(result[index].Days[day].Status) {
				result[index].Days[day].Status = status
			}
		}
	}
	return result, nil
}

func validOfficialUptime(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 100
}

func validateOfficialStatusHistory(history OfficialStatusHistory, checkedAt time.Time) error {
	if history.Days == nil || len(history.Days) > officialStatusHistoryDays {
		return errors.New("invalid official status history size")
	}
	if history.UptimePercent != nil && !validOfficialUptime(*history.UptimePercent) {
		return errors.New("invalid official status history uptime")
	}
	if history.FetchedAt == nil {
		if len(history.Days) != 0 || history.UptimePercent != nil || !history.Stale {
			return errors.New("official history without source timestamp")
		}
		return nil
	}
	if history.FetchedAt.IsZero() || history.FetchedAt.After(checkedAt) || len(history.Days) != officialStatusHistoryDays {
		return errors.New("invalid official history source timestamp")
	}
	start := officialStatusUTCDay(*history.FetchedAt).AddDate(0, 0, -(officialStatusHistoryDays - 1))
	for i, day := range history.Days {
		if day.Date != start.AddDate(0, 0, i).Format(time.DateOnly) || officialStatusRank(day.Status) < 0 {
			return fmt.Errorf("invalid official history day %d", i)
		}
	}
	return nil
}

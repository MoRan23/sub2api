package service

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const officialStatusTestSummary = `{"summary":{"data_available_since":"2020-01-01T00:00:00Z","structure":{"items":[
{"group":{"id":"01K5H8S53SY1KMS4GQMNMQM1K5","display_aggregated_uptime":true,"components":[{"component_id":"api","display_uptime":true},{"component_id":"not-displayed","display_uptime":false}]}},
{"group":{"id":"01K5H8S53SY1KMS4GQMNMZXTR1","display_aggregated_uptime":true,"components":[{"component_id":"chat","display_uptime":true}]}},
{"group":{"id":"01KMKF9EBTCD8BN9PG8DJZXRSQ","display_aggregated_uptime":true,"components":[{"component_id":"codex","display_uptime":true}]}}
]}}}`

func officialStatusTestHistory(impacts string) string {
	return fmt.Sprintf(`{"component_impacts":[%s],"component_uptimes":[
{"component_id":"api","data_available_since":"2020-01-01T00:00:00Z","uptime":"100"},
{"component_id":"chat","data_available_since":"2020-01-01T00:00:00Z","uptime":"100"},
{"component_id":"codex","data_available_since":"2020-01-01T00:00:00Z","uptime":"100"},
{"status_page_component_group_id":"01K5H8S53SY1KMS4GQMNMQM1K5","data_available_since":"2020-01-01T00:00:00Z","uptime":"99.97"},
{"status_page_component_group_id":"01K5H8S53SY1KMS4GQMNMZXTR1","data_available_since":"2020-01-01T00:00:00Z","uptime":"99.26"},
{"status_page_component_group_id":"01KMKF9EBTCD8BN9PG8DJZXRSQ","data_available_since":"2020-01-01T00:00:00Z","uptime":"99.94"}]}`, impacts)
}

func TestOfficialStatusHistoryUTCOverlapAndGroupUptime(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	impacts := `{"component_id":"api","start_at":"2026-11-18T23:50:00Z","end_at":"2026-11-20T00:00:00Z","status":"full_outage"},
{"component_id":"api","start_at":"2026-11-19T01:00:00Z","end_at":"2026-11-19T02:00:00Z","status":"degraded_performance"},
{"component_id":"chat","start_at":"2026-11-19T00:00:00Z","end_at":"2026-11-20T00:00:00Z","status":"partial_outage"},
{"component_id":"codex","start_at":"2026-11-19T23:00:00Z","status":"degraded_performance"},
{"component_id":"chat","start_at":"2026-11-20T13:00:00Z","status":"full_outage"},
{"component_id":"not-displayed","start_at":"2026-11-01T00:00:00Z","status":"full_outage"},
{"component_id":"retired-component","start_at":"2026-11-01T00:00:00Z","status":"full_outage"}`
	histories, err := parseOfficialStatusHistory([]byte(officialStatusTestSummary), []byte(officialStatusTestHistory(impacts)), now)
	require.NoError(t, err)
	for i, history := range histories {
		require.Len(t, history.Days, 91)
		require.Equal(t, "2026-08-22", history.Days[0].Date)
		require.Equal(t, "2026-11-20", history.Days[90].Date)
		require.Equal(t, []float64{99.97, 99.26, 99.94}[i], *history.UptimePercent)
		require.Equal(t, &now, history.FetchedAt)
		require.False(t, history.Stale)
	}
	require.Equal(t, "major_outage", histories[0].Days[88].Status)
	require.Equal(t, "major_outage", histories[0].Days[89].Status)
	require.Equal(t, "operational", histories[0].Days[90].Status, "midnight ending must not color the following day")
	require.Equal(t, "partial_outage", histories[1].Days[89].Status, "midnight start must cover the full day")
	require.Equal(t, "operational", histories[1].Days[90].Status, "future impacts do not apply")
	require.Equal(t, "degraded_performance", histories[2].Days[89].Status)
	require.Equal(t, "degraded_performance", histories[2].Days[90].Status)
}

func TestOfficialStatusHistoryRejectsInvalidSource(t *testing.T) {
	now := time.Now().UTC()
	valid := officialStatusTestHistory("")
	for name, body := range map[string]string{
		"missing impacts":       `{"component_uptimes":[]}`,
		"missing uptimes":       `{"component_impacts":[]}`,
		"null impacts":          strings.Replace(valid, `"component_impacts":[]`, `"component_impacts":null`, 1),
		"nonfinite uptime":      strings.Replace(valid, `"99.97"`, `"NaN"`, 1),
		"out of range uptime":   strings.Replace(valid, `"99.97"`, `"100.01"`, 1),
		"unknown status":        officialStatusTestHistory(`{"component_id":"api","start_at":"2026-11-19T00:00:00Z","status":"future-status"}`),
		"missing start":         officialStatusTestHistory(`{"component_id":"api","status":"partial_outage"}`),
		"missing component id":  officialStatusTestHistory(`{"start_at":"2026-11-19T00:00:00Z","status":"full_outage"}`),
		"oversize component id": officialStatusTestHistory(fmt.Sprintf(`{"component_id":%q,"start_at":"2026-11-19T00:00:00Z","status":"full_outage"}`, strings.Repeat("x", 129))),
		"inverted interval":     officialStatusTestHistory(`{"component_id":"api","start_at":"2026-11-20T00:00:00Z","end_at":"2026-11-19T00:00:00Z","status":"partial_outage"}`),
		"too many impacts":      officialStatusTestHistory(strings.TrimSuffix(strings.Repeat(`{"component_id":"api","start_at":"2026-11-19T00:00:00Z","status":"partial_outage"},`, 5001), ",")),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseOfficialStatusHistory([]byte(officialStatusTestSummary), []byte(body), now)
			require.Error(t, err)
		})
	}
	for _, summary := range []string{`{}`, strings.Replace(officialStatusTestSummary, `"codex"`, `"api"`, 1), strings.Replace(officialStatusTestSummary, `"display_aggregated_uptime":true`, `"display_aggregated_uptime":false`, 1)} {
		_, err := parseOfficialStatusHistory([]byte(summary), []byte(valid), now)
		require.Error(t, err)
	}
}

func TestOfficialStatusHistoryIncompleteCoverageStaysUnknown(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	var source officialHistoryEnvelope
	require.NoError(t, json.Unmarshal([]byte(officialStatusTestHistory("")), &source))
	source.Uptimes = source.Uptimes[1:] // Missing API component coverage, but official group percentage remains.
	body, err := json.Marshal(source)
	require.NoError(t, err)
	history, err := parseOfficialStatusHistory([]byte(officialStatusTestSummary), body, now)
	require.NoError(t, err)
	require.Equal(t, "unknown", history[0].Days[90].Status)
	require.Equal(t, "operational", history[1].Days[90].Status)
	source.Uptimes = source.Uptimes[:len(source.Uptimes)-1] // Missing Codex group uptime.
	body, err = json.Marshal(source)
	require.NoError(t, err)
	history, err = parseOfficialStatusHistory([]byte(officialStatusTestSummary), body, now)
	require.NoError(t, err)
	require.Nil(t, history[2].UptimePercent)
	require.Equal(t, "unknown", history[2].Days[90].Status)
	limited := strings.Replace(officialStatusTestSummary, "2020-01-01T00:00:00Z", "2026-11-19T00:00:00Z", 1)
	history, err = parseOfficialStatusHistory([]byte(limited), []byte(officialStatusTestHistory("")), now)
	require.NoError(t, err)
	require.Equal(t, "unknown", history[1].Days[88].Status)
	require.Equal(t, "operational", history[1].Days[89].Status)
}

func TestOfficialStatusHistoryCacheValidationAndSharedDays(t *testing.T) {
	now := time.Now().UTC()
	histories, err := parseOfficialStatusHistory([]byte(officialStatusTestSummary), []byte(officialStatusTestHistory("")), now)
	require.NoError(t, err)
	snapshot := emptyOfficialStatus()
	snapshot.CheckedAt = now
	for i := range snapshot.Groups {
		snapshot.Groups[i].History = histories[i]
	}
	require.NoError(t, ValidateOfficialStatusSnapshot(snapshot))
	s := &OfficialStatusService{}
	s.snapshot.Store(snapshot)
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			got := s.Get()
			require.Same(t, &snapshot.Groups[0].History.Days[0], &got.Groups[0].History.Days[0])
			got.Groups[0].History.Stale = true
		})
	}
	wg.Wait()
	require.False(t, snapshot.Groups[0].History.Stale)
	for _, mutate := range []func(*OfficialStatusHistory){
		func(h *OfficialStatusHistory) { h.Days = nil },
		func(h *OfficialStatusHistory) { h.Days = h.Days[:90] },
		func(h *OfficialStatusHistory) { h.Days = append(h.Days, OfficialStatusDay{}) },
		func(h *OfficialStatusHistory) { h.FetchedAt = nil },
		func(h *OfficialStatusHistory) { later := now.Add(time.Hour); h.FetchedAt = &later },
		func(h *OfficialStatusHistory) { percent := math.Inf(1); h.UptimePercent = &percent },
		func(h *OfficialStatusHistory) { h.Days[1].Date = h.Days[0].Date },
		func(h *OfficialStatusHistory) { h.Days[0].Status = "new-state" },
	} {
		invalid := histories[0]
		invalid.Days = append([]OfficialStatusDay(nil), invalid.Days...)
		mutate(&invalid)
		require.Error(t, validateOfficialStatusHistory(invalid, now))
	}
	snapshot.Version = 1
	require.Error(t, ValidateOfficialStatusSnapshot(snapshot))
}

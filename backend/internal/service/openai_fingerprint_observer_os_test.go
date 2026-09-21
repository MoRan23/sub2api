package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFingerprintObservationOSRequiresActualSelectedUA(t *testing.T) {
	plan := &OpenAIOAuthIdentityPlan{OSFamily: "windows", OSSource: "user_agent"}
	plan.ClientIdentity.UserAgent = "codex-tui/0.155.1 (Windows 10.0.26200; x86_64) WindowsTerminal"
	entry := FingerprintObservationEntry{UserAgent: plan.ClientIdentity.UserAgent}
	populateFingerprintObservationOS(&entry, plan)
	require.Equal(t, "windows", entry.RoutingOSFamily)
	require.Equal(t, "user_agent", entry.RoutingOSSource)
	entry.UserAgent = "codex-tui/0.155.1 (Ubuntu 24.04.4; x86_64) xterm-256color"
	populateFingerprintObservationOS(&entry, plan)
	require.Empty(t, entry.RoutingOSFamily)
	require.Empty(t, entry.RoutingOSSource)
}

func TestFingerprintObservationDailyOSRequiresMatchingWireRoot(t *testing.T) {
	daily := OpenAIDailyRootObservation{Enabled: true, Kind: "sync", OSFamily: "macos", SessionID: "019a0000-0000-7000-8000-000000000001", BusinessDate: "2026-09-21"}
	entry := FingerprintObservationEntry{SessionID: daily.SessionID}
	populateFingerprintObservationDailyRoot(&entry, daily)
	require.True(t, entry.DailyFixedRootEnabled)
	require.Equal(t, "macos", entry.DailyFixedRootOSFamily)
	require.Equal(t, daily.SessionID, entry.DailyFixedRootSessionID)
	entry.SessionID = "019a0000-0000-7000-8000-000000000002"
	populateFingerprintObservationDailyRoot(&entry, daily)
	require.False(t, entry.DailyFixedRootEnabled)
	require.Empty(t, entry.DailyFixedRootOSFamily)
	require.Empty(t, entry.DailyFixedRootSessionID)
	require.Empty(t, entry.DailyFixedRootBusinessDate)
}

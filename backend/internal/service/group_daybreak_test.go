package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveGroupDaybreak(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		name, platform             string
		blue, red                  bool
		patch                      *GroupDaybreakUpdate
		wantBlue, wantRed, wantErr bool
	}{
		{"default", PlatformOpenAI, false, false, nil, false, false, false},
		{"omitted preserved", PlatformOpenAI, true, true, &GroupDaybreakUpdate{}, true, true, false},
		{"enable blue", PlatformOpenAI, false, false, &GroupDaybreakUpdate{Blue: &yes}, true, false, false},
		{"enable both", PlatformComposite, false, false, &GroupDaybreakUpdate{Blue: &yes, Red: &yes}, true, true, false},
		{"red without blue rejected", PlatformOpenAI, false, false, &GroupDaybreakUpdate{Red: &yes}, false, false, true},
		{"disable blue clears red", PlatformOpenAI, true, true, &GroupDaybreakUpdate{Blue: &no}, false, false, false},
		{"explicit disable dominates", PlatformOpenAI, true, true, &GroupDaybreakUpdate{Blue: &no, Red: &yes}, false, false, false},
		{"disable red", PlatformOpenAI, true, true, &GroupDaybreakUpdate{Red: &no}, true, false, false},
		{"platform reset", PlatformAnthropic, true, true, nil, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			blue, red, err := ResolveGroupDaybreak(tc.platform, tc.blue, tc.red, tc.patch)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantBlue, blue)
			require.Equal(t, tc.wantRed, red)
		})
	}
}

func TestGroupDaybreakDuplicateStartsDisabled(t *testing.T) {
	source := &Group{Name: "source", Platform: PlatformOpenAI, OpenAIDaybreakBlueEnabled: true, OpenAIDaybreakRedEnabled: true}
	copy := cloneGroupForDuplicate(source, "operation")
	require.False(t, copy.OpenAIDaybreakBlueEnabled)
	require.False(t, copy.OpenAIDaybreakRedEnabled)
	require.Nil(t, copy.OpenAIDaybreakUpdate)
	require.True(t, source.OpenAIDaybreakRedEnabled)
}

func TestAPIKeyAuthSnapshotGroupDaybreakRoundtrip(t *testing.T) {
	id := int64(15)
	apiKey := &APIKey{ID: 7, UserID: 3, GroupID: &id, Key: "test-daybreak", Status: StatusActive, User: &User{ID: 3, Status: StatusActive}, Group: &Group{ID: id, Platform: PlatformComposite, Status: StatusActive, Hydrated: true, OpenAIDaybreakBlueEnabled: true, OpenAIDaybreakRedEnabled: true}}
	svc := &APIKeyService{}
	payload, err := json.Marshal(&APIKeyAuthCacheEntry{Snapshot: svc.snapshotFromAPIKey(context.Background(), apiKey)})
	require.NoError(t, err)
	var entry APIKeyAuthCacheEntry
	require.NoError(t, json.Unmarshal(payload, &entry))
	got, used, err := svc.applyAuthCacheEntry(apiKey.Key, &entry)
	require.NoError(t, err)
	require.True(t, used)
	require.True(t, got.Group.Hydrated)
	require.True(t, got.Group.OpenAIDaybreakBlueEnabled)
	require.True(t, got.Group.OpenAIDaybreakRedEnabled)
	entry.Snapshot.Version = 24
	_, used, err = svc.applyAuthCacheEntry(apiKey.Key, &entry)
	require.NoError(t, err)
	require.False(t, used)
}

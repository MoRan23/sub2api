package service

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	fingerprintSnapshotSessionA = "018f5c3c-6e3a-7abc-8def-1234567890ab"
	fingerprintSnapshotThreadA  = "018f5c3c-6e3a-7abe-8def-1234567890ac"
)

func TestFingerprintObservationSnapshotPaginatesUsersAndLoadsHierarchy(t *testing.T) {
	base := time.Date(2026, time.August, 9, 10, 0, 0, 0, time.UTC)
	entries := []FingerprintObservationEntry{
		// Snapshots receive the ring's newest-first ordering.
		{SequenceID: 8, Timestamp: base.Add(8 * time.Minute), UserID: 2, Username: "bob", APIKeyID: 20, APIKeyName: "bob-key"},
		{SequenceID: 7, Timestamp: base.Add(7 * time.Minute), UserID: 1, Username: "alice", Email: "alice@example.com", APIKeyID: 11, APIKeyName: "mobile", SessionID: fingerprintSnapshotSessionA, ThreadID: fingerprintSnapshotSessionA},
		{SequenceID: 6, Timestamp: base.Add(6 * time.Minute), UserID: 1, Username: "alice", Email: "alice@example.com", APIKeyID: 10, APIKeyName: "desktop"},
		{SequenceID: 5, Timestamp: base.Add(5 * time.Minute), UserID: 1, Username: "alice", Email: "alice@example.com", APIKeyID: 10, APIKeyName: "desktop"},
		{SequenceID: 4, Timestamp: base.Add(4 * time.Minute), UserID: 1, Username: "alice", Email: "alice@example.com", APIKeyID: 10, APIKeyName: "desktop", SessionID: fingerprintSnapshotSessionA},
		{SequenceID: 3, Timestamp: base.Add(3 * time.Minute), UserID: 1, Username: "alice", Email: "alice@example.com", APIKeyID: 10, APIKeyName: "desktop", SessionID: fingerprintSnapshotSessionA, ThreadID: fingerprintSnapshotThreadA, ParentThreadID: fingerprintSnapshotSessionA},
		{SequenceID: 2, Timestamp: base.Add(2 * time.Minute), UserID: 1, Username: "alice", Email: "alice@example.com", APIKeyID: 10, APIKeyName: "desktop", SessionID: fingerprintSnapshotSessionA, ThreadID: fingerprintSnapshotThreadA, ParentThreadID: fingerprintSnapshotSessionA},
		{SequenceID: 1, Timestamp: base.Add(time.Minute), UserID: 1, Username: "alice", Email: "alice@example.com", APIKeyID: 10, APIKeyName: "desktop", SessionID: fingerprintSnapshotSessionA, ThreadID: fingerprintSnapshotSessionA},
	}
	store := newFingerprintObservationSnapshotStore(5*time.Minute, 32, time.Now, nil)
	token, err := store.create(entries)
	require.NoError(t, err)
	require.True(t, fingerprintObservationSnapshotIsOpaque(token))

	first, err := store.pageUsers(token, 1, 1)
	require.NoError(t, err)
	require.Equal(t, token, first.SnapshotToken)
	require.Equal(t, 2, first.Total)
	require.Equal(t, 2, first.Pages)
	require.Len(t, first.Items, 1)
	require.Equal(t, int64(2), first.Items[0].UserID)
	require.Equal(t, 1, first.Items[0].ObservationCount)

	second, err := store.pageUsers(token, 2, 1)
	require.NoError(t, err)
	require.Len(t, second.Items, 1)
	alice := second.Items[0]
	require.Equal(t, int64(1), alice.UserID)
	require.Equal(t, 7, alice.ObservationCount, "one user must not be split across top-level pages")
	require.Equal(t, 2, alice.APIKeyCount)
	require.Equal(t, 2, alice.SessionCount, "synthetic unattributed branches are not counted as real sessions")
	require.Equal(t, 3, alice.ThreadCount, "synthetic unthreaded branches are not counted as real threads")
	require.Equal(t, 2, alice.UnattributedObservationCount)
	require.Equal(t, base.Add(time.Minute), alice.FirstObservedAt)
	require.Equal(t, base.Add(7*time.Minute), alice.LastObservedAt)

	keyPage1, err := store.listAPIKeys(token, alice.NodeID, "", 1)
	require.NoError(t, err)
	require.Equal(t, 2, keyPage1.Total)
	require.Len(t, keyPage1.Items, 1)
	require.NotEmpty(t, keyPage1.NextCursor)
	require.Equal(t, int64(11), keyPage1.Items[0].APIKeyID, "children retain newest-first order")
	keyPage2, err := store.listAPIKeys(token, alice.NodeID, keyPage1.NextCursor, 1)
	require.NoError(t, err)
	require.Len(t, keyPage2.Items, 1)
	require.Empty(t, keyPage2.NextCursor)
	desktop := keyPage2.Items[0]
	require.Equal(t, int64(10), desktop.APIKeyID)
	require.Equal(t, 6, desktop.ObservationCount)
	require.Equal(t, 1, desktop.SessionCount)
	require.Equal(t, 2, desktop.ThreadCount)
	require.Equal(t, 2, desktop.UnattributedObservationCount)

	sessions, err := store.listSessions(token, desktop.NodeID, "", 20)
	require.NoError(t, err)
	require.Equal(t, 3, sessions.Total)
	require.Len(t, sessions.Items, 3)
	for _, unattributed := range sessions.Items[:2] {
		require.True(t, unattributed.Unattributed)
		require.Empty(t, unattributed.SessionID, "synthetic sessions must not masquerade as UUIDs")
		require.Equal(t, 1, unattributed.ObservationCount, "every empty-session row must remain an independent branch")
		require.Equal(t, 0, unattributed.ThreadCount)
		require.True(t, unattributed.HasUnthreaded)
		require.Equal(t, 1, unattributed.UnthreadedObservationCount)
	}
	require.NotEqual(t, sessions.Items[0].NodeID, sessions.Items[1].NodeID)

	realSession := sessions.Items[2]
	require.False(t, realSession.Unattributed)
	require.Equal(t, fingerprintSnapshotSessionA, realSession.SessionID)
	require.Equal(t, 4, realSession.ObservationCount)
	require.Equal(t, 2, realSession.ThreadCount)
	require.Equal(t, 1, realSession.ChildThreadCount)
	require.True(t, realSession.HasRootThread)
	require.True(t, realSession.HasUnthreaded)

	threads, err := store.listThreads(token, realSession.NodeID, "", 20)
	require.NoError(t, err)
	require.Len(t, threads.Items, 3)
	require.Equal(t, FingerprintObservationThreadRelationRoot, threads.Items[0].Relation, "root is projected first even if it is older")
	require.Equal(t, fingerprintSnapshotThreadA, threads.Items[1].ThreadID)
	require.Equal(t, FingerprintObservationThreadRelationDescendant, threads.Items[1].Relation)
	require.Equal(t, fingerprintSnapshotSessionA, threads.Items[1].ParentThreadID)
	require.True(t, threads.Items[2].Unthreaded)
	require.Empty(t, threads.Items[2].ThreadID)
	require.Equal(t, FingerprintObservationThreadRelationUnthreaded, threads.Items[2].Relation)

	entryPage1, err := store.listEntries(token, threads.Items[1].NodeID, "", 1)
	require.NoError(t, err)
	require.Equal(t, 2, entryPage1.Total)
	require.Equal(t, uint64(3), entryPage1.Items[0].SequenceID)
	require.NotEmpty(t, entryPage1.NextCursor)
	entryPage2, err := store.listEntries(token, threads.Items[1].NodeID, entryPage1.NextCursor, 1)
	require.NoError(t, err)
	require.Equal(t, uint64(2), entryPage2.Items[0].SequenceID)
	require.Empty(t, entryPage2.NextCursor)
}

func TestFingerprintObservationSnapshotCursorIsScopedAndValidated(t *testing.T) {
	base := time.Now()
	entries := []FingerprintObservationEntry{
		{SequenceID: 4, Timestamp: base, UserID: 2, APIKeyID: 4},
		{SequenceID: 3, Timestamp: base.Add(-time.Second), UserID: 2, APIKeyID: 3},
		{SequenceID: 2, Timestamp: base.Add(-2 * time.Second), UserID: 1, APIKeyID: 2},
		{SequenceID: 1, Timestamp: base.Add(-3 * time.Second), UserID: 1, APIKeyID: 1},
	}
	store := newFingerprintObservationSnapshotStore(time.Minute, 32, time.Now, nil)
	token, err := store.create(entries)
	require.NoError(t, err)
	users, err := store.pageUsers(token, 1, 20)
	require.NoError(t, err)
	require.Len(t, users.Items, 2)
	user2 := users.Items[0]
	user1 := users.Items[1]
	keys, err := store.listAPIKeys(token, user1.NodeID, "", 1)
	require.NoError(t, err)
	require.NotEmpty(t, keys.NextCursor)

	_, err = store.listAPIKeys(token, user1.NodeID, "modified"+keys.NextCursor, 1)
	require.ErrorIs(t, err, ErrFingerprintObservationCursorInvalid)
	_, err = store.listSessions(token, keys.Items[0].NodeID, keys.NextCursor, 1)
	require.ErrorIs(t, err, ErrFingerprintObservationCursorInvalid, "a cursor cannot be reused at another level")
	_, err = store.listAPIKeys(token, user2.NodeID, keys.NextCursor, 1)
	require.ErrorIs(t, err, ErrFingerprintObservationCursorInvalid, "a cursor cannot be reused for another parent at the same level")
	_, err = store.listAPIKeys(token, "usr_unknown", "", 1)
	require.ErrorIs(t, err, ErrFingerprintObservationNodeNotFound)

	secondToken, err := store.create(entries)
	require.NoError(t, err)
	secondUsers, err := store.pageUsers(secondToken, 1, 20)
	require.NoError(t, err)
	secondUser1 := secondUsers.Items[1]
	_, err = store.listAPIKeys(secondToken, secondUser1.NodeID, keys.NextCursor, 1)
	require.ErrorIs(t, err, ErrFingerprintObservationCursorInvalid, "a cursor cannot be reused in another snapshot")
	_, err = store.listAPIKeys(secondToken, user1.NodeID, "", 1)
	require.ErrorIs(t, err, ErrFingerprintObservationNodeNotFound, "a node ID cannot be reused in another snapshot")
}

func TestFingerprintObservationSnapshotIsImmutable(t *testing.T) {
	base := time.Now()
	entries := []FingerprintObservationEntry{{
		SequenceID: 1, Timestamp: base, UserID: 1, Username: "before", APIKeyID: 2,
	}}
	store := newFingerprintObservationSnapshotStore(time.Minute, 32, time.Now, nil)
	token, err := store.create(entries)
	require.NoError(t, err)
	entries[0].Username = "mutated-after-create"
	entries = append(entries, FingerprintObservationEntry{SequenceID: 2, Timestamp: base.Add(time.Second), UserID: 3})

	page, err := store.pageUsers(token, 1, 20)
	require.NoError(t, err)
	require.Equal(t, 1, page.Total)
	require.Equal(t, "before", page.Items[0].Username)
	require.Equal(t, 1, page.Items[0].ObservationCount)
}

func TestFingerprintObservationSnapshotUsesLegacyFallbackForMissingActorIDs(t *testing.T) {
	base := time.Now()
	entries := []FingerprintObservationEntry{
		{SequenceID: 5, Timestamp: base, Username: "renamed", Email: " Alice@Example.com ", APIKeyName: " Desktop "},
		{SequenceID: 4, Timestamp: base.Add(-time.Second), Username: "alice", Email: "alice@example.com", APIKeyName: "desktop"},
		{SequenceID: 3, Timestamp: base.Add(-2 * time.Second), Username: "bob", APIKeyName: "desktop"},
		{SequenceID: 2, Timestamp: base.Add(-3 * time.Second), Username: " BOB ", APIKeyName: "mobile"},
		{SequenceID: 1, Timestamp: base.Add(-4 * time.Second), Username: "carol", APIKeyName: "desktop"},
	}
	store := newFingerprintObservationSnapshotStore(time.Minute, 32, time.Now, nil)
	token, err := store.create(entries)
	require.NoError(t, err)
	page, err := store.pageUsers(token, 1, 20)
	require.NoError(t, err)
	require.Equal(t, 3, page.Total, "missing numeric IDs must not collapse unrelated users")
	require.Equal(t, 2, page.Items[0].ObservationCount, "email is the primary normalized fallback")
	require.Equal(t, 2, page.Items[1].ObservationCount, "username is used when email is unavailable")

	keys, err := store.listAPIKeys(token, page.Items[1].NodeID, "", 20)
	require.NoError(t, err)
	require.Equal(t, 2, keys.Total, "missing API key IDs are separated by normalized key name")
}

func TestFingerprintObservationSnapshotExpiresEvictsAndScrubs(t *testing.T) {
	now := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	store := newFingerprintObservationSnapshotStore(time.Minute, 2, clock, nil)
	token, err := store.create([]FingerprintObservationEntry{{Timestamp: now, Username: "sensitive"}})
	require.NoError(t, err)
	retained := store.snapshots[token]
	require.NotNil(t, retained)

	now = now.Add(time.Minute)
	_, err = store.pageUsers(token, 1, 20)
	require.ErrorIs(t, err, ErrFingerprintObservationSnapshotNotFound)
	require.Nil(t, retained.entries)
	require.Nil(t, retained.users)
	require.Nil(t, retained.usersByID)
	require.Equal(t, [32]byte{}, retained.cursorKey)

	now = now.Add(time.Second)
	first, err := store.create(nil)
	require.NoError(t, err)
	second, err := store.create(nil)
	require.NoError(t, err)
	third, err := store.create(nil)
	require.NoError(t, err)
	_, err = store.pageUsers(first, 1, 20)
	require.ErrorIs(t, err, ErrFingerprintObservationSnapshotNotFound, "oldest snapshot must be evicted at the bound")
	_, err = store.pageUsers(second, 1, 20)
	require.NoError(t, err)
	_, err = store.pageUsers(third, 1, 20)
	require.NoError(t, err)
}

func TestFingerprintObservationSnapshotActivelyScrubsAtTTL(t *testing.T) {
	store := newFingerprintObservationSnapshotStore(25*time.Millisecond, 2, time.Now, nil)
	store.scheduleExpiry = true
	t.Cleanup(store.clear)
	token, err := store.create([]FingerprintObservationEntry{{Timestamp: time.Now(), Username: "ttl-sensitive"}})
	require.NoError(t, err)
	store.mu.Lock()
	retained := store.snapshots[token]
	store.mu.Unlock()
	require.NotNil(t, retained)

	require.Eventually(t, func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		_, exists := store.snapshots[token]
		return !exists && retained.entries == nil && retained.users == nil && retained.cursorKey == ([32]byte{})
	}, 2*time.Second, 10*time.Millisecond, "TTL cleanup must not wait for another admin request")
}

func TestDisablingFingerprintObservationClearsSnapshotsAndPreventsCreateRace(t *testing.T) {
	SetFingerprintObservationEnabled(false)
	SetFingerprintObservationEnabled(true)
	t.Cleanup(func() { SetFingerprintObservationEnabled(false) })
	globalFingerprintObserver.record(FingerprintObservationEntry{
		Timestamp: time.Now(), UserID: 1, Username: "must-be-scrubbed", APIKeyID: 2,
	})
	token, err := CreateFingerprintObservationSnapshot()
	require.NoError(t, err)
	globalFingerprintObservationSnapshotStore.mu.Lock()
	retained := globalFingerprintObservationSnapshotStore.snapshots[token]
	globalFingerprintObservationSnapshotStore.mu.Unlock()
	require.NotNil(t, retained)

	SetFingerprintObservationEnabled(false)
	_, err = PageFingerprintObservationUsers(token, 1, 20)
	require.True(t, errors.Is(err, ErrFingerprintObservationSnapshotNotFound))
	require.Nil(t, retained.entries)
	require.Nil(t, retained.users)
	require.Nil(t, retained.usersByID)
	require.Equal(t, [32]byte{}, retained.cursorKey)

	_, err = CreateFingerprintObservationSnapshot()
	require.ErrorIs(t, err, ErrFingerprintObservationSnapshotNotFound, "disabled observation cannot install an empty late snapshot")

	for i := 0; i < 20; i++ {
		SetFingerprintObservationEnabled(true)
		globalFingerprintObserver.record(FingerprintObservationEntry{Timestamp: time.Now(), Username: "race-sensitive"})
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, _ = CreateFingerprintObservationSnapshot()
		}()
		go func() {
			defer wg.Done()
			<-start
			SetFingerprintObservationEnabled(false)
		}()
		close(start)
		wg.Wait()
		globalFingerprintObservationSnapshotStore.mu.Lock()
		remainingSnapshots := len(globalFingerprintObservationSnapshotStore.snapshots)
		globalFingerprintObservationSnapshotStore.mu.Unlock()
		require.Zero(t, remainingSnapshots)
	}
}

func TestFingerprintObservationSnapshotNormalizesPagination(t *testing.T) {
	store := newFingerprintObservationSnapshotStore(time.Minute, 32, time.Now, nil)
	token, err := store.create(nil)
	require.NoError(t, err)
	page, err := store.pageUsers(token, 0, 1000)
	require.NoError(t, err)
	require.Equal(t, 1, page.Page)
	require.Equal(t, 100, page.PageSize)
	require.Equal(t, 1, page.Pages)
	require.NotNil(t, page.Items)
}

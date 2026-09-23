package service

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func pluginOAuthTestScope() PluginAccountScope {
	return newPluginAccountScope(pluginAccountScopeEntry{Platform: PlatformOpenAI, AccountType: AccountTypeOAuth})
}

func listPluginAccountIDsForTest(gateway *OpenAIGatewayService, ctx context.Context, platform, accountType string) ([]int64, error) {
	infos, err := gateway.ListPluginAccounts(ctx, pluginOAuthTestScope(), platform, accountType)
	ids := make([]int64, 0, len(infos))
	for _, info := range infos {
		ids = append(ids, info.ID)
	}
	return ids, err
}

type pluginAccountDirectoryRepository struct {
	AccountRepository
	accounts []Account
	err      error
	lists    int
}

func (r *pluginAccountDirectoryRepository) ListByPlatform(context.Context, string) ([]Account, error) {
	r.lists++
	return r.accounts, r.err
}

func (r *pluginAccountDirectoryRepository) GetByID(_ context.Context, id int64) (*Account, error) {
	if r.err != nil {
		return nil, r.err
	}
	for i := range r.accounts {
		if r.accounts[i].ID == id {
			return &r.accounts[i], nil
		}
	}
	return nil, nil
}

func TestOpenAIPluginAccountDirectoryUsesSameScopeForListAndResolve(t *testing.T) {
	parentID := int64(1)
	repo := &pluginAccountDirectoryRepository{accounts: []Account{
		{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Credentials: map[string]any{"access_token": "local-test-token"}},
		{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: "disabled"},
		{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, ParentAccountID: &parentID},
		{ID: 4, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive},
		{ID: 5, Platform: PlatformOpenAI, Type: AccountTypeSetupToken, Status: StatusActive},
		{ID: 6, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive},
	}}
	gateway := &OpenAIGatewayService{accountRepo: repo}
	ctx := context.Background()
	for _, filters := range [][2]string{{"", ""}, {" openai ", " oauth "}} {
		ids, err := listPluginAccountIDsForTest(gateway, ctx, filters[0], filters[1])
		require.NoError(t, err)
		require.Equal(t, []int64{1}, ids)
	}
	identity, err := gateway.ResolvePluginOutboundIdentity(ctx, pluginOAuthTestScope(), 1)
	require.NoError(t, err)
	require.NotNil(t, identity)
	require.Equal(t, "local-test-token", identity.Token)
	for _, id := range []int64{-1, 0, 2, 3, 4, 5, 6, 999} {
		identity, err := gateway.ResolvePluginOutboundIdentity(ctx, pluginOAuthTestScope(), id)
		require.NoError(t, err, "out-of-scope accounts must be rejected before token resolution")
		require.Nil(t, identity)
	}
	// A previously enumerated account can be disabled before credential lookup.
	repo.accounts[0].Status = "disabled"
	identity, err = gateway.ResolvePluginOutboundIdentity(ctx, pluginOAuthTestScope(), 1)
	require.NoError(t, err)
	require.Nil(t, identity)
	listCalls := repo.lists
	for _, filters := range [][2]string{{"anthropic", "oauth"}, {"openai", "apikey"}, {"openai", "setup-token"}} {
		ids, err := listPluginAccountIDsForTest(gateway, ctx, filters[0], filters[1])
		require.NoError(t, err)
		require.Empty(t, ids)
	}
	require.Equal(t, listCalls, repo.lists, "unsupported filters must not enumerate accounts")
}

func TestOpenAIPluginAccountDirectoryPreservesAccountIdentityWithoutTurnState(t *testing.T) {
	codexCanonicalUAMu.RLock()
	oldResolver := codexCanonicalUAResolver
	codexCanonicalUAMu.RUnlock()
	SetCodexCanonicalUserAgentResolver(func() string { return "codex-tui/0.200.1 (Ubuntu 22.4.0; x86_64) xterm-256color (codex-tui; 0.200.1)" })
	t.Cleanup(func() { SetCodexCanonicalUserAgentResolver(oldResolver) })
	proxy := &Proxy{ID: 9, Protocol: "http", Host: "proxy.invalid", Port: 8080}
	repo := &pluginAccountDirectoryRepository{accounts: []Account{
		{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, ProxyID: &proxy.ID, Proxy: proxy,
			Credentials: map[string]any{"access_token": "account-one-token", "chatgpt_account_id": "org-one", "chatgpt_account_is_fedramp": true,
				"user_agent": "codex_cli_rs/0.144.1 (Windows 11.0.26100; x86_64) WindowsTerminal"},
			Extra: map[string]any{"codex_turn_state": map[string]any{"enabled": true}, "codex_turn_state_generation": "test-generation"}},
		{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
			Credentials: map[string]any{"access_token": "account-two-token", "chatgpt_account_id": "org-two", "user_agent": "codex-tui/0.144.1 (Mac OS X 15.1.0; arm64) Terminal"}},
	}}
	// No turn-state store, identity resolver, or transport is installed. A passive
	// identity lookup must not require them or allocate any request/turn identity.
	gateway := &OpenAIGatewayService{accountRepo: repo}
	for i := range repo.accounts {
		account := &repo.accounts[i]
		identity, err := gateway.ResolvePluginOutboundIdentity(context.Background(), pluginOAuthTestScope(), account.ID)
		require.NoError(t, err)
		want := resolveCodexClientIdentityPlan(CodexClientIdentityNormalize, account.GetOpenAIUserAgent())
		require.Equal(t, want.UserAgent, identity.Headers.Get("User-Agent"))
		require.Equal(t, want.Version, identity.Headers.Get("Version"))
		require.Equal(t, want.Originator, identity.Headers.Get("Originator"))
		require.Equal(t, account.GetChatGPTAccountID(), identity.Headers.Get("ChatGPT-Account-Id"))
		require.Empty(t, identity.Headers.Get("x-codex-turn-state"))
		require.Empty(t, identity.Headers.Get("session_id"))
		require.Empty(t, identity.Headers.Get("thread_id"))
		if account.ID == 1 {
			require.Contains(t, identity.Headers.Get("User-Agent"), "Windows")
			require.Equal(t, "true", identity.Headers.Get("x-openai-fedramp"))
			require.Equal(t, proxy.URL(), identity.ProxyURL)
		} else {
			require.Contains(t, identity.Headers.Get("User-Agent"), "Mac OS X")
			require.Empty(t, identity.Headers.Get("x-openai-fedramp"))
			require.Empty(t, identity.ProxyURL)
		}
	}
}

func TestOpenAIPluginAccountDirectoryPropagatesRepositoryFailure(t *testing.T) {
	want := errors.New("directory lookup failed")
	gateway := &OpenAIGatewayService{accountRepo: &pluginAccountDirectoryRepository{err: want}}
	_, err := listPluginAccountIDsForTest(gateway, context.Background(), "", "")
	require.ErrorIs(t, err, want)
	_, err = gateway.ResolvePluginOutboundIdentity(context.Background(), pluginOAuthTestScope(), 1)
	require.ErrorIs(t, err, want)
}

func TestOpenAIPluginAccountDirectoryDoesNotRequireCredentials(t *testing.T) {
	account := Account{ID: 71, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive}
	repo := &pluginAccountDirectoryRepository{accounts: []Account{account}}
	gateway := &OpenAIGatewayService{accountRepo: repo}
	for _, family := range []string{"", OpenAIOSWindows, OpenAIOSLinux, OpenAIOSMacOS} {
		ctx := ContextWithOpenAIRequestOS(context.Background(), OpenAIRequestOS{Family: family})
		ids, err := listPluginAccountIDsForTest(gateway, ctx, "", "")
		require.NoError(t, err)
		require.Equal(t, []int64{account.ID}, ids)
	}
}

func TestOpenAIPluginAccountDirectoryUsesSharedCredentialsForEveryOSIdentity(t *testing.T) {
	account := Account{ID: 72, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
		Credentials: map[string]any{"access_token": "shared-access", "chatgpt_account_id": "plugin-owner"}}
	profiles, err := BuildOpenAIOAuthOSProfiles(&account, nil)
	require.NoError(t, err)
	profiles.Authorization = &OpenAIOAuthOSAuthorizationSummary{Status: OpenAIOAuthAuthorizationUnauthorized}
	account.OpenAIOAuthOSProfiles = profiles
	repo := &pluginAccountDirectoryRepository{accounts: []Account{account}}
	gateway := &OpenAIGatewayService{accountRepo: repo}
	for _, family := range []string{"", OpenAIOSWindows, OpenAIOSLinux, OpenAIOSMacOS} {
		ctx := ContextWithOpenAIRequestOS(context.Background(), OpenAIRequestOS{Family: family})
		ids, err := listPluginAccountIDsForTest(gateway, ctx, "", "")
		require.NoError(t, err)
		require.Equal(t, []int64{account.ID}, ids)
		identity, err := gateway.ResolvePluginOutboundIdentity(ctx, pluginOAuthTestScope(), account.ID)
		require.NoError(t, err)
		require.NotNil(t, identity)
		require.Equal(t, "shared-access", identity.Token)
		// This auxiliary lookup has no business request; it always uses the
		// account default identity while all systems share the same credential.
		profile := profiles.Profiles[profiles.DefaultOS]
		require.Equal(t, resolveCodexClientIdentityPlan(CodexClientIdentityNormalize, profile.UserAgent).UserAgent, identity.Headers.Get("User-Agent"))
	}
	require.Empty(t, repo.accounts[0].OpenAIOAuthCredentialOS, "lookup must not scope the shared account")
	require.Equal(t, "shared-access", repo.accounts[0].GetOpenAIAccessToken())
}

// pluginDirRepoStub embeds AccountRepository so it satisfies the full interface;
// only ListByPlatform is implemented (the sole method the directory listing uses).
// Any other call panics, which keeps the test honest about the surface it touches.
type pluginDirRepoStub struct {
	AccountRepository
	byPlatform map[string][]Account
}

func (r *pluginDirRepoStub) ListByPlatform(_ context.Context, platform string) ([]Account, error) {
	return r.byPlatform[platform], nil
}

func TestListPluginAccounts_ScopeAndSchedulable(t *testing.T) {
	future := time.Now().Add(time.Hour)
	parentID := int64(1)
	openai := []Account{
		// schedulable active oauth. Credentials hold secrets (stripped); Extra is
		// released.
		{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true,
			Name:        "primary",
			Credentials: map[string]any{"access_token": "SECRET-TOKEN", "refresh_token": "SECRET-REFRESH"},
			Extra:       map[string]any{"existing_key": "ek-value", "openai_compact_mode": "auto"}},
		// active but temp-unschedulable (paused) — status stays active, must still be
		// returned, but not schedulable
		{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true,
			TempUnschedulableUntil: &future, TempUnschedulableReason: "429 from upstream"},
		// active but rate-limited (429) — status stays active, returned, not schedulable
		{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true,
			RateLimitResetAt: &future},
		// shadow oauth — excluded entirely
		{ID: 4, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true,
			ParentAccountID: &parentID},
		// apikey type — out of (openai, oauth) scope, excluded
		{ID: 5, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true},
	}
	svc := &OpenAIGatewayService{accountRepo: &pluginDirRepoStub{byPlatform: map[string][]Account{PlatformOpenAI: openai}}}
	scope := newPluginAccountScope(pluginAccountScopeEntry{Platform: PlatformOpenAI, AccountType: AccountTypeOAuth})

	infos, err := svc.ListPluginAccounts(context.Background(), scope, "", "")
	require.NoError(t, err)

	got := map[int64]PluginAccountInfo{}
	for _, info := range infos {
		got[info.ID] = info
	}
	// 1,2,3 in scope; 4 shadow and 5 apikey excluded.
	require.Len(t, infos, 3)
	assert.Contains(t, got, int64(1))
	assert.Contains(t, got, int64(2))
	assert.Contains(t, got, int64(3))
	assert.NotContains(t, got, int64(4), "shadow account must be excluded")
	assert.NotContains(t, got, int64(5), "out-of-scope account type must be excluded")

	// Host-authoritative schedulable decision.
	assert.True(t, got[1].Schedulable, "active oauth is schedulable")
	assert.False(t, got[2].Schedulable, "temp-unschedulable account is not schedulable")
	assert.False(t, got[3].Schedulable, "rate-limited account is not schedulable")

	// Metadata carries readable info (incl. Extra) but NEVER the raw Credentials blob.
	meta := string(got[1].MetadataJSON)
	assert.NotContains(t, meta, "SECRET-TOKEN", "credentials must never appear in metadata")
	assert.NotContains(t, meta, "SECRET-REFRESH", "credentials must never appear in metadata")
	assert.Contains(t, meta, "ek-value", "Extra is intentionally released")
	assert.Contains(t, meta, "openai_compact_mode", "Extra is intentionally released")
	assert.Contains(t, meta, "primary", "readable name must be present in metadata")
	assert.Contains(t, string(got[2].MetadataJSON), "429 from upstream", "pause reason must be readable in metadata")
}

// TestAccountReadableSnapshot_DenylistTripwire fails whenever a new EXPORTED field
// is added to Account without being classified as either safe-to-expose or
// stripped by accountReadableSnapshotJSON. Because the snapshot is a denylist, a
// newly added secret-bearing field would otherwise silently ship to plugins. When
// this test fails: add the field to `stripped` (and zero it in
// accountReadableSnapshotJSON) if it can hold secrets/heavy data, otherwise add it
// to `safeToExpose`.
func TestAccountReadableSnapshot_DenylistTripwire(t *testing.T) {
	// Fields the snapshot intentionally strips. Credentials = long-lived secret
	// (refresh_token) not handed out by ResolveOutboundIdentity. Groups/AccountGroups
	// = relational graphs with back-references that would cycle under encoding/json.
	stripped := map[string]struct{}{
		"Credentials": {}, "Groups": {}, "AccountGroups": {},
		"OpenAIOAuthCredentialOS": {}, "OpenAIOAuthCredentialOwnerID": {},
		"OpenAIOAuthAuthorizationGeneration": {}, "OpenAIOAuthCredentialRevision": {},
		"OpenAIOAuthCredentialStateGeneration": {}, "OpenAIOAuthCredentialEpoch": {},
		"OpenAIOAuthInitialOS": {}, "OpenAIOAuthInitialCredentials": {},
	}
	// Fields intentionally exposed as readable metadata (incl. Extra and Proxy —
	// the proxy password is already handed out via ResolveOutboundIdentity's URL).
	safeToExpose := map[string]struct{}{
		"ID": {}, "Name": {}, "Notes": {}, "Platform": {}, "Type": {}, "Extra": {},
		"Proxy": {}, "ProxyID": {}, "ProxyFallbackOriginID": {}, "ProxyFallbackOriginName": {},
		"Concurrency": {}, "Priority": {}, "RateMultiplier": {}, "LoadFactor": {},
		"Status": {}, "ErrorMessage": {}, "LastUsedAt": {}, "ExpiresAt": {},
		"AutoPauseOnExpired": {}, "CreatedAt": {}, "UpdatedAt": {}, "Schedulable": {},
		"RateLimitedAt": {}, "RateLimitResetAt": {}, "OverloadUntil": {},
		"TempUnschedulableUntil": {}, "TempUnschedulableReason": {},
		"SessionWindowStart": {}, "SessionWindowEnd": {}, "SessionWindowStatus": {},
		"ParentAccountID": {}, "QuotaDimension": {}, "GroupIDs": {},
		"OpenAIOAuthOSProfiles": {},
	}
	tp := reflect.TypeOf(Account{})
	for i := 0; i < tp.NumField(); i++ {
		f := tp.Field(i)
		if f.PkgPath != "" {
			continue // unexported: never marshaled by encoding/json
		}
		_, isStripped := stripped[f.Name]
		_, isSafe := safeToExpose[f.Name]
		if !isStripped && !isSafe {
			t.Fatalf("Account.%s is a new exported field not classified for the plugin snapshot: "+
				"add it to accountReadableSnapshotJSON's denylist (if it holds secrets or is a "+
				"cyclic/heavy relation) or to safeToExpose (if it is non-secret readable metadata)", f.Name)
		}
	}

	// The raw Credentials blob must never serialize; Extra and the proxy ARE released.
	acct := &Account{
		ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
		Credentials: map[string]any{"access_token": "AT", "refresh_token": "LEAK-REFRESH"},
		Extra:       map[string]any{"opaque": "extra-released"},
		Proxy:       &Proxy{Host: "host", Port: 1, Username: "user", Password: "pw-released"},
	}
	snap := accountReadableSnapshotJSON(acct)
	require.NotNil(t, snap)
	var m map[string]any
	require.NoError(t, json.Unmarshal(snap, &m))
	assert.NotContains(t, string(snap), "LEAK-REFRESH", "raw Credentials must never appear in metadata")
	assert.Contains(t, string(snap), "extra-released", "Extra is intentionally released")
	assert.Contains(t, string(snap), "pw-released", "proxy is intentionally released (already exposed via 打票)")

	// Cycle safety: a populated Groups/AccountGroups back-reference cycle must NOT
	// crash json.Marshal (encoding/json does not detect cycles). Stripping them
	// guarantees the snapshot still returns valid JSON instead of stack-overflowing.
	g := &Group{ID: 7, Name: "g7"}
	ag := AccountGroup{GroupID: 7, Group: g, Account: acct}
	g.AccountGroups = []AccountGroup{ag} // g -> ag -> g  (and ag -> acct -> ...)
	acct.Groups = []*Group{g}
	acct.AccountGroups = []AccountGroup{ag}
	cyc := accountReadableSnapshotJSON(acct)
	require.NotNil(t, cyc, "snapshot must survive a cyclic Groups/AccountGroups graph")
	require.NoError(t, json.Unmarshal(cyc, &m))

	// Shallow-copy safety: the source account must be untouched.
	assert.NotNil(t, acct.Credentials, "snapshot must not mutate the source account")
	assert.NotNil(t, acct.Proxy, "snapshot must not mutate the source account")
	assert.Len(t, acct.Groups, 1, "snapshot must not mutate the source account's Groups")
}

func TestListPluginAccounts_ExcludesNonActiveDefenseInDepth(t *testing.T) {
	// Even if a repo were to return a non-active account, the directory must not
	// expose it (defense-in-depth beyond ListByPlatform's DB filter).
	svc := &OpenAIGatewayService{accountRepo: &pluginDirRepoStub{byPlatform: map[string][]Account{
		PlatformOpenAI: {
			{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true},
			{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusDisabled},
			{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusError},
		},
	}}}
	scope := newPluginAccountScope(pluginAccountScopeEntry{Platform: PlatformOpenAI, AccountType: AccountTypeOAuth})
	infos, err := svc.ListPluginAccounts(context.Background(), scope, "", "")
	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.Equal(t, int64(1), infos[0].ID)
}

func TestListPluginAccounts_EmptyScopeReturnsNothing(t *testing.T) {
	svc := &OpenAIGatewayService{accountRepo: &pluginDirRepoStub{byPlatform: map[string][]Account{
		PlatformOpenAI: {{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true}},
	}}}
	infos, err := svc.ListPluginAccounts(context.Background(), PluginAccountScope{}, "", "")
	require.NoError(t, err)
	assert.Empty(t, infos, "an empty scope must never enumerate accounts")
}

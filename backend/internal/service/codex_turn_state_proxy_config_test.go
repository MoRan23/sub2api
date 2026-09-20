package service

import (
	"maps"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodexTurnStateCollectorProxyOnlyChanged(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Account, *Account)
		want   bool
	}{
		{name: "new proxy", want: true},
		{name: "remove proxy", mutate: func(a, b *Account) {
			a.Extra[CodexTurnStateExtraKey] = codexTurnStateConfigMap(CodexTurnStateConfig{Enabled: true, AccountType: "auto", CollectorProxyID: new(int64(1))})
			b.Extra[CodexTurnStateExtraKey] = codexTurnStateConfigMap(CodexTurnStateConfig{Enabled: true, AccountType: "auto"})
		}, want: true},
		{name: "same proxy", mutate: func(a, b *Account) { b.Extra[CodexTurnStateExtraKey] = a.Extra[CodexTurnStateExtraKey] }},
		{name: "disabled", mutate: func(_, b *Account) {
			b.Extra[CodexTurnStateExtraKey] = codexTurnStateConfigMap(CodexTurnStateConfig{AccountType: "auto", CollectorProxyID: new(int64(2))})
		}},
		{name: "manual classification changed", mutate: func(_, b *Account) {
			b.Extra[CodexTurnStateExtraKey] = codexTurnStateConfigMap(CodexTurnStateConfig{Enabled: true, AccountType: "personal", CollectorProxyID: new(int64(2))})
		}},
		{name: "resolved classification changed", mutate: func(_, b *Account) { b.Credentials["plan_type"] = "team" }},
		{name: "unknown classification", mutate: func(a, b *Account) { a.Credentials["plan_type"], b.Credentials["plan_type"] = "unknown", "unknown" }},
		{name: "credentials changed", mutate: func(_, b *Account) { b.Credentials["access_token"] = "replacement" }},
		{name: "credential epoch changed", mutate: func(_, b *Account) { b.Extra[CodexTurnStateCredentialEpochExtraKey] = "replacement" }},
		{name: "credential epoch missing", mutate: func(a, b *Account) {
			delete(a.Extra, CodexTurnStateCredentialEpochExtraKey)
			delete(b.Extra, CodexTurnStateCredentialEpochExtraKey)
		}},
		{name: "generation unchanged", mutate: func(a, b *Account) {
			b.Extra[CodexTurnStateGenerationExtraKey] = a.Extra[CodexTurnStateGenerationExtraKey]
		}},
		{name: "generation missing", mutate: func(_, b *Account) { delete(b.Extra, CodexTurnStateGenerationExtraKey) }},
		{name: "shadow", mutate: func(_, b *Account) { b.ParentAccountID = new(int64(42)) }},
		{name: "non oauth", mutate: func(_, b *Account) { b.Type = AccountTypeAPIKey }},
		{name: "different account", mutate: func(_, b *Account) { b.ID++ }},
	} {
		t.Run(test.name, func(t *testing.T) {
			current := codexConfigAccount(t)
			target := *current
			target.Credentials, target.Extra = maps.Clone(current.Credentials), maps.Clone(current.Extra)
			target.Extra[CodexTurnStateGenerationExtraKey] = "replacement-generation"
			target.Extra[CodexTurnStateExtraKey] = codexTurnStateConfigMap(CodexTurnStateConfig{Enabled: true, AccountType: "auto", CollectorProxyID: new(int64(2))})
			if test.mutate != nil {
				test.mutate(current, &target)
			}
			require.Equal(t, test.want, CodexTurnStateCollectorProxyOnlyChanged(current, &target))
		})
	}
}

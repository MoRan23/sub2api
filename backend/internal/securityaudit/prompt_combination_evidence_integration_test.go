package securityaudit

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPromptAuditCombinationEvidencePersistsOnlyInAdminDetails(t *testing.T) {
	db := openPromptAuditIntegrationDB(t)
	repo := NewPostgreSQLRepository(db)
	ctx := context.Background()
	result := combinationEvidenceResult()
	snapshot := integrationSnapshot("combination-evidence")
	snapshot.FullPrompt = "original full prompt stays unchanged"
	event, err := repo.RecordBlocking(ctx, snapshot, 9, result, false)
	require.NoError(t, err)
	require.Equal(t, result.combinationMatch, event.CombinationMatch)
	require.NotContains(t, event.ScannerEvidence, combinationMatchEvidenceKey)

	var stored string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT scanner_evidence->>$2::text FROM prompt_audit_events WHERE id=$1`, event.ID, combinationMatchEvidenceKey).Scan(&stored))
	require.Equal(t, result.combinationMatch, decodeCombinationMatchEvidence(stored))
	page, err := repo.ListEvents(ctx, EventFilter{RequestID: snapshot.RequestID}, 1, 20)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Nil(t, page.Items[0].CombinationMatch)
	require.NotContains(t, page.Items[0].ScannerEvidence, combinationMatchEvidenceKey)
	listJSON, err := json.Marshal(page.Items[0])
	require.NoError(t, err)
	require.NotContains(t, string(listJSON), result.combinationMatch.Topic.Rule)
	require.NotContains(t, string(listJSON), result.combinationMatch.Topic.Content)
	require.NotContains(t, string(listJSON), result.combinationMatch.PolicyName)
	require.NotContains(t, string(listJSON), `"combination_match"`)
	require.Empty(t, page.Items[0].IssueSummaries[0].Evidence)

	detail, err := repo.GetEvent(ctx, event.ID)
	require.NoError(t, err)
	require.Equal(t, result.combinationMatch, detail.CombinationMatch)
	require.Equal(t, "custom-policy_1", detail.CombinationMatch.PolicyID)
	require.Equal(t, "自定义主题策略", detail.CombinationMatch.PolicyName)
	require.Equal(t, snapshot.FullPrompt, detail.Snapshot.FullPrompt)
	require.Equal(t, result.ScannerEvidence["combination_topic"], detail.ScannerEvidence["combination_topic"])
	require.Equal(t, result.ScannerEvidence["combination_intent"], detail.ScannerEvidence["combination_intent"])
	require.Contains(t, detail.IssueSummaries[0].Evidence, "携带历史")
	require.Contains(t, detail.IssueSummaries[0].Evidence, "最新用户输入")

	for _, raw := range []string{"", "{}", "not json"} {
		_, err = db.ExecContext(ctx, `UPDATE prompt_audit_events SET scanner_evidence=jsonb_set(scanner_evidence, ARRAY[$2::text], to_jsonb($3::text)) WHERE id=$1`, event.ID, combinationMatchEvidenceKey, raw)
		require.NoError(t, err)
		detail, err = repo.GetEvent(ctx, event.ID)
		require.NoError(t, err)
		require.Nil(t, detail.CombinationMatch)
		require.NotContains(t, detail.ScannerEvidence, combinationMatchEvidenceKey)
		require.Empty(t, detail.IssueSummaries[0].Evidence)
	}
}

func TestPromptAuditCombinationEvidenceRejectsGuardReservedKey(t *testing.T) {
	db := openPromptAuditIntegrationDB(t)
	repo := NewPostgreSQLRepository(db)
	ctx := context.Background()
	result := combinationEvidenceResult()
	result.ScannerBackend = "qwen3guard-openai"
	raw, err := json.Marshal(result.combinationMatch)
	require.NoError(t, err)
	result.ScannerEvidence[combinationMatchEvidenceKey] = string(raw)
	event, err := repo.RecordBlocking(ctx, integrationSnapshot("combination-forged"), 1, result, false)
	require.NoError(t, err)
	require.Nil(t, event.CombinationMatch)
	var storedReservedKey bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT scanner_evidence ? $2 FROM prompt_audit_events WHERE id=$1`, event.ID, combinationMatchEvidenceKey).Scan(&storedReservedKey))
	require.False(t, storedReservedKey)

	_, err = db.ExecContext(ctx, `UPDATE prompt_audit_events SET scanner_evidence=scanner_evidence || jsonb_build_object($2::text,$3::text) WHERE id=$1`, event.ID, combinationMatchEvidenceKey, string(raw))
	require.NoError(t, err)
	detail, err := repo.GetEvent(ctx, event.ID)
	require.NoError(t, err)
	require.Nil(t, detail.CombinationMatch)
	require.NotContains(t, detail.ScannerEvidence, combinationMatchEvidenceKey)
	require.Empty(t, detail.IssueSummaries)

	// Labels alone cannot establish local provenance at write time.
	result.ScannerBackend = "local-combination"
	result.combinationMatch = nil
	spoofed, err := repo.RecordBlocking(ctx, integrationSnapshot("combination-spoofed-labels"), 1, result, false)
	require.NoError(t, err)
	require.Nil(t, spoofed.CombinationMatch)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT scanner_evidence ? $2 FROM prompt_audit_events WHERE id=$1`, spoofed.ID, combinationMatchEvidenceKey).Scan(&storedReservedKey))
	require.False(t, storedReservedKey)
	require.Len(t, spoofed.IssueSummaries, 1)
	require.Empty(t, spoofed.IssueSummaries[0].Evidence)
}

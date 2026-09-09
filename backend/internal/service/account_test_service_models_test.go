//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAppendOpenAIConfiguredTestModelsIncludesGroupAllowlist(t *testing.T) {
	account := &Account{
		Credentials: map[string]any{"model_mapping": map[string]any{"gpt-5": "gpt-5.5"}},
		Groups:      []*Group{{Platform: PlatformOpenAI, ModelAllowlist: GroupModelAllowlist{Enabled: true, Models: []string{"gpt-image-2.5", "gpt-5"}}}},
	}
	models := AppendOpenAIConfiguredTestModels(nil, account)
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	require.Equal(t, []string{"gpt-5", "gpt-image-2.5"}, ids)
}

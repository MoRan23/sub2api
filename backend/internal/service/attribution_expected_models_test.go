package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAttributionExpectedModelsConfiguration(t *testing.T) {
	c := DefaultAttributionConfig()
	c.Default.ExpectedModels = []string{" z ", "a", "z"}
	custom := []string{}
	c.NewAccountTests.AttributionExpectedModels = &custom
	got, err := NormalizeAttributionConfig(c)
	require.NoError(t, err)
	require.Equal(t, []string{"a", "z"}, got.Default.ExpectedModels)
	require.Equal(t, []string{" z ", "a", "z"}, c.Default.ExpectedModels)
	require.NotNil(t, got.NewAccountTests.AttributionExpectedModels)
	b, err := json.Marshal(got)
	require.NoError(t, err)
	require.Contains(t, string(b), `"attribution_expected_models":[]`)
	require.NoError(t, json.Unmarshal(b, &got))
	require.NotNil(t, got.NewAccountTests.AttributionExpectedModels)
	c.Groups = []AttributionGroupPolicy{{GroupID: 1, Enabled: true, AttributionPolicy: AttributionPolicy{Model: "probe", ExpectedModels: []string{"group"}}}}
	p, _ := ResolveAttributionPolicy(c, []AccountGroup{{GroupID: 1}})
	require.Equal(t, []string{"group"}, ExpectedAttributionModels(p))
	c.Groups[0].Enabled = false
	p, _ = ResolveAttributionPolicy(c, []AccountGroup{{GroupID: 1}})
	require.Equal(t, []string{" z ", "a", "z"}, p.ExpectedModels)
	require.Equal(t, []string{"round-probe"}, ExpectedAttributionModels(AttributionPolicy{Model: "round-probe"}))
	for _, models := range [][]string{{""}, {"gpt-*"}, {"two models"}, {strings.Repeat("x", 201)}, make([]string, 501)} {
		c := DefaultAttributionConfig()
		c.Default.ExpectedModels = models
		_, err = NormalizeAttributionConfig(c)
		require.ErrorIs(t, err, ErrAttributionInvalid)
		c.Default.ExpectedModels = nil
		c.NewAccountTests.AttributionExpectedModels = &models
		_, err = NormalizeAttributionConfig(c)
		require.ErrorIs(t, err, ErrAttributionInvalid)
		s := &ModelAttributionService{repo: &attributionTestRepo{}}
		_, err = s.Create(context.Background(), []int64{1}, "probe", &models)
		require.ErrorIs(t, err, ErrAttributionInvalid)
	}
}

func TestAttributionExpectedModelsBaseline(t *testing.T) {
	old := AttributionSnapshot{GroupID: 1, Policy: AttributionPolicy{Model: "probe"}, Detector: testAttributionDetector()}
	legacy := AttributionDigest([]any{old.GroupID, old.Policy, old.Detector})
	require.Equal(t, legacy, AttributionPolicyDigest(old))
	old.Policy.ExpectedModels = []string{"probe"}
	require.Equal(t, legacy, AttributionPolicyDigest(old))
	old.Policy.ExpectedModels = []string{"second", "first"}
	changed := AttributionPolicyDigest(old)
	require.NotEqual(t, legacy, changed)
	old.Policy.ExpectedModels = []string{"first", "second"}
	require.Equal(t, changed, AttributionPolicyDigest(old))
	a := validAttributionAnalysis()
	require.NoError(t, ValidateAttributionAnalysis(a, []string{AttributionDefaultModel, "gpt-6-luna"}, []string{AttributionDefaultModel, "gpt-6-luna"}))
	require.EqualError(t, ValidateAttributionAnalysis(a, []string{AttributionDefaultModel, "missing"}, []string{AttributionDefaultModel, "gpt-6-luna"}), "expected_model_not_enrolled")
	a.Probability = attrProbability(.5)
	for i := range a.Results {
		a.Results[i].Probability = attrProbability(.5)
	}
	require.EqualError(t, ValidateAttributionAnalysis(a, []string{AttributionDefaultModel, "gpt-6-luna"}, []string{AttributionDefaultModel, "gpt-6-luna"}), "ambiguous_prediction")
}

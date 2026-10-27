package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAttributionActionsNormalizeAndRoundTrip(t *testing.T) {
	c := DefaultAttributionConfig()
	rules := AttributionModelMappings{" alias* ": " actual-model "}
	groups := []int64{9, 2}
	c.Default.HighModelMapping, c.Default.HighGroupIDs = &rules, &groups
	empty := AttributionModelMappings{}
	c.Default.LowModelMapping = &empty
	got, err := NormalizeAttributionConfig(c)
	require.NoError(t, err)
	require.Equal(t, AttributionModelMappings{"alias*": "actual-model"}, *got.Default.HighModelMapping)
	require.Equal(t, []int64{9, 2}, *got.Default.HighGroupIDs)
	data, err := json.Marshal(got)
	require.NoError(t, err)
	require.Contains(t, string(data), `"low_model_mapping":{}`)
	require.NotContains(t, string(data), `"low_group_ids"`)
	var decoded AttributionConfig
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.NotNil(t, decoded.Default.LowModelMapping)
	require.Empty(t, *decoded.Default.LowModelMapping)
	require.Nil(t, decoded.Default.LowGroupIDs)
}

func TestAttributionActionsRejectInvalidMappingsAndGroups(t *testing.T) {
	for _, input := range []string{
		`{"high_model_mapping":{"same":"a","same":"b"}}`,
		`{"high_model_mapping":{"same":"a"," same ":"b"}}`,
		`{"high_model_mapping":{"a*b":"target"}}`,
		`{"high_model_mapping":{"a**":"target"}}`,
		`{"high_model_mapping":{"alias":"target*"}}`,
		`{"high_model_mapping":{"alias":""}}`,
		`{"high_model_mapping":{"alias":null}}`,
		`{"high_model_mapping":{"":"target"}}`,
		`{"high_model_mapping":[]}`,
		`{"high_group_ids":[]}`,
		`{"high_group_ids":[1,1]}`,
		`{"low_group_ids":[0]}`,
	} {
		t.Run(input, func(t *testing.T) {
			c := DefaultAttributionConfig()
			err := json.Unmarshal([]byte(input), &c.Default)
			if err == nil {
				_, err = NormalizeAttributionConfig(c)
			}
			require.Error(t, err)
		})
	}
}

func TestAttributionActionsPolicyBaselineCompatibility(t *testing.T) {
	p := AttributionPolicy{Model: "probe", HighModels: []string{"high"}, LowModels: []string{"low"}}
	s := AttributionSnapshot{Version: 4, Policy: p, ResultMode: AttributionResultModeReplace}
	old := AttributionPolicyDigest(s)
	s.Version = 5
	require.Equal(t, old, AttributionPolicyDigest(s))
	empty := AttributionModelMappings{}
	s.Policy.HighModelMapping = &empty
	require.NotEqual(t, old, AttributionPolicyDigest(s), "explicit empty means clearing aliases")
	s.Version = 4
	require.Equal(t, old, AttributionPolicyDigest(s), "legacy jobs never acquire new actions")
	s.Version, s.ResultMode = 5, AttributionResultModeSchedule
	withActions := AttributionPolicyDigest(s)
	s.Policy = p
	require.Equal(t, withActions, AttributionPolicyDigest(s), "schedule mode ignores new actions")
	s.ResultMode = AttributionResultModeReplace
	groups := []int64{1, 2}
	s.Policy.HighGroupIDs = &groups
	before := AttributionPolicyDigest(s)
	groups[0], groups[1] = groups[1], groups[0]
	require.NotEqual(t, before, AttributionPolicyDigest(s), "group order controls priority")
}

func TestAttributionActionModelMappingReplacesOrPreserves(t *testing.T) {
	current := map[string]any{"old": "old", "alias": "old-target", "wild*": "old"}
	models := []string{"new", "alias"}
	require.Equal(t, map[string]any{"new": "new", "alias": "old-target", "wild*": "old"}, AttributionActionModelMapping(current, models, nil))
	rules := AttributionModelMappings{"alias": "target", "custom*": "actual"}
	require.Equal(t, map[string]any{"new": "new", "alias": "target", "custom*": "actual"}, AttributionActionModelMapping(current, models, &rules))
	empty := AttributionModelMappings{}
	require.Equal(t, map[string]any{"new": "new", "alias": "alias"}, AttributionActionModelMapping(current, models, &empty))
	require.Equal(t, "old-target", current["alias"])
}

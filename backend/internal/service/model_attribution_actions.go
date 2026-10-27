package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
)

// A pointer distinguishes an absent action from an enabled empty mapping.
type AttributionModelMappings map[string]string

func (m *AttributionModelMappings) UnmarshalJSON(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return fmt.Errorf("%w: 模型映射必须为对象", ErrAttributionInvalid)
	}
	out := AttributionModelMappings{}
	for d.More() {
		token, err = d.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok {
			return ErrAttributionInvalid
		}
		if _, exists := out[key]; exists {
			return fmt.Errorf("%w: 模型映射来源重复", ErrAttributionInvalid)
		}
		var value string
		if err = d.Decode(&value); err != nil {
			return err
		}
		out[key] = value
	}
	if _, err = d.Token(); err != nil {
		return err
	}
	*m = out
	return nil
}

type AttributionAccountGroup struct {
	GroupID  int64 `json:"group_id"`
	Priority int   `json:"priority"`
}

func AttributionAccountGroups(groups []AccountGroup) []AttributionAccountGroup {
	out := make([]AttributionAccountGroup, 0, len(groups))
	for _, g := range groups {
		out = append(out, AttributionAccountGroup{g.GroupID, g.Priority})
	}
	return out
}

func (p AttributionPolicy) WithoutActions() AttributionPolicy {
	p.HighModelMapping, p.LowModelMapping, p.HighGroupIDs, p.LowGroupIDs = nil, nil, nil, nil
	return p
}

func (p AttributionPolicy) ChangesGroups() bool { return p.HighGroupIDs != nil || p.LowGroupIDs != nil }

func (p AttributionPolicy) ActionGroups() []int64 {
	var out []int64
	for _, ids := range []*[]int64{p.HighGroupIDs, p.LowGroupIDs} {
		if ids != nil {
			out = append(out, (*ids)...)
		}
	}
	return out
}

func AttributionActionModelMapping(current map[string]any, models []string, mapping *AttributionModelMappings) map[string]any {
	if mapping == nil {
		return AttributionModelMapping(current, models)
	}
	out := make(map[string]any, len(models)+len(*mapping))
	for _, model := range models {
		out[model] = model
	}
	for from, to := range *mapping {
		out[from] = to
	}
	return out
}

func normalizeAttributionActions(p *AttributionPolicy) error {
	valid := func(s string) bool { return s != "" && len(s) <= 200 && strings.IndexFunc(s, unicode.IsSpace) < 0 }
	for _, field := range []**AttributionModelMappings{&p.HighModelMapping, &p.LowModelMapping} {
		if *field == nil {
			continue
		}
		if len(**field) > 500 {
			return ErrAttributionInvalid
		}
		out := AttributionModelMappings{}
		for from, to := range **field {
			from, to = strings.TrimSpace(from), strings.TrimSpace(to)
			if !valid(from) || !valid(to) || strings.Contains(to, "*") || strings.Count(from, "*") > 1 || strings.Contains(from, "*") && !strings.HasSuffix(from, "*") {
				return fmt.Errorf("%w: 映射来源仅支持末尾通配符，目标须为具体模型 ID", ErrAttributionInvalid)
			}
			if _, exists := out[from]; exists {
				return fmt.Errorf("%w: 模型映射来源重复", ErrAttributionInvalid)
			}
			out[from] = to
		}
		*field = &out
	}
	for _, field := range []**[]int64{&p.HighGroupIDs, &p.LowGroupIDs} {
		if *field == nil {
			continue
		}
		if len(**field) == 0 || len(**field) > 1000 {
			return fmt.Errorf("%w: 目标分组须至少选择一个", ErrAttributionInvalid)
		}
		seen := map[int64]bool{}
		out := make([]int64, 0, len(**field))
		for _, id := range **field {
			if id < 1 || seen[id] {
				return fmt.Errorf("%w: 目标分组无效或重复", ErrAttributionInvalid)
			}
			seen[id] = true
			out = append(out, id)
		}
		*field = &out
	}
	return nil
}

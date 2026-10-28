package service

import (
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// filterOpenAICodexStreamOptions follows the current Codex Responses schema.
// Legacy compact has no streaming options. Never enable the feature by default.
func filterOpenAICodexStreamOptions(value any, compact bool) (map[string]any, bool) {
	options, ok := value.(map[string]any)
	if compact || !ok || options["reasoning_summary_delivery"] != "sequential_cutoff" {
		return nil, true
	}
	if len(options) == 1 {
		return options, false
	}
	return map[string]any{"reasoning_summary_delivery": "sequential_cutoff"}, true
}

func normalizeOpenAICodexStreamOptionsBody(body []byte, compact bool) ([]byte, bool, error) {
	options := gjson.GetBytes(body, "stream_options")
	if !options.Exists() {
		return body, false, nil
	}
	filtered, changed := filterOpenAICodexStreamOptions(options.Value(), compact)
	if !changed {
		return body, false, nil
	}
	var out []byte
	var err error
	if filtered == nil {
		out, err = sjson.DeleteBytes(body, "stream_options")
	} else {
		out, err = sjson.SetBytes(body, "stream_options", filtered)
	}
	if err != nil {
		return body, false, fmt.Errorf("normalize Codex stream_options: %w", err)
	}
	return out, true, nil
}

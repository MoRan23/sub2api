package service

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
)

const openAITimingMetricsHeader = "x-responsesapi-include-timing-metrics"

// FingerprintTimingMetrics describes the physical request, including an
// explicit false. It never retains arbitrary invalid header text.
type FingerprintTimingMetrics struct {
	Status  string `json:"status"`
	Enabled *bool  `json:"enabled,omitempty"`
	Source  string `json:"source"`
}

func openAITimingMetricsValues(headers http.Header) ([]string, bool) {
	var names []string
	for name := range headers {
		if strings.EqualFold(name, openAITimingMetricsHeader) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	var values []string
	for _, name := range names {
		values = append(values, headers[name]...)
	}
	return values, len(names) > 0
}

func copyOpenAITimingMetricsHeader(dst, src http.Header) {
	if values, present := openAITimingMetricsValues(src); present {
		dst[http.CanonicalHeaderKey(openAITimingMetricsHeader)] = append([]string(nil), values...)
	}
}

func openAITimingMetricsCompatibility(headers http.Header) string {
	values, present := openAITimingMetricsValues(headers)
	if !present {
		return ""
	}
	// Preserve duplicate and invalid values as distinct compatibility keys. They
	// are still forwarded, never silently collapsed to the missing/default case.
	encoded, _ := json.Marshal(values)
	return string(encoded)
}

func observeOpenAITimingMetrics(headers http.Header, source string) *FingerprintTimingMetrics {
	observation := &FingerprintTimingMetrics{Status: "absent", Source: source}
	values, present := openAITimingMetricsValues(headers)
	if !present {
		return observation
	}
	observation.Status = "invalid"
	if len(values) != 1 {
		return observation
	}
	value := strings.TrimSpace(values[0])
	if value == "true" || value == "false" {
		enabled := value == "true"
		observation.Status, observation.Enabled = "valid", &enabled
	}
	return observation
}

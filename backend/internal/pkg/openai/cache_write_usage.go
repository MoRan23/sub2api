package openai

import "github.com/tidwall/gjson"

// CacheCreationTTLTokens accepts only complete, non-negative integer buckets
// whose sum equals the upstream-reported aggregate. Request cache settings do
// not prove that a cache write occurred or establish its lifetime.
func CacheCreationTTLTokens(value gjson.Result, aggregate int) (int, int) {
	for _, path := range []string{"input_tokens_details.cache_write_tokens_details", "prompt_tokens_details.cache_write_tokens_details"} {
		details := value.Get(path)
		if !details.Exists() {
			continue
		}
		return CacheCreationTTLDetailsTokens(details, aggregate)
	}
	return 0, 0
}

// CacheCreationTTLDetailsTokens validates a detached cache_write_tokens_details
// object with the same rules used for the complete usage object.
func CacheCreationTTLDetailsTokens(details gjson.Result, aggregate int) (int, int) {
	short, long := details.Get("ttl_30m_tokens"), details.Get("ttl_12h_tokens")
	if short.Type != gjson.Number || long.Type != gjson.Number {
		return 0, 0
	}
	a, b := short.Int(), long.Int()
	if a < 0 || b < 0 || float64(a) != short.Float() || float64(b) != long.Float() ||
		a > int64(aggregate) || b != int64(aggregate)-a {
		return 0, 0
	}
	return int(a), int(b)
}

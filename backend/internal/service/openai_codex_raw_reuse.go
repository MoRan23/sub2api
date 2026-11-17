package service

import (
	"bytes"
	"unicode/utf8"
)

// canReuseDecodedOpenAICodexBody is only called after decodeOpenAIJSONUseNumber
// has successfully decoded this exact body into decoded, and before any actual
// map mutation. It preserves the legacy decode/encode normalization for invalid
// UTF-8, unpaired UTF-16 escapes and duplicate object names.
//
// In valid JSON every colon outside a string belongs to an object member. The
// decoded tree has one map entry per surviving name. A duplicate always reduces
// that total (and may also discard its previous value's descendants), so equal
// totals prove that no duplicate, including an escaped equivalent, was lost.
func canReuseDecodedOpenAICodexBody(body []byte, decoded map[string]any) bool {
	if !utf8.Valid(body) {
		return false
	}
	rawMembers, validStrings := countOpenAICodexRawObjectMembers(body)
	return validStrings && rawMembers == countOpenAICodexDecodedObjectMembers(decoded)
}

func countOpenAICodexDecodedObjectMembers(value any) int {
	switch value := value.(type) {
	case map[string]any:
		total := len(value)
		for _, child := range value {
			total += countOpenAICodexDecodedObjectMembers(child)
		}
		return total
	case []any:
		total := 0
		for _, child := range value {
			total += countOpenAICodexDecodedObjectMembers(child)
		}
		return total
	default:
		return 0
	}
}

// The caller has already validated the JSON with the standard decoder. Byte
// searches skip long text/image strings without a per-byte Go scan or copies.
func countOpenAICodexRawObjectMembers(body []byte) (int, bool) {
	total := 0
	for len(body) > 0 {
		quote := bytes.IndexByte(body, '"')
		if quote < 0 {
			return total + bytes.Count(body, []byte{':'}), true
		}
		total += bytes.Count(body[:quote], []byte{':'})
		body = body[quote+1:]
		quote = bytes.IndexByte(body, '"')
		for {
			if quote < 0 {
				return 0, false
			}
			escape := bytes.IndexByte(body[:quote], '\\')
			if escape < 0 {
				body = body[quote+1:]
				break
			}
			body = body[escape:]
			quote -= escape
			if len(body) < 2 {
				return 0, false
			}
			consumed := 2
			if body[1] == 'u' {
				if len(body) < 6 {
					return 0, false
				}
				consumed = 6
				if body[2]|0x20 == 'd' {
					second := body[3] | 0x20
					if second == '8' || second == '9' || second == 'a' || second == 'b' {
						if len(body) < 12 || body[6] != '\\' || body[7] != 'u' || body[8]|0x20 != 'd' {
							return 0, false
						}
						low := body[9] | 0x20
						if low < 'c' || low > 'f' {
							return 0, false
						}
						consumed = 12
					} else if second >= 'c' && second <= 'f' {
						return 0, false
					}
				}
			}
			body = body[consumed:]
			quote -= consumed
			// Keep the already found terminator across escapes. Only an escaped
			// quote consumes it; rescanning it for every \\n would be quadratic.
			if quote < 0 {
				quote = bytes.IndexByte(body, '"')
			}
		}
	}
	return total, true
}

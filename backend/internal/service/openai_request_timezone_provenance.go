package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/tidwall/gjson"
)

// OpenAIRequestTimezoneProvenance is an observation-only checkpoint. It retains
// bounded paths and content digests, never the body or complete environment/tool
// text. A new checkpoint is needed after an adapter changes a source's content.
type OpenAIRequestTimezoneProvenance struct {
	valid      bool
	candidates []openAIRequestTimezoneProvenanceCandidate
}

type openAIRequestTimezoneProvenanceCandidate struct {
	path   string
	digest [sha256.Size]byte
}

// CaptureOpenAIRequestTimezoneProvenance records full environment texts and full
// search-tool structures by digest. Timezone values or array positions alone
// cannot prove that an adapter preserved a source. Budget or parse failures make
// the entire checkpoint unknown, preventing a partial scan from hiding copies.
func CaptureOpenAIRequestTimezoneProvenance(body []byte) *OpenAIRequestTimezoneProvenance {
	checkpoint := &OpenAIRequestTimezoneProvenance{}
	scan := scanOpenAIRequestTimezones(body)
	if scan.result.ScanStatus != "complete" {
		return checkpoint
	}
	budget := openAIRequestTimezoneProvenanceBudget{textBytes: scan.textBytes, nodes: scan.nodes}
	for _, occurrence := range scan.occurrences {
		var content []byte
		switch occurrence.item.Source {
		case "environment_context":
			content = []byte(occurrence.text)
		case "web_search":
			toolPath := strings.TrimSuffix(occurrence.item.Path, ".user_location.timezone")
			tool := gjson.GetBytes(body, toolPath)
			if !tool.IsObject() || !budget.countText(len(tool.Raw)) {
				return checkpoint
			}
			var err error
			content, err = canonicalOpenAIRequestTimezoneProvenanceJSON([]byte(tool.Raw), &budget)
			if err != nil {
				return checkpoint
			}
		default:
			return checkpoint
		}
		hasher := sha256.New()
		_, _ = hasher.Write([]byte(occurrence.item.Source))
		_, _ = hasher.Write([]byte{0})
		_, _ = hasher.Write(content)
		var digest [sha256.Size]byte
		copy(digest[:], hasher.Sum(nil))
		checkpoint.candidates = append(checkpoint.candidates, openAIRequestTimezoneProvenanceCandidate{
			path: occurrence.item.Path, digest: digest,
		})
	}
	checkpoint.valid = true
	return checkpoint
}

// MapTo associates a source only when its complete content/structure occurs
// exactly once on both sides. Missing or duplicate sources remain unknown; an
// empty destination is never invented because deletion needs adapter evidence.
// A non-nil map is always returned, including on failure, so the observer cannot
// fall back to same-index matching for an adapted request.
func (checkpoint *OpenAIRequestTimezoneProvenance) MapTo(after []byte) map[string]string {
	paths := make(map[string]string)
	if checkpoint == nil || !checkpoint.valid || len(checkpoint.candidates) == 0 {
		return paths
	}
	final := CaptureOpenAIRequestTimezoneProvenance(after)
	if !final.valid {
		return paths
	}
	beforeCounts := make(map[[sha256.Size]byte]int, len(checkpoint.candidates))
	afterCounts := make(map[[sha256.Size]byte]int, len(final.candidates))
	afterPaths := make(map[[sha256.Size]byte]string, len(final.candidates))
	for _, candidate := range checkpoint.candidates {
		beforeCounts[candidate.digest]++
	}
	for _, candidate := range final.candidates {
		afterCounts[candidate.digest]++
		afterPaths[candidate.digest] = candidate.path
	}
	for _, candidate := range checkpoint.candidates {
		if beforeCounts[candidate.digest] == 1 && afterCounts[candidate.digest] == 1 {
			paths[candidate.path] = afterPaths[candidate.digest]
		}
	}
	return paths
}

func DeriveOpenAIRequestTimezoneProvenance(before, after []byte) map[string]string {
	return CaptureOpenAIRequestTimezoneProvenance(before).MapTo(after)
}

type openAIRequestTimezoneProvenanceBudget struct {
	textBytes int
	nodes     int
}

func (budget *openAIRequestTimezoneProvenanceBudget) countText(size int) bool {
	if size > openAIRequestTimezoneTextLimit-budget.textBytes {
		return false
	}
	budget.textBytes += size
	return true
}

var errOpenAIRequestTimezoneProvenanceUnknown = errors.New("timezone provenance cannot be established")

// Parse bounded tool JSON with exact numbers and reject duplicate keys. Sorted
// object serialization ignores harmless key-order changes while retaining every
// tool field, nested value, array order and numeric representation.
func canonicalOpenAIRequestTimezoneProvenanceJSON(raw []byte, budget *openAIRequestTimezoneProvenanceBudget) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := readOpenAIRequestTimezoneProvenanceJSON(decoder, budget, 0)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errOpenAIRequestTimezoneProvenanceUnknown
	}
	return json.Marshal(value)
}

func readOpenAIRequestTimezoneProvenanceJSON(decoder *json.Decoder, budget *openAIRequestTimezoneProvenanceBudget, depth int) (any, error) {
	if depth > 128 || budget.nodes >= openAIRequestTimezoneNodeLimit {
		return nil, errOpenAIRequestTimezoneProvenanceUnknown
	}
	budget.nodes++
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, composite := token.(json.Delim)
	if !composite {
		return token, nil
	}
	switch delim {
	case '{':
		value := make(map[string]any)
		for decoder.More() {
			if budget.nodes >= openAIRequestTimezoneNodeLimit {
				return nil, errOpenAIRequestTimezoneProvenanceUnknown
			}
			budget.nodes++
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, errOpenAIRequestTimezoneProvenanceUnknown
			}
			if _, duplicate := value[key]; duplicate {
				return nil, errOpenAIRequestTimezoneProvenanceUnknown
			}
			child, err := readOpenAIRequestTimezoneProvenanceJSON(decoder, budget, depth+1)
			if err != nil {
				return nil, err
			}
			value[key] = child
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return nil, errOpenAIRequestTimezoneProvenanceUnknown
		}
		return value, nil
	case '[':
		value := make([]any, 0)
		for decoder.More() {
			child, err := readOpenAIRequestTimezoneProvenanceJSON(decoder, budget, depth+1)
			if err != nil {
				return nil, err
			}
			value = append(value, child)
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return nil, errOpenAIRequestTimezoneProvenanceUnknown
		}
		return value, nil
	default:
		return nil, errOpenAIRequestTimezoneProvenanceUnknown
	}
}

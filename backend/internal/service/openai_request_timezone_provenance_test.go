package service

import (
	"bytes"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIRequestTimezoneProvenanceMapsUniqueEnvironmentAcrossFiltering(t *testing.T) {
	a := timezoneTestEnvironment(OpenAIRequestTimezone, "2026-09-09")
	b := strings.ReplaceAll(a, "/project", "/different-project")
	before := timezoneTestBody(t, map[string]any{"input": []any{
		map[string]any{"role": "system", "content": "promoted instruction"},
		map[string]any{"role": "user", "content": a},
		map[string]any{"type": "item_reference", "id": "removed"},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "input_image", "image_url": "data:image/png;base64,"},
			map[string]any{"type": "input_text", "text": b},
		}},
	}})
	after := timezoneTestBody(t, map[string]any{"model": "mapped-model", "input": []any{
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": b}}},
		map[string]any{"role": "user", "content": a},
		map[string]any{"type": "compaction_trigger"},
	}})
	require.Equal(t, map[string]string{
		"input.1.content":        "input.1.content",
		"input.3.content.1.text": "input.0.content.0.text",
	}, DeriveOpenAIRequestTimezoneProvenance(before, after))
}

func TestOpenAIRequestTimezoneProvenanceMapsStringInputAndDoesNotRetainBody(t *testing.T) {
	environment := timezoneTestEnvironment(OpenAIRequestTimezone, "2026-09-09")
	before := timezoneTestBody(t, map[string]any{"input": environment})
	saved := bytes.Clone(before)
	checkpoint := CaptureOpenAIRequestTimezoneProvenance(before)
	for i := range before {
		before[i] = 'x'
	}
	after := timezoneTestBody(t, map[string]any{"input": []any{map[string]any{"role": "user", "content": environment}}})
	require.Equal(t, map[string]string{"input": "input.0.content"}, checkpoint.MapTo(after))
	require.Equal(t, map[string]string{"input": "input"}, checkpoint.MapTo(saved))
}

func TestOpenAIRequestTimezoneProvenanceNeverMatchesTimezoneOrIndexAlone(t *testing.T) {
	before := []byte(`{"tools":[{"type":"web_search_preview","user_location":{"timezone":"America/Los_Angeles"}},{"type":"web_search","user_location":{"timezone":"America/Los_Angeles"}}]}`)
	after := []byte(`{"tools":[{"user_location":{"timezone":"America/Los_Angeles"},"type":"web_search"}]}`)
	require.Equal(t, map[string]string{
		"tools.1.user_location.timezone": "tools.0.user_location.timezone",
	}, DeriveOpenAIRequestTimezoneProvenance(before, after), "the removed preview must not claim the retained tool at the same index")

	changed := []byte(`{"tools":[{"type":"web_search_preview","user_location":{"timezone":"America/Los_Angeles","city":"Seattle"}}]}`)
	require.Empty(t, DeriveOpenAIRequestTimezoneProvenance(before, changed), "equal timezone and path do not prove an unchanged complete tool")
	missing := DeriveOpenAIRequestTimezoneProvenance(before, []byte(`{"tools":[]}`))
	require.NotNil(t, missing)
	require.Empty(t, missing, "absence alone is not evidence that a particular adapter deleted a source")
}

func TestOpenAIRequestTimezoneProvenanceCanonicalizesFullToolWithoutLosingNumbers(t *testing.T) {
	before := []byte(`{"tools":[{"type":"web_search","user_location":{"timezone":"UTC","city":"Seattle","extension":{"large":9007199254740993,"list":[true,null]}}}]}`)
	after := []byte(`{"tools":[{"user_location":{"extension":{"list":[true,null],"large":9007199254740993},"city":"Seattle","timezone":"UTC"},"type":"web_search"}]}`)
	require.Equal(t, map[string]string{"tools.0.user_location.timezone": "tools.0.user_location.timezone"}, DeriveOpenAIRequestTimezoneProvenance(before, after))
	changed := bytes.ReplaceAll(after, []byte("9007199254740993"), []byte("9007199254740992"))
	require.Empty(t, DeriveOpenAIRequestTimezoneProvenance(before, changed))
}

func TestOpenAIRequestTimezoneProvenanceDuplicateCandidatesRemainUnknown(t *testing.T) {
	environment := timezoneTestEnvironment(OpenAIRequestTimezone, "2026-09-09")
	one := timezoneTestBody(t, map[string]any{"input": []any{map[string]any{"role": "user", "content": environment}}})
	two := timezoneTestBody(t, map[string]any{"input": []any{
		map[string]any{"role": "user", "content": environment},
		map[string]any{"role": "user", "content": environment},
	}})
	for _, pair := range [][2][]byte{{one, two}, {two, one}, {two, two}} {
		paths := DeriveOpenAIRequestTimezoneProvenance(pair[0], pair[1])
		require.NotNil(t, paths)
		require.Empty(t, paths)
	}
	tools := []byte(`{"tools":[{"type":"web_search","user_location":{"timezone":"UTC"}},{"user_location":{"timezone":"UTC"},"type":"web_search"}]}`)
	require.Empty(t, DeriveOpenAIRequestTimezoneProvenance(tools, tools))
}

func TestOpenAIRequestTimezoneProvenanceFailuresAreWholeSnapshotUnknown(t *testing.T) {
	environment := timezoneTestEnvironment(OpenAIRequestTimezone, "2026-09-09")
	valid := timezoneTestBody(t, map[string]any{"input": environment})
	oversizedTool := timezoneTestBody(t, map[string]any{"input": environment, "tools": []any{map[string]any{
		"type": "web_search", "user_location": map[string]any{"timezone": "UTC"}, "unknown": strings.Repeat("x", openAIRequestTimezoneTextLimit),
	}}})
	tooManyNodes := []byte(`{"input":` + string(timezoneTestBody(t, environment)) + `,"tools":[{"type":"web_search","user_location":{"timezone":"UTC"},"unknown":[` + strings.Repeat(`0,`, openAIRequestTimezoneNodeLimit) + `0]}]}`)
	tooManyItems := []byte(`{"tools":[` + strings.Repeat(`{"type":"web_search","user_location":{"timezone":"UTC"}},`, openAIRequestTimezoneItemLimit) + `{"type":"web_search","user_location":{"timezone":"UTC"}}]}`)
	duplicateKeys := []byte(`{"input":` + string(timezoneTestBody(t, environment)) + `,"tools":[{"type":"web_search","user_location":{"timezone":"UTC","timezone":"Asia/Shanghai"}}]}`)
	deepTool := []byte(`{"tools":[{"type":"web_search","user_location":{"timezone":"UTC"},"unknown":` + strings.Repeat("[", 130) + "0" + strings.Repeat("]", 130) + `}]}`)
	for name, body := range map[string][]byte{
		"invalid JSON": []byte(`{"input":`), "text limit": oversizedTool, "node limit": tooManyNodes,
		"item limit": tooManyItems, "duplicate tool keys": duplicateKeys, "depth limit": deepTool,
	} {
		t.Run(name, func(t *testing.T) {
			paths := DeriveOpenAIRequestTimezoneProvenance(body, body)
			require.NotNil(t, paths)
			require.Empty(t, paths)
			paths = CaptureOpenAIRequestTimezoneProvenance(valid).MapTo(body)
			require.NotNil(t, paths)
			require.Empty(t, paths, "a late scan failure must not expose earlier partial matches")
		})
	}
	var absent *OpenAIRequestTimezoneProvenance
	require.NotNil(t, absent.MapTo(valid))
	require.Empty(t, absent.MapTo(valid))
}

func TestOpenAIRequestTimezoneProvenanceCheckpointSupportsConcurrentRead(t *testing.T) {
	body := timezoneTestBody(t, map[string]any{"input": timezoneTestEnvironment(OpenAIRequestTimezone, "2026-09-09")})
	checkpoint := CaptureOpenAIRequestTimezoneProvenance(body)
	var wg sync.WaitGroup
	results := make(chan map[string]string, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- checkpoint.MapTo(body)
		}()
	}
	wg.Wait()
	close(results)
	for paths := range results {
		require.Equal(t, map[string]string{"input": "input"}, paths)
	}
}

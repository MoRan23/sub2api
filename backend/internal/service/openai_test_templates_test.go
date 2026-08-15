package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

func TestSelectOpenAITestTemplate(t *testing.T) {
	templates := []string{"first", "second", "third"}

	for index, want := range templates {
		if got := selectOpenAITestTemplate(templates, index); got != want {
			t.Fatalf("selectOpenAITestTemplate(%d) = %q, want %q", index, got, want)
		}
	}

	for _, index := range []int{-1, len(templates), len(templates) + 1} {
		if got := selectOpenAITestTemplate(templates, index); got != templates[0] {
			t.Fatalf("selectOpenAITestTemplate(%d) = %q, want fallback %q", index, got, templates[0])
		}
	}
	if got := selectOpenAITestTemplate(nil, 0); got != "" {
		t.Fatalf("selectOpenAITestTemplate(nil, 0) = %q, want empty", got)
	}
}

func TestRandomOpenAITestTemplatesStayWithinConfiguredSets(t *testing.T) {
	textTemplates := make(map[string]struct{}, len(openAITextTestTemplates))
	for _, value := range openAITextTestTemplates {
		textTemplates[value] = struct{}{}
	}
	compactTemplates := make(map[string]struct{}, len(openAICompactTestTemplates))
	for _, value := range openAICompactTestTemplates {
		compactTemplates[value] = struct{}{}
	}
	imageTemplates := make(map[string]struct{}, len(openAIImageTestTemplates))
	for _, value := range openAIImageTestTemplates {
		imageTemplates[value] = struct{}{}
	}
	for i := 0; i < 64; i++ {
		if _, ok := textTemplates[randomOpenAITextTestPrompt()]; !ok {
			t.Fatal("random text prompt was not selected from configured templates")
		}
		if _, ok := compactTemplates[randomOpenAICompactTestPrompt()]; !ok {
			t.Fatal("random compact prompt was not selected from configured templates")
		}
		if _, ok := imageTemplates[randomOpenAIImageTestPrompt()]; !ok {
			t.Fatal("random image prompt was not selected from configured templates")
		}
		if got := openAITestInstructions(); got != openai.DefaultInstructions {
			t.Fatalf("test instructions = %q, want openai.DefaultInstructions", got)
		}
	}
}

func TestCreateOpenAITestPayloadUsesRandomDefaultsAndPreservesAccountShape(t *testing.T) {
	for _, isOAuth := range []bool{false, true} {
		payload := createOpenAITestPayload("gpt-test", isOAuth)
		input, ok := payload["input"].([]map[string]any)
		if !ok || len(input) != 1 {
			t.Fatalf("input = %#v, want one message", payload["input"])
		}
		content, ok := input[0]["content"].([]map[string]any)
		if !ok || len(content) != 1 {
			t.Fatalf("content = %#v, want one item", input[0]["content"])
		}
		if _, ok := map[string]struct{}{
			"hi":    {},
			"hello": {},
			"Please respond with a short acknowledgement.": {},
			"Reply with one concise sentence.":             {},
			"Give a brief confirmation.":                   {},
		}[content[0]["text"].(string)]; !ok {
			t.Fatalf("unexpected Responses test input: %#v", content[0]["text"])
		}
		if got := payload["instructions"].(string); got != openai.DefaultInstructions {
			t.Fatalf("Responses test instructions = %q, want openai.DefaultInstructions", got)
		}
		if payload["store"] == nil && isOAuth {
			t.Fatal("OAuth Responses test must keep store=false")
		}
		if !isOAuth {
			if _, exists := payload["store"]; exists {
				t.Fatal("API-key Responses test must not add OAuth store field")
			}
		}
	}
}

func TestOpenAIChatCompletionsTestPayloadPreservesExplicitPrompt(t *testing.T) {
	explicit := createOpenAIChatCompletionsTestPayload("gpt-test", "explicit prompt")
	if got := explicit["messages"].([]map[string]any)[0]["content"]; got != "explicit prompt" {
		t.Fatalf("explicit prompt = %#v, want preserved prompt", got)
	}

	defaulted := createOpenAIChatCompletionsTestPayload("gpt-test", "")
	content := defaulted["messages"].([]map[string]any)[0]["content"].(string)
	if !containsOpenAITestTemplate(openAITextTestTemplates, content) {
		t.Fatalf("default Chat Completions prompt %q is not randomized from configured templates", content)
	}
}

func TestOpenAICompactProbePayloadUsesRandomDefaults(t *testing.T) {
	payload := createOpenAICompactProbePayload("gpt-test", true)
	if got := payload["instructions"].(string); got != openai.DefaultInstructions {
		t.Fatalf("compact instructions = %q, want openai.DefaultInstructions", got)
	}
	if stream, _ := payload["stream"].(bool); !stream {
		t.Fatal("native compact probe must use the streaming Responses wire")
	}
	if store, ok := payload["store"].(bool); !ok || store {
		t.Fatal("OAuth native compact probe must set store=false")
	}
	input := payload["input"].([]any)
	message := input[0].(map[string]any)
	content := message["content"].(string)
	if !containsOpenAITestTemplate(openAICompactTestTemplates, content) {
		t.Fatalf("compact prompt %q is not randomized from configured templates", content)
	}
	trigger := input[len(input)-1].(map[string]any)
	if trigger["type"] != "compaction_trigger" {
		t.Fatalf("compact probe terminal input = %#v, want compaction_trigger", trigger)
	}
}

func TestRandomOpenAIImageTestPromptUsesConfiguredTemplates(t *testing.T) {
	for i := 0; i < 32; i++ {
		if !containsOpenAITestTemplate(openAIImageTestTemplates, randomOpenAIImageTestPrompt()) {
			t.Fatal("image prompt was not selected from configured templates")
		}
	}
}

func containsOpenAITestTemplate(templates []string, value string) bool {
	for _, template := range templates {
		if template == value {
			return true
		}
	}
	return false
}

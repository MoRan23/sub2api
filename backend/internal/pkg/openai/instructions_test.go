package openai

import (
	"strings"
	"testing"
)

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// CodexBaseInstructionsForModel 应按模型返回对应的真实 Codex base prompt。
func TestCodexBaseInstructionsForModel(t *testing.T) {
	cases := []struct {
		model    string
		wantHead string
	}{
		{"gpt-6-astra", "You are Codex, an agent based on GPT-6"},
		{"gpt-6", "You are Codex, an agent based on GPT-6"},
		{"openai/gpt-6-astra", "You are Codex, an agent based on GPT-6"},
		{"OPENAI/GPT-6_ASTRA", "You are Codex, an agent based on GPT-6"},
		{"gpt-6-astra-2026-09-01", "You are Codex, an agent based on GPT-6"},
		{"gpt-5-codex", "You are Codex, based on GPT-5"},
		{"gpt-5.3-codex", "You are Codex, based on GPT-5"},
		{"gpt-5.3-codex-spark", "You are Codex, based on GPT-5"},
		{"gpt-5.1-codex-max", "You are Codex, based on GPT-5"},
		{"gpt-5.2-codex", "You are Codex, based on GPT-5"},
		{"gpt-5.5", "You are Codex, a coding agent based on GPT-5"},
		{" GPT-5.5 ", "You are Codex, a coding agent based on GPT-5"},
		{"gpt-5.2", "You are GPT-5.2 running in the Codex CLI"},
		{"gpt-5.1", "You are GPT-5.1 running in the Codex CLI"},
		{"gpt-5", "You are Codex, a coding agent based on GPT-5"}, // 回退到最新（GPT-5.5）
		{"gpt-5.4", "You are Codex, a coding agent based on GPT-5"},
		{"gpt-5.3", "You are Codex, a coding agent based on GPT-5"}, // 未单独维护 → 最新
		{"some-unknown-model", "You are Codex, a coding agent based on GPT-5"},
		{"", "You are Codex, a coding agent based on GPT-5"}, // 回退到最新
	}
	for _, c := range cases {
		got := strings.TrimSpace(CodexBaseInstructionsForModel(c.model))
		if got == "" {
			t.Errorf("model %q: got empty instructions", c.model)
			continue
		}
		if !strings.HasPrefix(got, c.wantHead) {
			t.Errorf("model %q: got prefix %q, want %q", c.model, firstLine(got), c.wantHead)
		}
	}
}

func TestCodexBaseInstructionsForModelCurrentManifestTemplates(t *testing.T) {
	for _, tt := range []struct {
		model string
		want  string
	}{
		{"gpt-6-astra", instructionsGPT6Astra},
		{"gpt-6", instructionsGPT6Astra},
		{"gpt-6-sol", instructionsGPT6Sol},
		{"openai/GPT-6_SOL", instructionsGPT6Sol},
		{"gpt-6-luna", instructionsGPT6Luna},
		{"OPENAI/GPT-6_LUNA", instructionsGPT6Luna},
		{"gpt-5.6-sol", instructionsGPT56},
		{"gpt-5.6-terra", instructionsGPT56},
		{"gpt-5.6-luna", instructionsGPT56},
		{"openai/GPT-5.6", instructionsGPT56},
		{"gpt-5.5", instructionsGPT55},
		{"gpt-5.4", instructionsGPT54},
		{"gpt-daybreak-blue-latest", instructionsDaybreakBlue},
		{"gpt-daybreak-red-latest", instructionsDaybreakRed},
		{"codex-auto-review", instructionsGPT56},
	} {
		t.Run(tt.model, func(t *testing.T) {
			if strings.TrimSpace(tt.want) == "" {
				t.Fatal("official model template must not be empty")
			}
			if got := CodexBaseInstructionsForModel(tt.model); got != tt.want {
				t.Fatal("model did not select its official manifest template")
			}
		})
	}
}

func TestCodexBaseInstructionsForModelNewVariantsUseExactNames(t *testing.T) {
	for _, model := range []string{"gpt-6-sol-latest", "gpt-6-luna-2026-09-23", "gpt-6-other"} {
		t.Run(model, func(t *testing.T) {
			if got := CodexBaseInstructionsForModel(model); got != instructionsGPT55 {
				t.Fatal("unlisted GPT-6 name must retain the unknown-model fallback")
			}
		})
	}
	if instructionsGPT6Astra == instructionsGPT6Sol || instructionsGPT6Astra == instructionsGPT6Luna || instructionsGPT6Sol == instructionsGPT6Luna {
		t.Fatal("GPT-6 variants must keep their distinct official templates")
	}
	if CodexBaseInstructionsForModel("codex-auto-review") == instructionsDaybreakBlue {
		t.Fatal("auto-review must follow its current template, not the older Daybreak template")
	}
}

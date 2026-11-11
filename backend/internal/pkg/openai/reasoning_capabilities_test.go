package openai

import "testing"

func TestIsCyberModelSpelling(t *testing.T) {
	for _, family := range []string{"gpt-5.6-cyber", "gpt-6-cyber"} {
		for _, suffix := range []string{"", "-max", "-ultra", "-2026-11-11", "-openai-compact", "-max-openai-compact"} {
			if !IsCyberModelSpelling(family + suffix) {
				t.Errorf("expected recognized Cyber model: %s%s", family, suffix)
			}
		}
	}
	for _, model := range []string{"openai/GPT-6-CYBER", "openai/gpt_5.6_cyber"} {
		if !IsCyberModelSpelling(model) {
			t.Errorf("expected recognized spelling: %s", model)
		}
	}
	for _, model := range []string{"gpt-6-cyber-preview", "gpt-5.6-cyber-custom", "gpt-6-cyber-2026-99-11", "gpt-6-astra", "gpt-5.6-sol", "cyber", "6 Cyber", "gpt-6-cyberpunk"} {
		if IsCyberModelSpelling(model) {
			t.Errorf("unexpected Cyber capability inheritance: %s", model)
		}
	}
}

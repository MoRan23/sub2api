package openai

// IsCyberModelSpelling recognizes the published Cyber models and established
// spelling, effort, snapshot and compact variants. Keep this independent from
// other GPT families: reasoning support does not imply their billing or request
// transformation rules.
func IsCyberModelSpelling(model string) bool {
	return IsKnownCodexModelVariant(model, "gpt-5.6-cyber") ||
		IsKnownCodexModelVariant(model, "gpt-6-cyber")
}

package service

import (
	_ "embed"
	"encoding/json"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

// Complete offline fallback for the public catalog, from openai/codex commit
// 126a0ee92ed2c5e73cd8a8f7e37563e8527552d7, models-manager/models.json.
// Unlike the legacy synthesis defaults, this preserves every official field,
// including instructions and explicit nulls. It is metadata, not account access
// evidence, and must never be observed by the OAuth capability cache.
//
//go:embed openai_codex_official_catalog.json
var bundledOfficialCodexCatalogBaseJSON []byte

// Merge newly published full descriptors once, retaining the raw JSON fields.
// The immutable index also covers a model missing from a successful older public
// snapshot. Neither these bytes nor their tiers are account capability evidence.
var bundledOfficialCodexCatalogJSON, bundledOfficialCodexDescriptors = loadBundledOfficialCodexCatalog()

func loadBundledOfficialCodexCatalog() ([]byte, map[string]json.RawMessage) {
	var catalog map[string]json.RawMessage
	if err := json.Unmarshal(bundledOfficialCodexCatalogBaseJSON, &catalog); err != nil {
		panic("invalid bundled official Codex catalog")
	}
	var models []json.RawMessage
	if err := json.Unmarshal(catalog["models"], &models); err != nil {
		panic("invalid bundled official Codex models")
	}
	models = append(models, openai.CodexGPT62SolMetadata, openai.CodexGPT62AstraMetadata)
	index := make(map[string]json.RawMessage, len(models))
	for _, raw := range models {
		var model struct {
			Slug string `json:"slug"`
		}
		if err := json.Unmarshal(raw, &model); err != nil || model.Slug == "" || index[model.Slug] != nil {
			panic("invalid or duplicate bundled official Codex model")
		}
		index[model.Slug] = raw
	}
	var err error
	if catalog["models"], err = json.Marshal(models); err != nil {
		panic(err)
	}
	body, err := json.Marshal(catalog)
	if err != nil {
		panic(err)
	}
	return body, index
}

// The administrator-supplied descriptors deliberately take precedence over
// automatic catalogs. Keep these resources complete; do not infer account
// authorization from their advertised tools or access programs.
//
//go:embed openai_codex_gpt_5.6_cyber.json
var codexGPT56CyberDescriptorJSON []byte

//go:embed openai_codex_gpt_6_cyber.json
var codexGPT6CyberDescriptorJSON []byte

// codexUserModelDescriptorOverride returns immutable descriptor bytes. Callers
// must decode or copy them before applying public aliases or shell projection.
// Model aliases and variants are resolved by the caller; arbitrary prefixes
// must not gain the Cyber descriptor.
func codexUserModelDescriptorOverride(modelID string) (json.RawMessage, bool) {
	switch modelID {
	case "gpt-5.6-cyber":
		return json.RawMessage(codexGPT56CyberDescriptorJSON), true
	case "gpt-6-cyber":
		return json.RawMessage(codexGPT6CyberDescriptorJSON), true
	default:
		return nil, false
	}
}

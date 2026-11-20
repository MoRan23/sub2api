package service

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
)

func catalogModelAudioDeclaration(raw json.RawMessage) (bool, bool) {
	root := parseRawJSONView(raw)
	modalities := root.Get("input_modalities")
	if !modalities.Exists() {
		modalities = root.Get("modalities.input")
	}
	if !modalities.IsArray() {
		return false, false
	}
	supported := false
	modalities.ForEach(func(_, modality gjson.Result) bool {
		supported = strings.EqualFold(strings.TrimSpace(modality.String()), "audio")
		return !supported
	})
	return supported, true
}

func openAIGroupCatalogSupportsAudioInput(group *Group, accounts []Account, fixed map[int64]map[string]bool, publicID, metadataID string, descriptor json.RawMessage) bool {
	routed := group.GetRoutingAccountIDs(publicID)
	declaredAudio, _ := catalogModelAudioDeclaration(descriptor)
	found := false
	for i := range accounts {
		account := &accounts[i]
		if !account.IsOpenAI() || !account.IsModelSupported(publicID) ||
			(len(routed) > 0 && !containsInt64(routed, account.ID)) {
			continue
		}
		found = true
		target := publicID
		if !openAIGroupCatalogUsesPassthroughNames(account) {
			target = account.GetMappedModel(publicID)
		}
		metadata, _ := account.GetUpstreamModelMetadata(target)
		modalities := metadata.InputModalities
		if supported, declared := fixed[account.ID][target]; declared {
			if !supported {
				return false
			}
			modalities = []string{"audio"}
		}
		// The public subscription snapshot is not evidence for an API key route.
		if len(modalities) == 0 && account.IsOpenAIOAuthLike() && target == metadataID && declaredAudio {
			modalities = []string{"audio"}
		}
		if !accountCodexModelSupportsAudioInput(account, target, modalities) {
			return false
		}
	}
	return found
}

package service

import infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"

// GroupOpenAIServiceTierUpdate is transient intent, not a cached group setting.
type GroupOpenAIServiceTierUpdate struct {
	ForceFast      *bool
	ForceUltrafast *bool
	FreeFast       *bool
	FreeUltrafast  *bool
}

// ResolveGroupOpenAIServiceTier merges partial updates with the current record.
// The repository repeats this operation under the group row lock.
func ResolveGroupOpenAIServiceTier(platform string, forceFast, forceUltrafast, freeFast, freeUltrafast bool, patch *GroupOpenAIServiceTierUpdate) (bool, bool, bool, bool, error) {
	if !groupSupportsOpenAIFast(platform) {
		return false, false, false, false, nil
	}
	if patch != nil {
		if patch.ForceFast != nil {
			forceFast = *patch.ForceFast
		}
		if patch.ForceUltrafast != nil {
			forceUltrafast = *patch.ForceUltrafast
		}
		if patch.FreeFast != nil {
			freeFast = *patch.FreeFast
		}
		if patch.FreeUltrafast != nil {
			freeUltrafast = *patch.FreeUltrafast
		}
	}
	if forceFast && forceUltrafast {
		return false, false, false, false, infraerrors.BadRequest("INVALID_GROUP_OPENAI_SERVICE_TIER", "forced Fast and Ultrafast cannot both be enabled")
	}
	return forceFast, forceUltrafast, freeFast, freeUltrafast, nil
}

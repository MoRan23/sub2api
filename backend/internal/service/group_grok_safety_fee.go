package service

import (
	"math"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// GroupGrokSafetyFeeUpdate is a partial administrative update. A nil fee keeps
// the current value, while a finite negative value explicitly clears it.
type GroupGrokSafetyFeeUpdate struct {
	Enabled *bool
	Fee     *float64
}

// ResolveGroupGrokSafetyFee is also applied while holding the group row lock,
// so background snapshots cannot overwrite omitted administrator settings.
func ResolveGroupGrokSafetyFee(platform string, enabled bool, fee *float64, patch *GroupGrokSafetyFeeUpdate) (bool, *float64, error) {
	if patch != nil {
		if patch.Enabled != nil {
			enabled = *patch.Enabled
		}
		if patch.Fee != nil {
			fee = patch.Fee
		}
	}
	if fee != nil {
		if math.IsNaN(*fee) || math.IsInf(*fee, 0) || *fee >= 1e12 {
			return false, nil, infraerrors.BadRequest("INVALID_GROK_SAFETY_FEE", "Grok safety fee must be a finite amount within decimal(20,8) range")
		}
		if *fee < 0 {
			fee = nil
		} else {
			amount := math.Round(*fee*1e8) / 1e8
			fee = &amount
		}
	}
	if platform != PlatformGrok {
		if enabled {
			return false, nil, infraerrors.BadRequest("INVALID_GROK_SAFETY_FEE_PLATFORM", "Grok safety fees can only be enabled for Grok groups")
		}
		return false, nil, nil
	}
	return enabled, fee, nil
}

package service

import infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"

// GroupDaybreakUpdate is transient update intent, never part of a cached group.
type GroupDaybreakUpdate struct {
	Blue *bool
	Red  *bool
}

// ResolveGroupDaybreak applies explicit preferences to the latest stored pair.
// Persistence repeats this validation while holding the group row lock.
func ResolveGroupDaybreak(platform string, blue, red bool, patch *GroupDaybreakUpdate) (bool, bool, error) {
	if platform != PlatformOpenAI && platform != PlatformComposite {
		return false, false, nil
	}
	if patch != nil {
		if patch.Blue != nil {
			blue = *patch.Blue
		}
		if patch.Red != nil {
			red = *patch.Red
		}
		if patch.Blue != nil && !*patch.Blue {
			return false, false, nil
		}
	}
	if red && !blue {
		return false, false, infraerrors.BadRequest("INVALID_GROUP_DAYBREAK_CONFIG", "Daybreak Red requires Daybreak Blue")
	}
	return blue, red, nil
}

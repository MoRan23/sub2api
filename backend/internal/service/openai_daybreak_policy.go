package service

import (
	"context"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
)

type openAIDaybreakPolicyContextKey struct{}
type openAIDaybreakInjectionDisabledKey struct{}

// A snapshot belongs to one physical HTTP attempt, never to a WS connection or
// to retained retry input. WS frames that have no snapshot read the live switch.
func freezeOpenAIDaybreakPolicyOnRequest(req *http.Request, settings *SettingService) {
	if req == nil {
		return
	}
	if _, frozen := req.Context().Value(openAIDaybreakPolicyContextKey{}).(bool); frozen {
		return
	}
	*req = *req.WithContext(context.WithValue(req.Context(), openAIDaybreakPolicyContextKey{}, settings.IsOpenAIDaybreakEnabled(req.Context())))
}

func openAIDaybreakPolicyEnabled(ctx context.Context, settings *SettingService) bool {
	if ctx != nil {
		if enabled, ok := ctx.Value(openAIDaybreakPolicyContextKey{}).(bool); ok {
			return enabled
		}
	}
	return settings.IsOpenAIDaybreakEnabled(ctx)
}

func withOpenAIDaybreakInjectionDisabled(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, openAIDaybreakInjectionDisabledKey{}, true)
}

func (s *OpenAIGatewayService) openAIDaybreakGroup(ctx context.Context, c *gin.Context) *Group {
	var group *Group
	if ctx != nil {
		group, _ = ctx.Value(ctxkey.Group).(*Group)
	}
	if group == nil && c != nil && c.Request != nil {
		group, _ = c.Request.Context().Value(ctxkey.Group).(*Group)
	}
	if !IsGroupContextValid(group) {
		return nil
	}
	if GetOpenAIClientTransport(c) == OpenAIClientTransportWS {
		// The handshake's group is only authority for the ID. It is not evidence
		// of the current preferences: a long-lived connection may outlive edits.
		if s == nil || s.schedulerSnapshot == nil {
			return nil
		}
		if ctx == nil {
			ctx = context.Background()
		}
		lookupCtx, cancel := context.WithTimeout(ctx, openAIRequestPolicyDBTimeout)
		defer cancel()
		fresh, err := s.schedulerSnapshot.GetGroupByIDLite(lookupCtx, group.ID)
		if err != nil || !IsGroupContextValid(fresh) || fresh.ID != group.ID {
			return nil
		}
		group = fresh
	}
	if group.Status != StatusActive || (group.Platform != PlatformOpenAI && group.Platform != PlatformComposite) {
		return nil
	}
	return group
}

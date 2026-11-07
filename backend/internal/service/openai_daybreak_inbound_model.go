package service

import (
	"context"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
)

// This value is private to a request/WS turn, captured before model mapping.
// A present but empty value must not fall back to a mapped model.
type openAIDaybreakInboundModelKey struct{}

func withOpenAIDaybreakInboundModel(ctx context.Context, model string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, openAIDaybreakInboundModelKey{}, strings.TrimSpace(model))
}

func withOpenAIDaybreakInboundModelFallback(ctx context.Context, model string) context.Context {
	if ctx != nil {
		if _, ok := ctx.Value(openAIDaybreakInboundModelKey{}).(string); ok {
			return ctx
		}
	}
	return withOpenAIDaybreakInboundModel(ctx, model)
}

func withOpenAIDaybreakHTTPInboundModel(ctx context.Context, c *gin.Context, fallback string) context.Context {
	if ctx != nil {
		if _, ok := ctx.Value(openAIDaybreakInboundModelKey{}).(string); ok {
			return ctx
		}
	}
	if model, ok := RequestedPublicModelFromContext(ctx); ok {
		return withOpenAIDaybreakInboundModel(ctx, model)
	}
	if c != nil && c.Request != nil {
		requestCtx := c.Request.Context()
		if model, ok := RequestedPublicModelFromContext(requestCtx); ok {
			return withOpenAIDaybreakInboundModel(ctx, model)
		}
		if model, ok := requestCtx.Value(ctxkey.Model).(string); ok {
			return withOpenAIDaybreakInboundModel(ctx, model)
		}
	}
	if ctx != nil {
		if model, ok := ctx.Value(ctxkey.Model).(string); ok {
			return withOpenAIDaybreakInboundModel(ctx, model)
		}
	}
	return withOpenAIDaybreakInboundModel(ctx, fallback)
}

func openAIDaybreakInboundModel(ctx context.Context, fallback string) string {
	if ctx != nil {
		if model, ok := ctx.Value(openAIDaybreakInboundModelKey{}).(string); ok {
			return model
		}
	}
	// Direct synthetic requests without a mapping stage use their own model.
	return strings.TrimSpace(fallback)
}

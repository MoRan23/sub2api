package service

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

type openAIBackendIngressSourceKey struct{}

type openAIBackendIngressSource struct {
	IDs []string
}

// WithOpenAIBackendIngressSource captures only opaque-source lookup keys before
// protocol recovery can remove them. It never retains the request body or a
// cookie/token value. Validation waits for the actual OAuth token snapshot.
func WithOpenAIBackendIngressSource(ctx context.Context, body []byte, headers http.Header) context.Context {
	if _, exists := ctx.Value(openAIBackendIngressSourceKey{}).(openAIBackendIngressSource); exists {
		return ctx
	}
	ids := backendOpaqueValues(body)
	if previous := gjson.GetBytes(body, "previous_response_id").String(); previous != "" {
		ids = append(ids, "response:"+previous)
	}
	if state := headers.Get("x-codex-turn-state"); state != "" {
		ids = append(ids, "state:"+excelStateDigest(state))
	}
	return context.WithValue(ctx, openAIBackendIngressSourceKey{}, openAIBackendIngressSource{IDs: ids})
}

func withOpenAIBackendIngressSource(ctx context.Context, c *gin.Context, body []byte) context.Context {
	var headers http.Header
	if c != nil && c.Request != nil {
		headers = c.Request.Header
	}
	return WithOpenAIBackendIngressSource(ctx, body, headers)
}

func (s *OpenAIGatewayService) validateOpenAIBackendIngressSource(ctx context.Context, account *Account, scope string) error {
	if !openAIBackendProvenanceRequired(account) {
		return nil
	}
	if s == nil || s.excelState == nil {
		return ErrOpenAIExcelStateUnavailable
	}
	source, _ := ctx.Value(openAIBackendIngressSourceKey{}).(openAIBackendIngressSource)
	for _, id := range source.IDs {
		if _, err := s.excelState.get(ctx, scope, "provenance", id); err != nil {
			return ErrOpenAIBackendHistoryUnavailable
		}
	}
	return nil
}

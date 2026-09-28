package service

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// A tool result is still caller-controlled. Keeping its data URLs inline must
// not grant it access to another tenant/account/route's uploaded file IDs.
func TestExcelAttachmentsToolOutputCannotBypassFileOwnership(t *testing.T) {
	for _, field := range []string{"output", "content"} {
		for _, kind := range []string{"function_call_output", "custom_tool_call_output"} {
			t.Run(kind+"/"+field, func(t *testing.T) {
				body := []byte(`{"model":"gpt-6-astra","input":[{"type":"` + kind + `","call_id":"call-local","` + field + `":[{"type":"input_image","file_id":"another-session-upload"}]}]}`)
				req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://bps.openai.com/basispoints/api/responses", bytes.NewReader(body))
				require.NoError(t, err)
				gateway := &OpenAIGatewayService{}
				_, err = gateway.prepareOpenAIExcelAttachments(req, "", &Account{ID: 7}, "tenant-a/account-a/session-a")
				require.Error(t, err)
			})
		}
	}
}

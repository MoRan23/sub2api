package handler

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestValidateStandaloneFunctionOutputRequest(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		accepted    bool
	}{
		{"approved plan", `{"type":"function_call_output","namespace":"agent","name":"proposed_plan","id":"fco_plan","output":"plan"}`, true},
		{"named context", `{"type":"function_call_output","name":"external_context","output":[{"type":"input_text","text":"context"}]}`, true},
		{"null call id", `{"type":"function_call_output","call_id":null,"name":"context","output":"context"}`, true},
		{"empty call id is not standalone", `{"type":"function_call_output","call_id":"","name":"context","output":"context"}`, false},
		{"invalid namespace is not standalone", `{"type":"function_call_output","name":"context","namespace":1,"output":"context"}`, false},
		{"invalid output is not standalone", `{"type":"function_call_output","name":"context","output":null}`, false},
		{"standalone cannot cover orphan", `{"type":"function_call_output","name":"context","output":"context"},{"type":"function_call_output","call_id":"call_missing","output":"orphan"}`, false},
		{"standalone cannot cover missing id", `{"type":"function_call_output","name":"context","output":"context"},{"type":"function_call_output","output":"orphan"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			h := &OpenAIGatewayHandler{}
			require.Equal(t, tc.accepted, h.validateFunctionCallOutputRequest(c, []byte(`{"input":[`+tc.input+`]}`), zap.NewNop()))
			if tc.accepted {
				require.False(t, c.Writer.Written())
			} else {
				require.Equal(t, 400, w.Code)
			}
		})
	}
}

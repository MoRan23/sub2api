package codexcontext

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUntrustedOutputSharesForwardAndAuditBoundary(t *testing.T) {
	for _, name := range []string{"browser_info", "goal_objective", "untrusted_context"} {
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				kind, namespace, output string
				callID                  any
				hasCallID, valid        bool
			}{
				{"function_call_output", "agent", "external text", nil, false, true},
				{"function_call_output", "agent", "external text", " \t", true, true},
				{"function_call_output", "agent", "external text", nil, true, false},
				{"function_call_output", "agent", "external text", "call_real", true, false},
				{"function_call_output", "agent", "external text", 42, true, false},
				{"function_call_output", "other", "external text", nil, false, false},
				{"message", "agent", "external text", nil, false, false},
				{"function_call_output", "agent", " \t\n", nil, false, false},
			} {
				text, valid := UntrustedOutput(tc.kind, tc.namespace, name, tc.output, tc.callID, tc.hasCallID)
				require.Equal(t, tc.valid, valid, "%+v", tc)
				if valid {
					require.Equal(t, "<external_"+name+">"+tc.output+"</external_"+name+">", text)
				} else {
					require.Empty(t, text)
				}
			}
		})
	}
}

package codexcontext

import "testing"

func TestUntrustedContextNames(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		valid        bool
	}{
		{"ide_context", "workspace", true},
		{"ide_context", " ", false},
		{"untrusted_content", "", true},
		{"proposed_plan", " \n", true},
		{"proposed_plan", "", false},
	} {
		t.Run(tc.name+tc.output, func(t *testing.T) {
			_, valid := UntrustedOutput("function_call_output", "agent", tc.name, tc.output, nil, false)
			if valid != tc.valid {
				t.Fatalf("valid = %v, want %v", valid, tc.valid)
			}
		})
	}
}

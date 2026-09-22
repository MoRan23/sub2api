package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodexTelemetryShellFromMultipleEnvironments(t *testing.T) {
	for _, test := range []struct {
		name, content, want string
	}{
		{"legacy", `<shell>/bin/zsh</shell>`, "zsh"},
		{"single remote", `<environments><environment id="remote"><cwd>/home/test</cwd><shell>/bin/bash</shell></environment></environments>`, "bash"},
		{"primary remote", `<environments><environment id="local" primary="false"><shell>powershell</shell></environment><environment id="remote" primary="true"><shell>/usr/bin/fish</shell></environment></environments>`, "fish"},
		{"primary first", `<environments><environment id="local" primary="true"><shell>C:\Windows\System32\cmd.exe</shell></environment><environment id="remote" primary="false"><shell>bash</shell></environment></environments>`, "cmd"},
		{"unavailable secondary", `<environments><environment id="local" primary="true"><shell>pwsh</shell></environment><environment id="remote" status="unavailable" /></environments>`, "pwsh"},
		{"explicit available", `<environments><environment id="remote"><status>available</status><shell>bash</shell></environment></environments>`, "bash"},
		{"two primaries", `<environments><environment id="a" primary="true"><shell>bash</shell></environment><environment id="b" primary="true"><shell>zsh</shell></environment></environments>`, ""},
		{"two unmarked", `<environments><environment id="a"><shell>bash</shell></environment><environment id="b"><shell>bash</shell></environment></environments>`, ""},
		{"explicit nonprimary", `<environments><environment id="a" primary="false"><shell>bash</shell></environment></environments>`, ""},
		{"unavailable primary", `<environments><environment id="a" primary="true" status="unavailable"><shell>bash</shell></environment><environment id="b" primary="false"><shell>zsh</shell></environment></environments>`, ""},
		{"starting primary", `<environments><environment id="a" primary="true"><status>starting</status><shell>bash</shell></environment></environments>`, ""},
		{"failed primary", `<environments><environment id="a" primary="true"><status>failed</status><shell>bash</shell></environment></environments>`, ""},
		{"missing primary shell", `<environments><environment id="a" primary="true"><cwd>/home/test</cwd></environment><environment id="b" primary="false"><shell>bash</shell></environment></environments>`, ""},
		{"duplicate id", `<environments><environment id="a" primary="true"><shell>bash</shell></environment><environment id="a" primary="false"><shell>zsh</shell></environment></environments>`, ""},
		{"duplicate shell", `<environments><environment id="a"><shell>bash</shell><shell>zsh</shell></environment></environments>`, ""},
		{"duplicate primary attribute", `<environments><environment id="a" primary="true" primary="false"><shell>bash</shell></environment></environments>`, ""},
		{"invalid primary", `<environments><environment id="a" primary="maybe"><shell>bash</shell></environment></environments>`, ""},
		{"conflicting status", `<environments><environment id="a" status="available"><status>failed</status><shell>bash</shell></environment></environments>`, ""},
		{"mixed formats", `<shell>powershell</shell><environments><environment id="a"><shell>bash</shell></environment></environments>`, ""},
		{"multiple containers", `<environments><environment id="a"><shell>bash</shell></environment></environments><environments><environment id="b"><shell>zsh</shell></environment></environments>`, ""},
		{"nested example", `<example><environments><environment id="a"><shell>bash</shell></environment></environments></example>`, ""},
		{"nested shell subtree", `<environments><environment id="a"><shell><example>bash</example></shell></environment></environments>`, ""},
		{"arbitrary command", `<environments><environment id="a"><shell>bash -c private-command</shell></environment></environments>`, ""},
		{"namespace", `<environments><environment id="a"><shell xmlns="urn:example">bash</shell></environment></environments>`, ""},
		{"unavailable shell", `<environments><environment id="a"><shell status="unavailable">bash</shell></environment></environments>`, ""},
		{"missing id", `<environments><environment><shell>bash</shell></environment></environments>`, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			text := "<environment_context>" + test.content + "</environment_context>"
			require.Equal(t, test.want, codexTelemetryShellFromEnvironment(text))
		})
	}
	t.Run("environment limit", func(t *testing.T) {
		var content strings.Builder
		content.WriteString(`<environment_context><environments>`)
		for i := 0; i <= openAIRequestOSEnvironmentLimit; i++ {
			fmt.Fprintf(&content, `<environment id="env%d" primary="%t"><shell>bash</shell></environment>`, i, i == 0)
		}
		content.WriteString(`</environments></environment_context>`)
		require.Empty(t, codexTelemetryShellFromEnvironment(content.String()))
	})
}

func TestCodexTelemetryRemoteShellPreservesWireSystem(t *testing.T) {
	account := osIdentityTestAccount(t, 123)
	for _, os := range OpenAIOAuthOSFamilies() {
		t.Run(os, func(t *testing.T) {
			ua := account.OpenAIOAuthOSProfiles.Profiles[os].UserAgent
			headers := http.Header{"User-Agent": {ua}}
			body, err := json.Marshal(map[string]any{"input": []map[string]string{{
				"role": "user", "content": `<environment_context><environments><environment id="remote" primary="true"><cwd>/home/project</cwd><shell>/usr/bin/fish</shell></environment><environment id="local" primary="false"><shell>powershell</shell></environment></environments></environment_context>`,
			}}})
			require.NoError(t, err)
			original := string(body)
			input := codexTelemetryInputFromWire(account, headers, body, "", false, CodexWireProfile{})
			require.Equal(t, "fish", input.Shell)
			require.Equal(t, os, input.OSFamily)
			require.Equal(t, ua, input.UserAgent)
			require.Equal(t, original, string(body))
			require.Equal(t, ua, headers.Get("User-Agent"))
		})
	}
}

func TestCodexTelemetryMultiEnvironmentEvidenceBoundaries(t *testing.T) {
	const env = `<environment_context><environments><environment id="remote"><shell>bash</shell></environment></environments></environment_context>`
	user := func(content string) map[string]any {
		return map[string]any{"role": "user", "content": content}
	}
	for _, test := range []struct {
		name  string
		input []map[string]any
		want  string
	}{
		{"current", []map[string]any{user(env)}, "bash"},
		{"quoted", []map[string]any{user("Example: " + env)}, ""},
		{"fenced", []map[string]any{user("```xml\n" + env + "\n```")}, ""},
		{"historical", []map[string]any{user(env), {"role": "assistant", "content": "done"}, user("next")}, ""},
		{"skill", []map[string]any{{"role": "user", "content": env, "metadata": map[string]any{"content_item_kinds": []string{"skills.skill"}}}}, ""},
		{"newest conflict blocks old", []map[string]any{user(env), user(`<environment_context><environments><environment id="other" primary="false"><shell>zsh</shell></environment></environments></environment_context>`)}, ""},
		{"text limit", []map[string]any{user("<environment_context>" + strings.Repeat(" ", openAIRequestOSTextLimit) + "<shell>bash</shell></environment_context>")}, ""},
		{"node limit", []map[string]any{user("<environment_context>" + strings.Repeat("<node/>", openAIRequestOSXMLNodeLimit) + "<shell>bash</shell></environment_context>")}, ""},
		{"depth limit", []map[string]any{user("<environment_context>" + strings.Repeat("<node>", openAIRequestOSXMLDepthLimit) + strings.Repeat("</node>", openAIRequestOSXMLDepthLimit) + "<shell>bash</shell></environment_context>")}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, carrier := range []string{"input", "messages"} {
				body, err := json.Marshal(map[string]any{carrier: test.input})
				require.NoError(t, err)
				require.Equal(t, test.want, codexTelemetryShellFromBody(body), carrier)
			}
		})
	}
}

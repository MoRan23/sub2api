package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAICodexClientWindowPathsRestoreAfterIndependentCompacts(t *testing.T) {
	for _, transport := range []string{"http", "oauth_passthrough", "websocket", "websocket_http_bridge"} {
		t.Run(transport, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{}
			svc, _ := newOpenAIIdentityPathService(t, true, upstream)
			account := newOpenAIIdentityPathOAuthAccount(927500)
			session, clientWindow := codexClientWindowPathUUID(t), codexClientWindowPathUUID(t)
			ws := transport == "websocket" || transport == "websocket_http_bridge"
			initialBody := codexClientWindowPathBody(t, ws, session, 0, clientWindow, clientWindow, "", true)
			initialContext, _ := newOpenAIIdentityPathContext(t, "/v1/responses", initialBody, 927501)
			_, initialPlan := codexClientWindowPathBuild(t, transport, svc, upstream, account, initialContext, initialBody)

			// Compact requests do not carry a normal-turn client window signal.
			// Their successful deliveries can advance the main window several
			// times while its client binding still references the original window.
			current := initialPlan.Window
			for range 2 {
				digest, err := OpenAICodexCompactTurnDigest("restore-path-secret", initialPlan.CredentialOwnerNamespace, initialPlan.APIKeyID, current, codexClientWindowPathUUID(t))
				require.NoError(t, err)
				committed, err := svc.CommitOpenAICodexWindowSnapshot(context.Background(), initialPlan.WindowMappingKey, current, digest)
				require.NoError(t, err)
				require.Equal(t, OpenAICodexWindowCommitAdvanced, committed.Status)
				current = committed.Snapshot
			}
			require.Equal(t, uint64(2), current.Number)

			// A new request after Codex restores the original client window must
			// resolve a fresh server generation, without reviving its old UUID.
			restoredBody := codexClientWindowPathBody(t, ws, session, 0, clientWindow, clientWindow, "", true)
			restoredContext, _ := newOpenAIIdentityPathContext(t, "/v1/responses", restoredBody, 927501)
			outbound, restored := codexClientWindowPathBuild(t, transport, svc, upstream, account, restoredContext, restoredBody)
			require.Equal(t, uint64(3), restored.Window.Number)
			require.Equal(t, initialPlan.Window.FirstContextWindowID, restored.Window.FirstContextWindowID)
			require.Equal(t, current.ContextWindowID, restored.Window.PreviousContextWindowID)
			require.NotEqual(t, current.ContextWindowID, restored.Window.ContextWindowID)
			require.NotEqual(t, initialPlan.Window.ContextWindowID, restored.Window.ContextWindowID)
			requireCodexClientWindowPathProjection(t, outbound, restored, clientWindow)

			duplicateContext, _ := newOpenAIIdentityPathContext(t, "/v1/responses", restoredBody, 927501)
			_, duplicate := codexClientWindowPathBuild(t, transport, svc, upstream, account, duplicateContext, restoredBody)
			require.Equal(t, restored.Window, duplicate.Window)

			// A physical retry retains the already frozen plan; it must not be
			// confused with the fresh ingress that performed recovery above.
			retriedBody, retried := codexClientWindowPathBuild(t, transport, svc, upstream, account, initialContext, initialBody)
			require.Equal(t, initialPlan.Window, retried.Window)
			requireCodexClientWindowPathProjection(t, retriedBody, retried, clientWindow)
			stored, err := svc.ResolveOpenAICodexWindowSnapshot(context.Background(), restored.WindowMappingKey, restored.Window.ThreadID, codexClientWindowPathUUID(t))
			require.NoError(t, err)
			require.Equal(t, restored.Window, stored)
		})
	}
}

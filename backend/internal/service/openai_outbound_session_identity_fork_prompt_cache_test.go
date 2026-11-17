package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Native ephemeral forks can retain their parent's session header and cache
// key while supplying a different flat metadata session. The unified identity
// facade already gives the canonical header precedence over flat metadata;
// preserve that cache continuity without restoring the old per-field mapper.
func TestOpenAIOAuthIdentityForkKeepsCanonicalParentPromptCacheKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mode := range []OpenAIOAuthIdentityProjectionMode{
		OpenAIOAuthIdentityProjectionRegular,
		OpenAIOAuthIdentityProjectionPassthrough,
		OpenAIOAuthIdentityProjectionCompact,
	} {
		t.Run(string(mode), func(t *testing.T) {
			account := &Account{ID: 73901, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
			otherAccount := &Account{ID: 73902, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
			svc := &OpenAIGatewayService{
				cfg:         &config.Config{JWT: config.JWTConfig{Secret: "fork-prompt-cache-facade-test"}},
				accountRepo: newAuthorizedOpenAIOAuthTestRepo(account, otherAccount),
			}
			project := func(target *Account, apiKeyID int64, metadataSession, cacheKey string) (OpenAIOAuthIdentityPlan, http.Header, []byte) {
				t.Helper()
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
				c.Request.Header.Set("session-id", "parent-session")
				c.Set("api_key", &APIKey{ID: apiKeyID})
				body := []byte(fmt.Sprintf(`{"model":"gpt-5.6-sol","input":[{"type":"message","role":"user","content":"hello"}],"prompt_cache_key":%q,"client_metadata":{"session_id":%q,"unknown":"keep"},"untouched":1.2300}`, cacheKey, metadataSession))
				original := string(body)
				capture := CaptureOpenAIOAuthIdentity(c, body, "")
				require.Equal(t, "parent-session", capture.Logical.SessionKey)
				require.Equal(t, OpenAIOutboundSessionLogicalKeySourceHeaderSession, capture.Logical.Source)
				if cacheKey == "parent-session" {
					require.Equal(t, OpenAICodexPromptCacheKeyDefault, capture.PromptCacheKey.Kind)
				} else {
					require.Equal(t, OpenAICodexPromptCacheKeyOverride, capture.PromptCacheKey.Kind)
				}
				plan, err := svc.GetOrResolveOpenAIOAuthOutboundIdentity(context.Background(), c, target, capture, OpenAIOAuthIdentityPlanOptions{
					TurnIdentityEnabled: true,
					ProjectionMode:      mode,
					InstallationPolicy:  OpenAIOAuthInstallationPreserve,
				}, nil)
				require.NoError(t, err)
				require.True(t, plan.PromptCacheKey.Enabled)
				headers := c.Request.Header.Clone()
				out, err := ApplyOpenAIOAuthIdentityPlan(headers, body, plan)
				require.NoError(t, err)
				require.Equal(t, original, string(body), "projection must not mutate the captured ingress bytes")
				require.Equal(t, plan.PromptCacheKey.Value, gjson.GetBytes(out, "prompt_cache_key").String())
				if mode == OpenAIOAuthIdentityProjectionPassthrough {
					require.Contains(t, string(out), `"untouched":1.2300`)
				}
				if mode == OpenAIOAuthIdentityProjectionCompact {
					require.False(t, gjson.GetBytes(out, "client_metadata").Exists())
				} else {
					require.Equal(t, "keep", gjson.GetBytes(out, "client_metadata.unknown").String())
				}
				return plan, headers, out
			}

			parent, parentHeaders, _ := project(account, 79, "parent-session", "parent-session")
			fork, forkHeaders, _ := project(account, 79, "fork-session", "parent-session")
			require.NotEqual(t, "parent-session", parent.PromptCacheKey.Value)
			require.Equal(t, parent.TurnIdentity.SessionID, parent.PromptCacheKey.Value)
			require.Equal(t, parent.PromptCacheKey.Value, fork.PromptCacheKey.Value)
			require.Equal(t, parentHeaders.Get("session-id"), forkHeaders.Get("session-id"))
			require.NotEqual(t, parent.RequestTurn.ID, fork.RequestTurn.ID, "a fork remains a new ingress turn")

			retry, _, _ := project(account, 79, "fork-session", "parent-session")
			require.Equal(t, fork.PromptCacheKey.Value, retry.PromptCacheKey.Value)
			custom, _, _ := project(account, 79, "fork-session", "caller-cache-key")
			require.Equal(t, OpenAICodexPromptCacheKeyOverride, custom.PromptCacheKey.Kind)
			require.NotEqual(t, parent.PromptCacheKey.Value, custom.PromptCacheKey.Value)
			failover, _, _ := project(otherAccount, 79, "fork-session", "parent-session")
			require.NotEqual(t, parent.PromptCacheKey.Value, failover.PromptCacheKey.Value)
			otherKey, _, _ := project(account, 80, "fork-session", "parent-session")
			require.NotEqual(t, parent.PromptCacheKey.Value, otherKey.PromptCacheKey.Value)
		})
	}
}

func TestOpenAIOAuthIdentityForkDoesNotAliasConflictingCanonicalMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("session-id", "parent-session")
	body := []byte(`{"client_metadata":{"x-codex-turn-metadata":"{\"session_id\":\"fork-session\",\"thread_id\":\"fork-thread\"}"},"prompt_cache_key":"parent-session"}`)
	capture := CaptureOpenAIOAuthIdentity(c, body, "")
	require.Equal(t, "fork-session", capture.Logical.SessionKey)
	require.Equal(t, "fork-thread", capture.Logical.ThreadKey)
	require.Equal(t, OpenAICodexPromptCacheKeyOverride, capture.PromptCacheKey.Kind)
	for _, alias := range capture.Aliases {
		require.NotEqual(t, "parent-session", alias.SessionKey, "a conflicting header must not claim the canonical fork identity")
	}
}

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestResponsesBridgeContinuationIsClientErrorBeforeUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"claude-sonnet-4-5","previous_response_id":"resp_prior","reasoning":{"effort":"low"},"input":"continue"}`)
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "fixture-key"}}
	for name, forward := range map[string]func(*gin.Context) error{
		"openai chat fallback": func(c *gin.Context) error {
			svc := &OpenAIGatewayService{cfg: &config.Config{}}
			_, err := svc.forwardResponsesViaRawChatCompletions(context.Background(), c, account, body, time.Now())
			return err
		},
		"openai native anthropic": func(c *gin.Context) error {
			svc := &OpenAIGatewayService{cfg: &config.Config{}}
			_, err := svc.forwardResponsesViaNativeAnthropic(context.Background(), c, account, body, "")
			return err
		},
		"anthropic platform": func(c *gin.Context) error {
			svc := &GatewayService{cfg: &config.Config{}}
			anthropicAccount := *account
			anthropicAccount.Platform = PlatformAnthropic
			_, err := svc.ForwardAsResponses(context.Background(), c, &anthropicAccount, body, nil)
			return err
		},
		"responses shaped chat": func(c *gin.Context) error {
			svc := &OpenAIGatewayService{}
			_, err := svc.convertResponsesShapedChatBody(c, body)
			return err
		},
		"antigravity": func(c *gin.Context) error {
			svc := newAntigravityCompatService(config.GatewayConfig{}, &queuedHTTPUpstreamStub{})
			_, err := svc.ForwardAsResponses(context.Background(), c, newAntigravityCompatAccount(AccountTypeOAuth), body, nil)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body)))
			err := forward(c)
			require.ErrorContains(t, err, "previous_response_id")
			var failover *UpstreamFailoverError
			require.NotErrorAs(t, err, &failover)
			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Contains(t, recorder.Body.String(), "invalid_request_error")
			require.Contains(t, recorder.Body.String(), "resend the full conversation")
		})
	}
}

package service

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
)

const accountTestResponseInfoContextKey = "account_test_response_info"

// This summary belongs only to the manual test's physical response. It never
// publishes cache tokens, collector demand, or business observation history.
type accountTestResponseInfo struct {
	UpstreamModel  string                    `json:"upstream_model,omitempty"`
	CodexTurnState *accountTestTurnStateInfo `json:"codex_turn_state,omitempty"`
	accountType    string
	issuedAt       time.Time
	sent           bool
}

type accountTestTurnStateInfo struct {
	Length           int    `json:"length"`
	ExpectedLength   int    `json:"expected_length"`
	Shape            string `json:"shape"`
	ValidationReason string `json:"validation_reason,omitempty"`
	Source           string `json:"source,omitempty"`
}

func beginAccountTestResponseInfo(c *gin.Context, credentialAccount *Account, headers http.Header) {
	info := &accountTestResponseInfo{}
	if IsCodexTurnStateAccount(credentialAccount) {
		info.accountType = CodexTurnStateAccountTypeForAccount(credentialAccount)
		info.CodexTurnState = &accountTestTurnStateInfo{Shape: "missing"}
		switch info.accountType {
		case "personal":
			info.CodexTurnState.ExpectedLength = 292
		case "team_business":
			info.CodexTurnState.ExpectedLength = 332
		}
		for key, values := range headers {
			if strings.EqualFold(key, "x-codex-turn-state") {
				for _, token := range values {
					info.observeToken(token, "header")
				}
			}
		}
	}
	c.Set(accountTestResponseInfoContextKey, info)
}

func getAccountTestResponseInfo(c *gin.Context) *accountTestResponseInfo {
	value, _ := c.Get(accountTestResponseInfoContextKey)
	info, _ := value.(*accountTestResponseInfo)
	return info
}

func observeAccountTestResponseInfo(c *gin.Context, payload []byte) {
	info := getAccountTestResponseInfo(c)
	if info == nil || info.sent {
		return
	}
	var event struct {
		Model    string `json:"model"`
		Response struct {
			Model string `json:"model"`
		} `json:"response"`
	}
	if json.Unmarshal(payload, &event) != nil {
		return
	}
	model := strings.TrimSpace(event.Response.Model)
	if model == "" {
		model = strings.TrimSpace(event.Model)
	}
	// Never substitute the requested model when upstream omits its model.
	if model != "" && len(model) <= 256 && strings.IndexFunc(model, unicode.IsControl) < 0 {
		info.UpstreamModel = model
	}
	for _, token := range CodexTurnStateTokensFromEvent(payload) {
		info.observeToken(token, "metadata")
	}
}

func (info *accountTestResponseInfo) observeToken(token, source string) {
	if info.CodexTurnState == nil || token == "" {
		return
	}
	shape, err := ParseCodexTurnState(token, info.accountType, time.Now())
	candidate := accountTestTurnStateInfo{
		Length: len(token), ExpectedLength: info.CodexTurnState.ExpectedLength,
		Shape: shape.Shape, Source: source,
	}
	if err != nil {
		candidate.Shape = "unknown"
		candidate.ValidationReason = err.Error() // parser emits fixed reason codes only
	}
	// A response can contain both header and metadata candidates. Match cache
	// admission's target-first policy without retaining any token or token hash.
	rank := func(shape string) int {
		switch shape {
		case CodexTurnStateShapeTarget:
			return 3
		case CodexTurnStateShapeExtended:
			return 2
		case "unknown":
			return 1
		default:
			return 0
		}
	}
	currentRank, candidateRank := rank(info.CodexTurnState.Shape), rank(candidate.Shape)
	if candidateRank < currentRank || (candidateRank == currentRank && shape.IssuedAt.Before(info.issuedAt)) {
		return
	}
	info.CodexTurnState, info.issuedAt = &candidate, shape.IssuedAt
}

func (s *AccountTestService) sendAccountTestResponseInfo(c *gin.Context) {
	if info := getAccountTestResponseInfo(c); info != nil && !info.sent {
		info.sent = true
		s.sendEvent(c, TestEvent{Type: "response_info", Data: info})
	}
}

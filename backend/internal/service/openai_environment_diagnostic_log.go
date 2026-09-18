package service

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const openAIEnvironmentDiagnosticLabelLimit = 128

type openAIEnvironmentDiagnosticCandidate struct {
	SourcePath        string                             `json:"source_path"`
	Path              string                             `json:"path"`
	Eligible          bool                               `json:"eligible"`
	Current           bool                               `json:"current"`
	Source            string                             `json:"source"`
	EnvironmentSource string                             `json:"environment_source,omitempty"`
	ParseStatus       string                             `json:"parse_status"`
	ParseReason       string                             `json:"parse_reason,omitempty"`
	ConversionStatus  string                             `json:"conversion_status,omitempty"`
	ConversionReason  string                             `json:"conversion_reason,omitempty"`
	Timezone          string                             `json:"timezone,omitempty"`
	CurrentDate       string                             `json:"current_date,omitempty"`
	Diagnostic        *openAIEnvironmentSourceDiagnostic `json:"diagnostic,omitempty"`
}

// Log at the physical send boundary, independently of the fingerprint observer.
// Only frozen source diagnostics are inspected; outgoing content is used solely
// for bounded, validated correlation metadata and never to select new sources.
func (s *OpenAIGatewayService) logOpenAIEnvironmentSourceDiagnostic(c *gin.Context, account *Account,
	state *RequestTimezoneState, body []byte, outbound http.Header, transport string) {
	if c == nil || account == nil || !account.IsOpenAIOAuth() || state == nil || len(body) == 0 {
		return
	}
	candidates := make([]openAIEnvironmentDiagnosticCandidate, 0, openAIRequestTimezoneItemLimit)
	problemCount := 0
	for _, source := range state.projectionSources {
		occurrence := source.occurrence
		if !occurrence.environment || (occurrence.eligible && occurrence.item.Status == "valid") {
			continue
		}
		problemCount++
		if len(candidates) >= openAIRequestTimezoneItemLimit {
			continue
		}
		item := occurrence.item
		candidate := openAIEnvironmentDiagnosticCandidate{
			SourcePath: item.Path, Path: item.Path, Eligible: occurrence.eligible, Current: item.Current,
			Source: item.Source, EnvironmentSource: item.EnvironmentSource,
			ParseStatus: item.Status, ParseReason: item.Reason,
			Diagnostic: occurrence.environmentDiagnostic,
		}
		if candidate.Diagnostic != nil {
			candidate.SourcePath = candidate.Diagnostic.Path
		}
		for _, conversion := range state.Conversions {
			if conversion.Path == item.Path {
				candidate.ConversionStatus, candidate.ConversionReason = conversion.Status, conversion.Reason
				break
			}
		}
		if validRequestTimezone(item.Value) {
			candidate.Timezone = item.Value
		}
		if parsed, err := time.Parse("2006-01-02", item.CurrentDate); err == nil && parsed.Year() > 0 && parsed.Format("2006-01-02") == item.CurrentDate {
			candidate.CurrentDate = item.CurrentDate
		}
		candidates = append(candidates, candidate)
	}
	scanIncomplete := state.projectionScanStatus != "" && state.projectionScanStatus != "complete"
	if problemCount == 0 && !scanIncomplete {
		return
	}

	// WS headers describe the socket's first handshake, which can belong to an
	// earlier turn. A current frame's body is its only identity authority here.
	profile := captureCodexWireProfile(nil, body, "")
	if transport == "http" {
		profile = finalFingerprintCodexWireProfile(outbound, body)
	}
	sessionID, threadID := codexTelemetryUUID(profile.SessionID), codexTelemetryUUID(profile.ThreadID)
	if transport == "http" {
		sessionID = openAIEnvironmentDiagnosticHeaderUUID(outbound, sessionID, "session-id", "session_id")
		threadID = openAIEnvironmentDiagnosticHeaderUUID(outbound, threadID, "thread-id", "thread_id", "conversation-id", "conversation_id", "x-client-request-id")
	}
	subagent := profile.SubagentHeader
	if transport == "http" && subagent == "" {
		subagent = outbound.Get("x-openai-subagent")
	}
	targetTimezone := ""
	if validRequestTimezone(state.Target.Timezone) {
		targetTimezone = state.Target.Timezone
	}
	ctx := context.Background()
	if c.Request != nil {
		ctx = c.Request.Context()
	}
	logger.FromContext(ctx).Info("openai.environment_source_diagnostic",
		zap.Int("schema_version", 1),
		zap.Int64("account_id", account.ID),
		zap.String("transport", transport),
		zap.Time("accepted_at", state.AcceptedAt),
		zap.String("target_timezone", targetTimezone),
		zap.Bool("timezone_conversion_enabled", state.Policy.TimezoneConversionEnabled),
		zap.Bool("passthrough_timezone_conversion_enabled", state.Policy.PassthroughTimezoneConversionEnabled),
		zap.Bool("passthrough", state.passthrough),
		zap.String("scan_status", state.projectionScanStatus),
		zap.Bool("scan_incomplete", scanIncomplete),
		zap.Int("candidate_count", problemCount),
		zap.Int("candidate_limit", openAIRequestTimezoneItemLimit),
		zap.Int("omitted_candidates", problemCount-len(candidates)),
		zap.Bool("truncated", state.projectionScanStatus == "limited" || problemCount > len(candidates)),
		zap.String("observed_session_id", sessionID),
		zap.String("observed_thread_id", threadID),
		zap.String("observed_turn_id", codexTelemetryUUID(profile.TurnID.Value)),
		zap.String("request_kind", openAIEnvironmentDiagnosticLabel(string(profile.RequestKind))),
		zap.String("subagent", openAIEnvironmentDiagnosticLabel(subagent)),
		zap.String("subagent_kind", openAIEnvironmentDiagnosticLabel(profile.SubagentKind)),
		zap.String("thread_source", openAIEnvironmentDiagnosticLabel(profile.ThreadSource)),
		zap.String("turn_trigger", openAIEnvironmentDiagnosticLabel(profile.TurnTrigger)),
		zap.Any("candidates", candidates),
	)
}

// Explicit malformed or conflicting header identities stay unknown; do not
// conceal them by falling back to a different body carrier.
func openAIEnvironmentDiagnosticHeaderUUID(headers http.Header, fallback string, names ...string) string {
	present, resolved := false, ""
	for _, name := range names {
		for _, raw := range headerValuesCaseInsensitive(headers, name) {
			present = true
			value := codexTelemetryUUID(raw)
			if value == "" || (resolved != "" && resolved != value) {
				return ""
			}
			resolved = value
		}
	}
	if present {
		return resolved
	}
	return fallback
}

func openAIEnvironmentDiagnosticLabel(value string) string {
	if value == "" || len(value) > openAIEnvironmentDiagnosticLabelLimit || strings.TrimSpace(value) != value {
		return ""
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '_' && char != '-' && char != '.' && char != '/' {
			return ""
		}
	}
	return value
}

package service

import (
	"strings"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

const codexMetadataProfileStringMaxBytes = 256

// CodexMetadataProfile is a request-level value snapshot of the process
// configuration that may fill missing generated compatibility metadata. Wire
// finalization receives this value explicitly and never reads mutable config.
type CodexMetadataProfile struct {
	AgentName                    string
	Sandbox                      string
	SandboxMode                  string
	AutoReviewEnabled            bool
	TurnMetadataIncludesToolInfo bool
}

func defaultCodexMetadataProfile() CodexMetadataProfile {
	defaults := config.DefaultGatewayCodexMetadataConfig()
	return CodexMetadataProfile{
		AgentName:                    defaults.AgentName,
		Sandbox:                      defaults.Sandbox,
		SandboxMode:                  defaults.SandboxMode,
		AutoReviewEnabled:            defaults.AutoReviewEnabled,
		TurnMetadataIncludesToolInfo: defaults.TurnMetadataIncludesToolInfo,
	}
}

func validCodexMetadataProfileString(value string) bool {
	return value != "" && len(value) <= codexMetadataProfileStringMaxBytes &&
		utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n")
}

func (profile CodexMetadataProfile) normalized() CodexMetadataProfile {
	defaults := defaultCodexMetadataProfile()
	profile.AgentName = strings.TrimSpace(profile.AgentName)
	profile.Sandbox = strings.TrimSpace(profile.Sandbox)
	profile.SandboxMode = strings.TrimSpace(profile.SandboxMode)
	if !validCodexMetadataProfileString(profile.AgentName) {
		profile.AgentName = defaults.AgentName
	}
	if !validCodexMetadataProfileString(profile.Sandbox) {
		profile.Sandbox = defaults.Sandbox
	}
	if !validCodexMetadataProfileString(profile.SandboxMode) {
		profile.SandboxMode = defaults.SandboxMode
	}
	return profile
}

func (s *OpenAIGatewayService) codexMetadataProfileSnapshot() CodexMetadataProfile {
	configured := config.DefaultGatewayCodexMetadataConfig()
	if s != nil && s.cfg != nil {
		configured = s.cfg.Gateway.CodexMetadata.Normalized()
	}
	return (CodexMetadataProfile{
		AgentName:                    configured.AgentName,
		Sandbox:                      configured.Sandbox,
		SandboxMode:                  configured.SandboxMode,
		AutoReviewEnabled:            configured.AutoReviewEnabled,
		TurnMetadataIncludesToolInfo: configured.TurnMetadataIncludesToolInfo,
	}).normalized()
}

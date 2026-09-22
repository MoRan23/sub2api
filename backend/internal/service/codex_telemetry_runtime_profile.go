package service

import (
	"encoding/json"
	"time"
)

// An explicit allowlist is intentionally used instead of marshalling Input or
// Client. Those live snapshots also contain OAuth tokens and proxy credentials.
type codexTelemetryProfileSnapshot struct {
	AccountID               int64             `json:"account_id"`
	AccountName             string            `json:"account_name"`
	ChatGPTAccountID        string            `json:"chatgpt_account_id"`
	OwnerAccountID          int64             `json:"owner_account_id"`
	OSFamily                string            `json:"os_family"`
	CredentialOS            string            `json:"credential_os,omitempty"`
	AuthorizationGeneration string            `json:"authorization_generation,omitempty"`
	InstallationID          string            `json:"installation_id"`
	ManagedInstallation     bool              `json:"managed_installation"`
	ProxyID                 *int64            `json:"proxy_id,omitempty"`
	SamplingID              string            `json:"sampling_id,omitempty"`
	UserAgent               string            `json:"user_agent"`
	Originator              string            `json:"originator"`
	Version                 string            `json:"version"`
	SessionID               string            `json:"session_id"`
	ThreadID                string            `json:"thread_id"`
	TurnID                  string            `json:"turn_id"`
	RootTurnID              string            `json:"root_turn_id"`
	ParentThreadID          string            `json:"parent_thread_id"`
	ParentTurnID            string            `json:"parent_turn_id"`
	ForkedFromThreadID      string            `json:"forked_from_thread_id"`
	ThreadSource            string            `json:"thread_source"`
	TurnTrigger             string            `json:"turn_trigger"`
	AgentName               string            `json:"agent_name"`
	SubagentKind            string            `json:"subagent_kind"`
	OpenAISubagent          string            `json:"openai_subagent"`
	Shell                   string            `json:"shell"`
	Sandbox                 string            `json:"sandbox"`
	SandboxMode             string            `json:"sandbox_mode"`
	ApprovalPolicy          string            `json:"approval_policy"`
	ApprovalsReviewer       string            `json:"approvals_reviewer"`
	AutoReviewEnabled       *bool             `json:"auto_review_enabled,omitempty"`
	GuardianV2Enabled       *bool             `json:"guardian_v2_enabled,omitempty"`
	Model                   string            `json:"model"`
	Effort                  string            `json:"effort"`
	ServiceTier             string            `json:"service_tier"`
	Started                 time.Time         `json:"started"`
	Ended                   time.Time         `json:"ended"`
	FirstThread             bool              `json:"first_thread"`
	Websocket               bool              `json:"websocket"`
	DynamicTool             bool              `json:"dynamic_tool"`
	Command                 bool              `json:"command"`
	FileChange              bool              `json:"file_change"`
	AttemptCount            int               `json:"attempt_count"`
	SamplingCount           int               `json:"sampling_count"`
	ClientRetryCount        int               `json:"client_retry_count"`
	SimulationEnabled       bool              `json:"simulation_enabled"`
	ObservationEnabled      bool              `json:"observation_enabled"`
	PoolID                  string            `json:"pool_id"`
	ScenarioSeed            string            `json:"scenario_seed"`
	Source                  string            `json:"source"`
	Reasons                 []string          `json:"reasons,omitempty"`
	FieldSources            map[string]string `json:"field_sources,omitempty"`
}

func marshalCodexTelemetryProfile(p codexTelemetryProfile) ([]byte, error) {
	i := p.input
	return json.Marshal(codexTelemetryProfileSnapshot{
		AccountID: i.AccountID, AccountName: i.AccountName, ChatGPTAccountID: i.ChatGPTAccountID, OwnerAccountID: i.OwnerAccountID, OSFamily: i.OSFamily,
		CredentialOS: i.CredentialOS, AuthorizationGeneration: i.AuthorizationGeneration,
		InstallationID: i.InstallationID, ManagedInstallation: i.ManagedInstallation, ProxyID: i.ProxyID,
		SamplingID: i.SamplingID, UserAgent: p.client.userAgent, Originator: p.client.originator, Version: p.client.version,
		SessionID: p.sessionID, ThreadID: p.threadID, TurnID: p.turnID, RootTurnID: p.rootTurnID,
		ParentThreadID: i.ParentThreadID, ParentTurnID: i.ParentTurnID, ForkedFromThreadID: i.ForkedFromThreadID,
		ThreadSource: i.ThreadSource, TurnTrigger: i.TurnTrigger, AgentName: i.AgentName,
		SubagentKind: i.SubagentKind, OpenAISubagent: i.OpenAISubagent, Shell: i.Shell,
		Sandbox: i.Sandbox, SandboxMode: i.SandboxMode, ApprovalPolicy: i.ApprovalPolicy,
		ApprovalsReviewer: i.ApprovalsReviewer, AutoReviewEnabled: i.AutoReviewEnabled, GuardianV2Enabled: i.GuardianV2Enabled,
		Model: p.model, Effort: p.effort, ServiceTier: p.serviceTier, Started: p.started, Ended: p.ended,
		FirstThread: p.firstThread, Websocket: p.websocket, DynamicTool: p.dynamicTool, Command: p.command, FileChange: p.fileChange,
		AttemptCount: p.attemptCount, SamplingCount: p.samplingCount, ClientRetryCount: p.clientRetryCount,
		SimulationEnabled: p.simulationEnabled, ObservationEnabled: p.observationEnabled,
		PoolID: p.poolID, ScenarioSeed: p.scenarioSeed, Source: p.source, Reasons: p.reasons, FieldSources: p.fieldSources,
	})
}

func unmarshalCodexTelemetryProfile(data []byte) (codexTelemetryProfile, error) {
	var snap codexTelemetryProfileSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return codexTelemetryProfile{}, ErrCodexTelemetryInvalidState
	}
	i := CodexTelemetryInput{
		AccountID: snap.AccountID, AccountName: snap.AccountName, ChatGPTAccountID: snap.ChatGPTAccountID, OwnerAccountID: snap.OwnerAccountID, OSFamily: snap.OSFamily,
		CredentialOS: snap.CredentialOS, AuthorizationGeneration: snap.AuthorizationGeneration,
		InstallationID: snap.InstallationID, ManagedInstallation: snap.ManagedInstallation, ProxyID: snap.ProxyID,
		SamplingID: snap.SamplingID, UserAgent: snap.UserAgent, Originator: snap.Originator, Version: snap.Version,
		SessionID: snap.SessionID, ThreadID: snap.ThreadID, TurnID: snap.TurnID, RootTurnID: snap.RootTurnID,
		ParentThreadID: snap.ParentThreadID, ParentTurnID: snap.ParentTurnID, ForkedFromThreadID: snap.ForkedFromThreadID,
		ThreadSource: snap.ThreadSource, TurnTrigger: snap.TurnTrigger, AgentName: snap.AgentName,
		SubagentKind: snap.SubagentKind, OpenAISubagent: snap.OpenAISubagent, Shell: snap.Shell,
		Sandbox: snap.Sandbox, SandboxMode: snap.SandboxMode, ApprovalPolicy: snap.ApprovalPolicy,
		ApprovalsReviewer: snap.ApprovalsReviewer, AutoReviewEnabled: snap.AutoReviewEnabled, GuardianV2Enabled: snap.GuardianV2Enabled,
		Model: snap.Model, Effort: snap.Effort, ServiceTier: snap.ServiceTier, WebSocket: snap.Websocket, StartedAt: snap.Started,
	}
	return codexTelemetryProfile{
		client:    codexTelemetryClient{localID: snap.AccountID, name: snap.AccountName, accountID: snap.ChatGPTAccountID, userAgent: snap.UserAgent, originator: snap.Originator, version: snap.Version},
		sessionID: snap.SessionID, threadID: snap.ThreadID, turnID: snap.TurnID, rootTurnID: snap.RootTurnID,
		model: snap.Model, effort: snap.Effort, serviceTier: snap.ServiceTier, started: snap.Started, ended: snap.Ended,
		firstThread: snap.FirstThread, websocket: snap.Websocket, dynamicTool: snap.DynamicTool, command: snap.Command, fileChange: snap.FileChange,
		input: i, attemptCount: snap.AttemptCount, samplingCount: snap.SamplingCount, clientRetryCount: snap.ClientRetryCount,
		simulationEnabled: snap.SimulationEnabled, observationEnabled: snap.ObservationEnabled,
		poolID: snap.PoolID, scenarioSeed: snap.ScenarioSeed, source: snap.Source, reasons: snap.Reasons, fieldSources: snap.FieldSources,
	}, nil
}

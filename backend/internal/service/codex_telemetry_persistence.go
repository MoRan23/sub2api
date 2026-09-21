package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/codexnative"
)

// SetPersistence is called by Wire before any business request is accepted.
// A configured persistent store never falls back to process-local state.
func (s *CodexTelemetryService) SetPersistence(store CodexTelemetryStore, accounts AccountRepository, proxies ProxyRepository) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.accountRepo, s.proxyRepo = accounts, proxies
	s.mu.Unlock()
	if err := s.SetStore(store); err != nil {
		slog.Warn("Codex telemetry policy unavailable; waiting for persistent storage recovery")
	}
}

// Tests using the standalone memory store have no account directory. Keep their
// transport credentials in memory only; production always reads the repository.
func (s *CodexTelemetryService) rememberTransport(input CodexTelemetryInput) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.accountRepo != nil {
		return
	}
	if s.transportInputs == nil {
		s.transportInputs = make(map[CodexTelemetryPoolKey]CodexTelemetryInput)
	}
	owner := input.OwnerAccountID
	if owner == 0 {
		owner = input.AccountID
	}
	key := CodexTelemetryPoolKey{OwnerAccountID: owner, OSFamily: input.OSFamily, InstallationID: input.InstallationID}
	if _, exists := s.transportInputs[key]; !exists && len(s.transportInputs) >= codexTelemetryMaxStates {
		return
	}
	s.transportInputs[key] = input
}

func (s *CodexTelemetryService) dispatchPersistedBatches(ctx context.Context) {
	s.mu.Lock()
	if !s.enabledLocked() || s.store == nil {
		s.mu.Unlock()
		return
	}
	store, epoch := s.store, s.epoch
	capacity := codexTelemetryQueueSize - s.queueDepth
	s.mu.Unlock()
	if capacity <= 0 {
		return
	}
	// Small claims avoid monopolising a shared pool when multiple nodes are live.
	if capacity > 4 {
		capacity = 4
	}
	claims, err := store.ClaimBatches(ctx, time.Now().UTC(), capacity)
	if err != nil {
		return
	}
	for _, claim := range claims {
		profile, err := unmarshalCodexTelemetryProfile(claim.Metadata)
		if err != nil {
			_, _ = store.CompleteBatch(ctx, claim.ID, claim.ClaimID, CodexTelemetryBatchResult{Status: "failed", ErrorCode: "invalid_batch"}, time.Now().UTC())
			continue
		}
		profile.poolID, profile.source = claim.Pool.ID, claim.Source
		s.mu.Lock()
		if !s.enabledLocked() || s.epoch != epoch {
			s.mu.Unlock()
			return
		}
		entry := s.newObservationLocked(profile, 0, claim.Type, codexTelemetryBatchNames(claim), 0)
		entry.BatchID = claim.ID
		if claim.Type == "analytics" {
			var payload struct {
				Events []struct {
					Type   string                     `json:"event_type"`
					Params map[string]json.RawMessage `json:"event_params"`
				} `json:"events"`
			}
			if json.Unmarshal(claim.Payload, &payload) == nil {
				for _, event := range payload.Events {
					if event.Type == "codex_thread_initialized" {
						if value, exists := event.Params["is_worktree"]; exists && string(value) == "null" {
							entry.IsWorktree = json.RawMessage("null")
						}
					}
				}
			}
		}
		copyClaim := claim
		s.enqueueLocked(codexTelemetryJob{profile: profile, body: append([]byte(nil), claim.Payload...), metrics: claim.Type == "metrics", epoch: epoch, entry: entry, persisted: &copyClaim})
		s.mu.Unlock()
	}
}

func codexTelemetryBatchNames(batch CodexTelemetryBatch) []string {
	var payload struct {
		Events []struct {
			Type string `json:"event_type"`
		} `json:"events"`
		ResourceMetrics []struct {
			ScopeMetrics []struct {
				Metrics []struct {
					Name string `json:"name"`
				} `json:"metrics"`
			} `json:"scopeMetrics"`
		} `json:"resourceMetrics"`
	}
	if json.Unmarshal(batch.Payload, &payload) != nil {
		return nil
	}
	names := make([]string, 0)
	seen := make(map[string]bool)
	add := func(name string) {
		if name != "" && !seen[name] && len(names) < 128 {
			names = append(names, name)
			seen[name] = true
		}
	}
	for _, event := range payload.Events {
		add(event.Type)
	}
	for _, resource := range payload.ResourceMetrics {
		for _, scope := range resource.ScopeMetrics {
			for _, metric := range scope.Metrics {
				add(metric.Name)
			}
		}
	}
	return names
}

// hydrateTelemetryTransport restores transport secrets at dispatch, never from
// the durable payload. A missing original route is an error, not a direct route.
func (s *CodexTelemetryService) hydrateTelemetryTransport(ctx context.Context, job *codexTelemetryJob) string {
	if job.persisted == nil {
		return ""
	}
	batch := job.persisted
	s.mu.Lock()
	accounts, proxies := s.accountRepo, s.proxyRepo
	inMemory := s.transportInputs[batch.Pool.Key]
	s.mu.Unlock()
	input := job.profile.input
	if accounts == nil {
		if inMemory.ChatGPTAccountID != input.ChatGPTAccountID || inMemory.AccessToken == "" {
			return "missing_oauth_credentials"
		}
		input.AccessToken, input.ProxyURL = inMemory.AccessToken, inMemory.ProxyURL
		input.nativeHTTPScope = inMemory.nativeHTTPScope
	} else {
		owner, err := accounts.GetByID(ctx, batch.Pool.Key.OwnerAccountID)
		if err != nil || owner == nil {
			return "identity_unavailable"
		}
		if !IsOpenAIOAuthOSProfileOwner(owner) || owner.Status != StatusActive || owner.GetChatGPTAccountID() != input.ChatGPTAccountID {
			return "stale_identity"
		}
		if input.ManagedInstallation {
			if owner.OpenAIOAuthOSProfiles == nil {
				return "stale_identity"
			}
			profile, exists := owner.OpenAIOAuthOSProfiles.Profiles[batch.Pool.Key.OSFamily]
			if !exists || profile.InstallationID != batch.Pool.Key.InstallationID {
				return "stale_identity"
			}
		}
		input.AccessToken = strings.TrimSpace(owner.GetOpenAIAccessToken())
		if input.AccessToken == "" {
			return "missing_oauth_credentials"
		}
		input.ProxyURL = ""
		if batch.ProxyID != nil {
			if proxies == nil {
				return "proxy_unavailable"
			}
			proxy, err := proxies.GetByID(ctx, *batch.ProxyID)
			if err != nil || proxy == nil || !proxy.IsActive() || proxy.IsExpired(time.Now()) {
				return "proxy_unavailable"
			}
			input.ProxyURL = proxy.URL()
		}
		transport := withOpenAINativeHTTPAccountScope(ctx, owner, accounts, "telemetry")
		input.nativeHTTPScope, _ = codexnative.ScopeFromContext(transport)
		input.nativeHTTPScope.SourceUserAgent = input.UserAgent
	}
	job.profile.input = input
	job.profile.client.accessToken, job.profile.client.proxyURL = input.AccessToken, input.ProxyURL
	job.profile.client.nativeHTTPScope = input.nativeHTTPScope
	return ""
}

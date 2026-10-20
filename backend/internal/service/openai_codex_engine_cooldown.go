package service

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	codexEngineUnavailableScopeHeader       = "X-Codex-Engine-Unavailable-Scope"
	codexEngineDeploymentCooldownDefault    = 5 * time.Second
	codexEngineDeploymentCooldownMin        = time.Second
	codexEngineDeploymentCooldownMax        = 30 * time.Second
	codexEngineDeploymentCooldownMaxEntries = 4096
)

// Deployment capacity is independent of account health and administrator intent.
// Success does not clear this state: an older in-flight success must not undo a
// newer maintenance response. Expiry and extension share the same lock.
type codexEngineDeploymentCooldownState struct {
	mu    sync.Mutex
	until map[int64]time.Time
}

func codexEngineDeploymentCooldown(retryAfter string, now time.Time) time.Duration {
	retryAfter = strings.TrimSpace(retryAfter)
	delay := codexEngineDeploymentCooldownDefault
	if seconds, err := strconv.ParseInt(retryAfter, 10, 64); err == nil {
		// Clamp before converting to Duration so large values cannot overflow.
		if seconds <= 0 {
			return codexEngineDeploymentCooldownMin
		}
		if seconds >= int64(codexEngineDeploymentCooldownMax/time.Second) {
			return codexEngineDeploymentCooldownMax
		}
		delay = time.Duration(seconds) * time.Second
	} else if deadline, err := http.ParseTime(retryAfter); err == nil {
		delay = deadline.Sub(now)
	}
	if delay < codexEngineDeploymentCooldownMin {
		return codexEngineDeploymentCooldownMin
	}
	if delay > codexEngineDeploymentCooldownMax {
		return codexEngineDeploymentCooldownMax
	}
	return delay
}

// Only an explicit deployment-wide rejection from the selected Engine account
// can suspend that account for subsequent requests. Group/model capacity errors,
// other providers, and errors inside an already accepted HTTP 200 stream do not
// establish deployment unavailability. Internal account diagnostics must not
// create or change business cooldowns. Never turn this signal into a replay.
func (s *OpenAIGatewayService) observeCodexEngineDeploymentUnavailable(account *Account, resp *http.Response, failure *codexEngineResponseError, now time.Time) {
	if s == nil || account == nil || account.ID <= 0 || !account.IsCodexEngine() || isOpenAICandyTestAccount(account) ||
		resp == nil || resp.StatusCode != http.StatusServiceUnavailable || failure == nil ||
		strings.TrimSpace(resp.Header.Get(codexEngineUnavailableScopeHeader)) != "deployment" {
		return
	}
	if failure.code != "deployment_maintenance" && failure.code != "capacity_unavailable" {
		return
	}
	deadline := now.Add(codexEngineDeploymentCooldown(resp.Header.Get("Retry-After"), now))
	state := &s.codexEngineDeploymentCooldowns
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.until == nil {
		state.until = make(map[int64]time.Time)
	}
	if current, ok := state.until[account.ID]; ok {
		if deadline.After(current) {
			state.until[account.ID] = deadline
		}
		return
	}
	if len(state.until) >= codexEngineDeploymentCooldownMaxEntries {
		var earliestID int64
		var earliest time.Time
		for id, until := range state.until {
			if !now.Before(until) {
				delete(state.until, id)
			} else if earliest.IsZero() || until.Before(earliest) {
				earliestID, earliest = id, until
			}
		}
		if len(state.until) >= codexEngineDeploymentCooldownMaxEntries {
			delete(state.until, earliestID)
		}
	}
	state.until[account.ID] = deadline
}

func (s *OpenAIGatewayService) isCodexEngineDeploymentUnavailable(account *Account, now time.Time) bool {
	if s == nil || account == nil || account.ID <= 0 || !account.IsCodexEngine() {
		return false
	}
	state := &s.codexEngineDeploymentCooldowns
	state.mu.Lock()
	defer state.mu.Unlock()
	until, ok := state.until[account.ID]
	if !ok {
		return false
	}
	if !now.Before(until) {
		delete(state.until, account.ID)
		return false
	}
	return true
}

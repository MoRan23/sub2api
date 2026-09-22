package service

import (
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

type codexMetricDescriptor struct {
	name       string
	kind       string
	unit       string
	attributes string
}

// Keep the upstream metric vocabulary. A definition does not mean a sample is
// invented when the corresponding transport or timing was not observed.
var codexMetricDescriptors = []codexMetricDescriptor{
	{"codex.process.start", "sum", "", "originator"},
	{"codex.sqlite.init.count", "sum", "", "db,error,originator,phase,status"},
	{"codex.sqlite.init.duration_ms", "histogram", "ms", "db,error,originator,phase,status"},
	{"codex.app_server.codex_home.size_bytes", "histogram", "", "compression_enabled,directory"},
	{"codex.remote_models.fetch_update.duration_ms", "histogram", "ms", ""},
	{"codex.plugins.loaded_cache.event", "sum", "", "event"},
	{"codex.remote_models.load_cache.duration_ms", "histogram", "ms", ""},
	{"codex.plugins.loaded_cache.wait.duration_ms", "histogram", "ms", ""},
	{"codex.plugins.loaded_cache.load.duration_ms", "histogram", "ms", ""},
	{"codex.plugins.loaded_cache.request", "sum", "", "outcome"},
	{"codex.mcp.protocol_discovery", "sum", "", "mode,outcome,server_kind"},
	{"codex.mcp.protocol_discovery.duration_ms", "histogram", "ms", "mode,outcome,server_kind"},
	{"codex.mcp.tools.fetch_uncached.duration_ms", "histogram", "ms", "trigger"},
	{"codex.mcp.tools.list.duration_ms", "histogram", "ms", "cache"},
	{"codex.apps.installed.duration_ms", "histogram", "ms", "force_refresh,outcome,path,refresh,reload,retained_previous_snapshot"},
	{"codex.apps.installed.response_bytes", "histogram", "", "path"},
	{"codex.apps.installed.connector_count", "histogram", "", "path"},
	{"codex.apps.installed.tool_count", "histogram", "", "path"},
	{"codex.apps.snapshot.age_ms", "histogram", "ms", "observation,path"},
	{"codex.apps.read.duration_ms", "histogram", "ms", "include_tools"},
	{"codex.sqlite.logs.write.count", "sum", "", "error,originator,status"},
	{"codex.sqlite.logs.write.duration_ms", "histogram", "ms", "error,originator,status"},
	{"codex.sqlite.logs.write.bytes", "histogram", "", "error,originator,status"},
	{"codex.sqlite.logs.write.entries", "histogram", "", "error,originator,status"},
	{"codex.sqlite.logs.write.max_entry_bytes", "histogram", "", "error,originator,status"},
	{"codex.mcp.tools.cache_write.duration_ms", "histogram", "ms", "status"},
	{"codex.mcp.tools.cache_publish.duration_ms", "histogram", "ms", "result,source"},
	{"codex.apps.refresh.duration_ms", "histogram", "ms", "path,trigger"},
	{"codex.feature.state", "sum", "", "app.version,auth_mode,feature,model,originator,service_name,session_source,value"},
	{"codex.thread.started", "sum", "", "app.version,auth_mode,is_git,is_worktree,model,originator,service_name,session_source"},
	{"codex.shell_snapshot.duration_ms", "histogram", "ms", "app.version,auth_mode,model,originator,session_source,success,version"},
	{"codex.shell_snapshot", "sum", "", "app.version,auth_mode,failure_reason,model,originator,session_source,success,version"},
	{"codex.startup.phase.duration_ms", "histogram", "ms", "app.version,auth_mode,model,originator,phase,service_name,session_source,status"},
	{"codex.websocket.request", "sum", "", "app.version,auth_mode,model,originator,service_name,session_source,success"},
	{"codex.websocket.request.duration_ms", "histogram", "ms", "app.version,auth_mode,model,originator,service_name,session_source,success"},
	{"codex.rollout_compression.materialize", "sum", "", "outcome"},
	{"codex.websocket.event", "sum", "", "app.version,auth_mode,kind,model,originator,service_name,session_source,success"},
	{"codex.websocket.event.duration_ms", "histogram", "ms", "app.version,auth_mode,kind,model,originator,service_name,session_source,success"},
	{"codex.sse_event", "sum", "", "app.version,auth_mode,kind,model,originator,service_name,session_source,success"},
	{"codex.sse_event.duration_ms", "histogram", "ms", "app.version,auth_mode,kind,model,originator,service_name,session_source,success"},
	{"codex.startup_prewarm.duration_ms", "histogram", "ms", "app.version,auth_mode,model,originator,service_name,session_source,status"},
	{"codex.startup_prewarm.age_at_first_turn_ms", "histogram", "ms", "app.version,auth_mode,model,originator,service_name,session_source,status"},
	{"codex.thread.skills.enabled_total", "histogram", "", "app.version,auth_mode,catalog_surface,model,originator,service_name,session_source"},
	{"codex.thread.skills.kept_total", "histogram", "", "app.version,auth_mode,catalog_surface,model,originator,service_name,session_source"},
	{"codex.thread.skills.truncated", "histogram", "", "app.version,auth_mode,catalog_surface,model,originator,service_name,session_source"},
	{"codex.thread.skills.description_truncated_chars", "histogram", "", "app.version,auth_mode,catalog_surface,model,originator,service_name,session_source"},
	{"codex.skills.shadow_selection", "sum", "", "candidate_set_truncated,method,query_script,query_truncated,status"},
	{"codex.skills.shadow_selection.duration_ms", "histogram", "ms", "candidate_set_truncated,method,query_script,query_truncated,status"},
	{"codex.skills.shadow_selection.catalog_entries", "histogram", "", "candidate_set_truncated,method,query_script,query_truncated,status"},
	{"codex.skills.shadow_selection.selected_entries", "histogram", "", "candidate_set_truncated,method,query_script,query_truncated,status"},
	{"codex.skills.shadow_selection.query_terms", "histogram", "", "candidate_set_truncated,method,query_script,query_truncated,status"},
	{"codex.skills.shadow_selection.reduction_bps", "histogram", "", "candidate_set_truncated,method,query_script,query_truncated,status"},
	{"codex.turn.ttft.duration_ms", "histogram", "ms", "app.version,auth_mode,model,originator,service_name,session_source"},
	{"codex.turn.ttfm.duration_ms", "histogram", "ms", "app.version,auth_mode,model,originator,service_name,session_source"},
	{"codex.responses_api_overhead.duration_ms", "histogram", "ms", "app.version,auth_mode,model,originator,service_name,session_source"},
	{"codex.responses_api_inference_time.duration_ms", "histogram", "ms", "app.version,auth_mode,model,originator,service_name,session_source"},
	{"codex.responses_api_engine_iapi_tbt.duration_ms", "histogram", "ms", "app.version,auth_mode,model,originator,service_name,session_source"},
	{"codex.turn.e2e_duration_ms", "histogram", "ms", "app.version,auth_mode,model,originator,service_name,session_source"},
	{"codex.turn.network_proxy", "sum", "", "active,app.version,auth_mode,model,originator,service_name,session_source,tmp_mem_enabled"},
	{"codex.turn.tool.call", "histogram", "", "app.version,auth_mode,model,originator,service_name,session_source,tmp_mem_enabled"},
	{"codex.turn.memory", "sum", "", "app.version,auth_mode,config_use_memories,feature_enabled,has_citations,model,originator,read_allowed,service_name,session_source"},
	{"codex.turn.unified_exec.running_processes", "sum", "", "app.version,auth_mode,model,originator,service_name,session_source"},
	{"codex.windows_mxc.available", "sum", "", "available"},
	{"codex.tool.unified_exec", "sum", "", "app.version,auth_mode,model,originator,session_source,tty"},
	{"codex.hooks.run", "sum", "", "app.version,auth_mode,execution_mode,handler_type,hook_name,model,originator,session_source,source,status"},
	{"codex.hooks.run.duration_ms", "histogram", "ms", "app.version,auth_mode,execution_mode,handler_type,hook_name,model,originator,session_source,source,status"},
	{"codex.guardian.review", "sum", "", "app.version,auth_mode,model,originator,session_source"},
	{"codex.guardian.review.duration_ms", "histogram", "ms", "app.version,auth_mode,model,originator,session_source"},
	{"codex.external_agent_config.detect", "sum", "", "migration_type"},
	{"codex.rollout.size_bytes", "histogram", "", ""},
}

var codexHistogramBounds = []float64{0, 5, 10, 25, 50, 75, 100, 250, 500, 750, 1000, 1250, 1500, 1750, 2000, 2250, 2500, 3000, 3500, 4000, 4500, 5000, 6000, 7000, 7500, 8000, 9000, 10000, 12000, 15000, 20000, 30000, 60000, 120000}
var codexValueHistogramBounds = []float64{0, 5, 10, 25, 50, 75, 100, 250, 500, 750, 1000, 2500, 5000, 7500, 10000}
var codexHomeSizeHistogramBounds = []float64{1048576, 10485760, 104857600, 1073741824, 10737418240, 107374182400, 1099511627776}
var codexSQLiteBytesHistogramBounds = []float64{128, 256, 512, 1024, 2048, 4096, 8192, 16384, 32768, 65536, 131072, 262144, 524288, 1048576, 2097152, 4194304, 8388608, 16777216}

func codexMetricBounds(descriptor codexMetricDescriptor) []float64 {
	switch descriptor.name {
	case "codex.app_server.codex_home.size_bytes":
		return codexHomeSizeHistogramBounds
	case "codex.sqlite.logs.write.bytes", "codex.sqlite.logs.write.max_entry_bytes":
		return codexSQLiteBytesHistogramBounds
	}
	if descriptor.unit == "ms" {
		return codexHistogramBounds
	}
	return codexValueHistogramBounds
}

// Exact names from Codex 0.155.1's built-in Statsig exporter. Do not turn
// this into prefix filtering: allowed turn and tool summaries share prefixes.
func codexStatsigMetricAllowed(name string) bool {
	switch name {
	case "codex.api_request", "codex.api_request.duration_ms", "codex.conversation.turn.count",
		"exec_server_client_requests_total", "codex.responses_api_engine_iapi_ttft.duration_ms",
		"codex.responses_api_engine_service_tbt.duration_ms", "codex.responses_api_engine_service_ttft.duration_ms",
		"codex.tool.call", "codex.tool.call.duration_ms", "codex.turn.cost_microusd", "codex.turn.token_usage":
		return false
	}
	return true
}

const codexTelemetryMetricStateLimit = 4096

type codexTelemetryMetricBatch struct {
	profile  codexTelemetryProfile
	body     []byte
	names    []string
	turns    int
	attempts int
	source   string
}

type codexMetricAggregate struct {
	descriptor codexMetricDescriptor
	attributes []any
	started    time.Time
	finished   time.Time
	count      uint64
	sum        float64
	minimum    float64
	maximum    float64
	buckets    []uint64
}

func (a *codexMetricAggregate) observe(value float64, started, finished time.Time) {
	if a.count == 0 {
		a.minimum, a.maximum, a.started = value, value, started
		a.buckets = make([]uint64, len(codexMetricBounds(a.descriptor))+1)
	} else {
		a.minimum, a.maximum = math.Min(a.minimum, value), math.Max(a.maximum, value)
		if started.Before(a.started) {
			a.started = started
		}
	}
	if finished.After(a.finished) {
		a.finished = finished
	}
	a.count++
	a.sum += value
	index := sort.SearchFloat64s(codexMetricBounds(a.descriptor), value)
	a.buckets[index]++
}

type codexMetricState struct {
	profile           codexTelemetryProfile
	lastSeen          time.Time
	pending           map[string]*codexMetricAggregate
	turns             int
	attempts          int
	externalAgentSent bool
	collectedAt       time.Time
	source            string
	eventMetricKinds  map[string]struct{}
}

// The service mutex protects this store. Samples are summarized into bounded
// buckets rather than retaining a growing list of individual observations.
type codexTelemetryMetricStore struct {
	states              map[string]*codexMetricState
	clients             map[string]time.Time
	compatibilityReason string
}

func newCodexTelemetryMetricStore() *codexTelemetryMetricStore {
	return &codexTelemetryMetricStore{states: make(map[string]*codexMetricState), clients: make(map[string]time.Time)}
}

func codexMetricClientKey(profile codexTelemetryProfile) string {
	if profile.poolID != "" {
		return profile.poolID
	}
	key, _ := json.Marshal([]any{
		profile.input.OwnerAccountID, profile.client.localID,
		codexTelemetryOSFamily(profile), profile.input.InstallationID,
	})
	return string(key)
}

func codexMetricStateKey(profile codexTelemetryProfile) string {
	// Resource, credential routing and client/model attributes all form the
	// partition. A newer request must never relabel pending samples of another
	// model, UA, transport, account or proxy with its own profile.
	key, _ := json.Marshal([]any{
		codexMetricClientKey(profile), profile.client.localID,
		profile.input.CredentialOS, profile.input.AuthorizationGeneration,
		profile.client.userAgent, profile.client.originator, profile.client.version,
		profile.input.ProxyID, profile.model, profile.effort, profile.serviceTier,
		profile.websocket, profile.simulationEnabled, profile.observationEnabled, codexResourceAttributes(profile),
	})
	return string(key)
}

func (s *codexTelemetryMetricStore) state(profile codexTelemetryProfile, now time.Time) (*codexMetricState, bool) {
	key := codexMetricStateKey(profile)
	if state := s.states[key]; state != nil {
		state.lastSeen = now
		// Credentials may refresh during a long-running process. They are only
		// transport context and never included in keys, attributes or summaries.
		state.profile.client.accessToken = profile.client.accessToken
		state.profile.input.AccessToken = profile.input.AccessToken
		return state, false
	}
	if len(s.states) >= codexTelemetryMetricStateLimit {
		var oldestKey string
		var oldest time.Time
		for stateKey, state := range s.states {
			if oldestKey == "" || state.lastSeen.Before(oldest) {
				oldestKey, oldest = stateKey, state.lastSeen
			}
		}
		delete(s.states, oldestKey)
	}
	state := &codexMetricState{profile: profile, lastSeen: now, collectedAt: now, pending: make(map[string]*codexMetricAggregate)}
	s.states[key] = state
	return state, true
}

func (s *codexTelemetryMetricStore) touch(profile codexTelemetryProfile) []codexTelemetryMetricBatch {
	state, _ := s.state(profile, profile.started)
	if !codexSimulatesClientBehavior(profile) {
		return nil
	}
	clientKey := codexMetricClientKey(profile)
	if _, exists := s.clients[clientKey]; exists {
		s.clients[clientKey] = profile.started
		return nil
	}
	if len(s.clients) >= codexTelemetryMetricStateLimit {
		var oldestKey string
		var oldest time.Time
		for key, seen := range s.clients {
			if oldestKey == "" || seen.Before(oldest) {
				oldestKey, oldest = key, seen
			}
		}
		delete(s.clients, oldestKey)
	}
	s.clients[clientKey] = profile.started
	for _, descriptor := range codexMetricDescriptors {
		if !codexMetricIsStartup(descriptor.name) {
			continue
		}
		if descriptor.name == "codex.windows_mxc.available" {
			// A Windows UA does not prove whether the MXC capability probe passed.
			continue
		}
		if descriptor.name == "codex.app_server.codex_home.size_bytes" {
			for _, directory := range []string{"sessions", "archived_sessions"} {
				value := float64(simulatedInt(profile.scenarioSeed+":"+descriptor.name+":"+directory, 262144))
				state.addWithSource(descriptor, value, profile, "completed", profile.started, map[string]string{"directory": directory}, "simulated")
			}
			continue
		}
		state.addWithSource(descriptor, codexStartupMetricValue(profile, descriptor), profile, "completed", profile.started, nil, "simulated")
	}
	return nil
}

func codexMetricIsStartup(name string) bool {
	// These are explicitly simulated client initialization activities. Network
	// request success and turn timings require an actual terminal response.
	for _, prefix := range []string{"codex.turn.", "codex.responses_api", "codex.websocket.", "codex.sse_event", "codex.thread.", "codex.hooks.", "codex.guardian.", "codex.tool."} {
		if strings.HasPrefix(name, prefix) {
			return false
		}
	}
	return name != "codex.external_agent_config.detect" && name != "codex.rollout.size_bytes"
}

func (s *codexTelemetryMetricStore) record(profile codexTelemetryProfile, result codexTelemetryTerminal) {
	if !codexSimulatesClientBehavior(profile) {
		// A Responses completion cannot prove a client turn, hook, or tool ended.
		return
	}
	now := result.finished
	if now.IsZero() {
		now = time.Now()
	}
	state, _ := s.state(profile, now)
	profile.ended = now
	state.turns++
	for _, descriptor := range codexMetricDescriptors {
		var value float64
		switch descriptor.name {
		case "codex.turn.e2e_duration_ms":
			value = float64(elapsedMillis(profile.started, now, now))
		case "codex.turn.ttft.duration_ms":
			if result.result.FirstTokenAt.IsZero() {
				continue
			}
			value = float64(elapsedMillis(profile.started, result.result.FirstTokenAt, now))
		case "codex.turn.ttfm.duration_ms":
			if result.result.FirstAgentMessageAt.IsZero() {
				continue
			}
			value = float64(elapsedMillis(profile.started, result.result.FirstAgentMessageAt, now))
		case "codex.thread.started":
			if !profile.firstThread {
				continue
			}
			value = 1
		case "codex.thread.skills.enabled_total", "codex.thread.skills.kept_total", "codex.thread.skills.truncated", "codex.thread.skills.description_truncated_chars":
			if !profile.firstThread {
				continue
			}
			value = codexStartupMetricValue(profile, descriptor)
		case "codex.hooks.run", "codex.hooks.run.duration_ms":
			hookName, hookCount := codexSimulatedLifecycleHook(profile, result.explicitClientInterrupt)
			for index := range hookCount {
				hookValue := float64(1)
				if descriptor.unit == "ms" {
					hookValue = float64(codexSimulatedHookDuration(profile, index))
				}
				state.addWithSource(descriptor, hookValue, profile, "completed", now, map[string]string{"hook_name": hookName}, "simulated")
			}
			continue
		case "codex.guardian.review", "codex.guardian.review.duration_ms":
			if !codexSimulatesGuardian(profile) {
				continue
			}
			_, action := codexGuardianTarget(profile)
			value = 1
			if descriptor.unit == "ms" {
				value = float64(codexActivityDuration(profile, "guardian", 100, 800))
			}
			state.addWithSource(descriptor, value, profile, "completed", now, map[string]string{
				"decision": "approved", "terminal_status": "approved", "failure_reason": "none",
				"approval_request_source": "main_turn", "action": action["type"].(string), "session_kind": "trunk_new", "had_prior_review_context": "false",
			}, "simulated")
			continue
		case "codex.turn.tool.call":
			value = float64(boolInt(profile.dynamicTool) + boolInt(codexSimulatesFileChange(profile)))
		case "codex.tool.unified_exec":
			if !profile.command || !profile.dynamicTool {
				continue
			}
			value = 1
		case "codex.rollout.size_bytes":
			if !codexSimulatesFileChange(profile) {
				continue
			}
			value = float64(1024 + simulatedInt(codexScenarioKey(profile)+":rollout", 196608))
		case "codex.external_agent_config.detect":
			if state.externalAgentSent {
				continue
			}
			state.externalAgentSent = true
			value = 1
		case "codex.turn.network_proxy", "codex.turn.memory":
			value = 1
		case "codex.turn.unified_exec.running_processes":
			value = 0
		default:
			// Server-only overhead/inference/inter-token times are unavailable
			// in the terminal snapshot; do not substitute random startup data.
			continue
		}
		state.addWithSource(descriptor, value, profile, result.status, now, nil, "simulated")
	}
}

// recordAttempt records physical sends separately from logical turn completion.
// Failed retries remain visible under the account, transport and model that
// actually sent them, while simulated turn events are still emitted only once.
func (s *codexTelemetryMetricStore) recordAttempt(profile codexTelemetryProfile, result codexTelemetryTerminal) {
	if !profile.observationEnabled {
		return
	}
	now := result.finished
	if now.IsZero() {
		now = time.Now()
	}
	state, _ := s.state(profile, now)
	state.attempts++
	measurement := result.result
	for _, descriptor := range codexMetricDescriptors {
		var value float64
		switch descriptor.name {
		case "codex.websocket.request":
			if !profile.websocket || measurement.SendSucceeded == nil {
				continue
			}
			state.addWithSource(descriptor, 1, profile, "", now, map[string]string{"success": strconv.FormatBool(*measurement.SendSucceeded)}, "observed")
			continue
		case "codex.websocket.event", "codex.sse_event":
			if strings.HasPrefix(descriptor.name, "codex.websocket") != profile.websocket {
				continue
			}
			for _, event := range codexTelemetryMetricEvents(measurement) {
				if event.Count > 0 {
					state.addWithSource(descriptor, float64(event.Count), profile, "", now, state.eventMetricAttributes(event), "observed")
				}
			}
			continue
		case "codex.websocket.request.duration_ms":
			if !profile.websocket || measurement.SendSucceeded == nil || measurement.SendDurationMS < 0 {
				continue
			}
			state.addWithSource(descriptor, measurement.SendDurationMS, profile, "", now, map[string]string{"success": strconv.FormatBool(*measurement.SendSucceeded)}, "observed")
			continue
		case "codex.websocket.event.duration_ms", "codex.sse_event.duration_ms":
			if strings.HasPrefix(descriptor.name, "codex.websocket") != profile.websocket {
				continue
			}
			for _, event := range measurement.EventMetrics {
				state.addEventWaitAggregate(descriptor, event, profile, now)
			}
			continue
		case "codex.responses_api_overhead.duration_ms", "codex.responses_api_inference_time.duration_ms", "codex.responses_api_engine_iapi_tbt.duration_ms":
			var exists bool
			rawName := map[string]string{
				"codex.responses_api_overhead.duration_ms":        "responses_duration_excl_engine_and_client_tool_time_ms",
				"codex.responses_api_inference_time.duration_ms":  "engine_service_total_ms",
				"codex.responses_api_engine_iapi_tbt.duration_ms": "engine_iapi_tbt_across_engine_calls_ms",
			}[descriptor.name]
			value, exists = measurement.ServerTiming[rawName]
			if !exists {
				continue
			}
		default:
			continue
		}
		state.addWithSource(descriptor, value, profile, result.status, now, nil, "observed")
	}
}

func codexTelemetryMetricEvents(result CodexTelemetryResult) []CodexTelemetryEventMetric {
	if len(result.EventMetrics) > 0 {
		return result.EventMetrics
	}
	// Older result producers know the counts but cannot recover event kinds.
	failed := min(result.FailedEventCount, result.EventCount)
	if failed < 0 {
		failed = 0
	}
	events := make([]CodexTelemetryEventMetric, 0, 2)
	if succeeded := result.EventCount - failed; succeeded > 0 {
		events = append(events, CodexTelemetryEventMetric{Kind: "unknown", Success: true, Count: uint64(succeeded)})
	}
	if failed > 0 {
		events = append(events, CodexTelemetryEventMetric{Kind: "unknown", Success: false, Count: uint64(failed)})
	}
	return events
}

func (s *codexMetricState) eventMetricAttributes(event CodexTelemetryEventMetric) map[string]string {
	success := strconv.FormatBool(event.Success)
	return map[string]string{"kind": s.eventMetricKind(event.Kind, success), "success": success}
}

// Counts and durations share one assignment for the entire delta window.
// Reserve two unknown series so overflow preserves every success/failure sample.
func (s *codexMetricState) eventMetricKind(kind, success string) string {
	kind = codexTelemetryEventKind(kind)
	if kind == "unknown" {
		return kind
	}
	key := kind + ":" + success
	if _, exists := s.eventMetricKinds[key]; exists {
		return kind
	}
	if len(s.eventMetricKinds) >= codexTelemetryMaxEventKinds-2 {
		return "unknown"
	}
	if s.eventMetricKinds == nil {
		s.eventMetricKinds = make(map[string]struct{})
	}
	s.eventMetricKinds[key] = struct{}{}
	return kind
}

func (s *codexMetricState) addEventWaitAggregate(descriptor codexMetricDescriptor, event CodexTelemetryEventMetric, profile codexTelemetryProfile, finished time.Time) {
	if event.WaitCount == 0 || len(event.WaitBuckets) != len(codexMetricBounds(descriptor))+1 {
		return
	}
	for _, value := range []float64{event.WaitSumMS, event.WaitMinMS, event.WaitMaxMS} {
		if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return
		}
	}
	if event.WaitMinMS > event.WaitMaxMS {
		return
	}
	var bucketCount uint64
	for _, count := range event.WaitBuckets {
		bucketCount += count
	}
	if bucketCount != event.WaitCount {
		return
	}
	aggregate := s.aggregateWithSource(descriptor, profile, "", s.eventMetricAttributes(event), "observed")
	if aggregate.count == 0 {
		aggregate.started, aggregate.minimum, aggregate.maximum = profile.started, event.WaitMinMS, event.WaitMaxMS
		aggregate.buckets = make([]uint64, len(event.WaitBuckets))
	} else {
		aggregate.minimum, aggregate.maximum = math.Min(aggregate.minimum, event.WaitMinMS), math.Max(aggregate.maximum, event.WaitMaxMS)
		if profile.started.Before(aggregate.started) {
			aggregate.started = profile.started
		}
	}
	if finished.After(aggregate.finished) {
		aggregate.finished = finished
	}
	aggregate.count += event.WaitCount
	aggregate.sum += event.WaitSumMS
	for index, count := range event.WaitBuckets {
		aggregate.buckets[index] += count
	}
}

func (s *codexMetricState) add(descriptor codexMetricDescriptor, value float64, profile codexTelemetryProfile, status string, finished time.Time) {
	s.addWithAttributes(descriptor, value, profile, status, finished, nil)
}

func (s *codexMetricState) addWithAttributes(descriptor codexMetricDescriptor, value float64, profile codexTelemetryProfile, status string, finished time.Time, overrides map[string]string) {
	s.addWithSource(descriptor, value, profile, status, finished, overrides, codexTelemetryProfileSource(profile))
}

func (s *codexMetricState) addWithSource(descriptor codexMetricDescriptor, value float64, profile codexTelemetryProfile, status string, finished time.Time, overrides map[string]string, source string) {
	if !codexStatsigMetricAllowed(descriptor.name) || value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return
	}
	s.aggregateWithSource(descriptor, profile, status, overrides, source).observe(value, profile.started, finished)
}

func (s *codexMetricState) aggregateWithSource(descriptor codexMetricDescriptor, profile codexTelemetryProfile, status string, overrides map[string]string, source string) *codexMetricAggregate {
	if s.source == "" {
		s.source = source
	} else if s.source != source {
		s.source = "mixed"
	}
	attributes := codexMetricAttributes(profile, descriptor, status, overrides)
	encoded, _ := json.Marshal(attributes)
	key := descriptor.name + ":" + string(encoded)
	aggregate := s.pending[key]
	if aggregate == nil {
		aggregate = &codexMetricAggregate{descriptor: descriptor, attributes: attributes}
		s.pending[key] = aggregate
	}
	return aggregate
}

func (s *codexTelemetryMetricStore) flush(now time.Time) []codexTelemetryMetricBatch {
	keys := make([]string, 0, len(s.states))
	for key := range s.states {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	batches := make([]codexTelemetryMetricBatch, 0, len(keys))
	for _, key := range keys {
		state := s.states[key]
		if now.Before(state.collectedAt.Add(time.Minute)) {
			continue
		}
		if len(state.pending) > 0 {
			for _, aggregate := range state.pending {
				aggregate.started, aggregate.finished = state.collectedAt, now
			}
			batches = append(batches, state.batch())
			state.pending = make(map[string]*codexMetricAggregate)
			state.eventMetricKinds = nil
		}
		state.collectedAt, state.source = now, ""
		state.turns, state.attempts = 0, 0
		if now.Sub(state.lastSeen) > 5*time.Minute {
			delete(s.states, key)
		}
	}
	return batches
}

func (s *codexTelemetryMetricStore) clear() {
	s.states = make(map[string]*codexMetricState)
	s.clients = make(map[string]time.Time)
	s.compatibilityReason = ""
}

func (s *codexMetricState) batch() codexTelemetryMetricBatch {
	keys := make([]string, 0, len(s.pending))
	for key := range s.pending {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	metrics := make([]any, 0, len(keys))
	names := make([]string, 0, len(keys))
	byName := make(map[string]map[string]any)
	for _, key := range keys {
		aggregate := s.pending[key]
		metric := aggregate.otlp()
		name, kind := aggregate.descriptor.name, aggregate.descriptor.kind
		if existing := byName[name]; existing != nil {
			data, dataOK := existing[kind].(map[string]any)
			incoming, incomingOK := metric[kind].(map[string]any)
			points, pointsOK := data["dataPoints"].([]any)
			incomingPoints, incomingPointsOK := incoming["dataPoints"].([]any)
			if dataOK && incomingOK && pointsOK && incomingPointsOK {
				data["dataPoints"] = append(points, incomingPoints...)
				continue
			}
		}
		byName[name] = metric
		metrics = append(metrics, metric)
		names = append(names, name)
	}
	resource := map[string]any{"attributes": codexResourceAttributes(s.profile), "droppedAttributesCount": 0}
	scope := map[string]any{"name": "codex", "version": "", "attributes": []any{}, "droppedAttributesCount": 0}
	scopeMetrics := map[string]any{"scope": scope, "metrics": metrics, "schemaUrl": ""}
	body, _ := json.Marshal(map[string]any{"resourceMetrics": []any{map[string]any{"resource": resource, "scopeMetrics": []any{scopeMetrics}, "schemaUrl": ""}}})
	profile := s.profile
	profile.sessionID, profile.threadID, profile.turnID, profile.rootTurnID = "", "", "", ""
	profile.input.SessionID, profile.input.ThreadID, profile.input.TurnID = "", "", ""
	profile.input.RootTurnID, profile.input.ParentThreadID, profile.input.ParentTurnID, profile.input.ForkedFromThreadID = "", "", "", ""
	return codexTelemetryMetricBatch{profile: profile, body: body, names: names, turns: s.turns, attempts: s.attempts, source: s.source}
}

func (a *codexMetricAggregate) otlp() map[string]any {
	point := map[string]any{
		"attributes": a.attributes, "startTimeUnixNano": strconv.FormatInt(a.started.UnixNano(), 10),
		"timeUnixNano": strconv.FormatInt(a.finished.UnixNano(), 10), "exemplars": []any{}, "flags": 0,
	}
	metric := map[string]any{"name": a.descriptor.name, "description": "", "unit": a.descriptor.unit, "metadata": []any{}}
	if a.descriptor.kind == "sum" {
		point["asInt"] = strconv.FormatInt(int64(a.sum), 10)
		metric["sum"] = map[string]any{"dataPoints": []any{point}, "aggregationTemporality": 1, "isMonotonic": true}
		return metric
	}
	if a.descriptor.unit == "ms" {
		metric["description"] = "Duration in milliseconds."
	}
	point["count"], point["sum"], point["min"], point["max"] = strconv.FormatUint(a.count, 10), a.sum, a.minimum, a.maximum
	point["explicitBounds"] = codexMetricBounds(a.descriptor)
	buckets := make([]string, len(a.buckets))
	for index, count := range a.buckets {
		buckets[index] = strconv.FormatUint(count, 10)
	}
	point["bucketCounts"] = buckets
	metric["histogram"] = map[string]any{"dataPoints": []any{point}, "aggregationTemporality": 1}
	return metric
}

func codexResourceAttributes(profile codexTelemetryProfile) []any {
	_, _, osName, osVersion, _ := codexUserAgentParts(profile.client.userAgent, profile.client.version)
	if codexTelemetryOSFamily(profile) == "macos" {
		osName = "Mac OS"
	}
	return codexOTLPAttributes(map[string]string{
		"os": codexMetricSanitizeTag(osName), "os_version": codexMetricSanitizeTag(osVersion), "service.version": profile.client.version, "env": "dev",
		"telemetry.sdk.version": "0.31.0", "telemetry.sdk.language": "rust",
		"service.name": codexMetricResourceService(profile), "telemetry.sdk.name": "opentelemetry",
	})
}

func codexMetricAttributes(profile codexTelemetryProfile, descriptor codexMetricDescriptor, status string, overrides map[string]string) []any {
	values := make(map[string]string)
	if descriptor.attributes != "" {
		for _, name := range strings.Split(descriptor.attributes, ",") {
			if value := codexMetricAttributeValue(profile, descriptor.name, name, status); value != "" {
				values[name] = value
			}
		}
	}
	for name, value := range overrides {
		if value == "" {
			delete(values, name)
		} else {
			values[name] = value
		}
	}
	return codexOTLPAttributes(values)
}

func codexOTLPAttributes(values map[string]string) []any {
	keys := make([]string, 0, len(values))
	for key := range values {
		if values[key] != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	attributes := make([]any, 0, len(keys))
	for _, key := range keys {
		attributes = append(attributes, map[string]any{"key": key, "value": map[string]any{"stringValue": values[key]}})
	}
	return attributes
}

func codexMetricAttributeValue(profile codexTelemetryProfile, metric, name, status string) string {
	if name == "originator" && (metric == "codex.process.start" || strings.HasPrefix(metric, "codex.sqlite.")) {
		return codexMetricOriginator(profile)
	}
	if status == "" {
		status = "failed"
	}
	success := status == "completed"
	statusLabel, failureReason := status, status
	if success {
		statusLabel, failureReason = "success", "none"
	}
	if strings.HasPrefix(metric, "codex.shell_snapshot") {
		shell := codexTelemetryShell(profile)
		if shell != "bash" && shell != "zsh" && shell != "sh" {
			success, statusLabel, failureReason = false, "failed", "write_failed"
		}
	}
	values := map[string]string{
		"app.version": profile.client.version, "auth_mode": "Chatgpt", "model": profile.model,
		"originator": codexMetricOriginator(profile), "service_name": codexMetricProductService(profile),
		"session_source": codexMetricSessionSource(profile), "status": statusLabel, "success": strconv.FormatBool(success),
		"error": failureReason, "outcome": statusLabel, "active": strconv.FormatBool(profile.input.ProxyID != nil),
		"is_git": "false", "is_worktree": "unknown", "tty": "false", "cache": "miss", "compression_enabled": "false",
		"execution_mode": "sync", "handler_type": "mcp_tool", "hook_name": "Stop", "source": "plugin",
		"migration_type": "config", "tmp_mem_enabled": "false", "value": strconv.FormatBool(codexSimulatedHookCount(profile) > 0),
		"candidate_set_truncated": "false", "catalog_surface": "thread_context", "config_use_memories": "false",
		"db": "state", "directory": "codex_home", "event": "clear", "feature": "hooks",
		"feature_enabled": "false", "failure_reason": failureReason, "force_refresh": "false",
		"has_citations": "false", "include_tools": "false", "kind": "response." + status,
		"method": "weighted_lexical_v1", "mode": "legacy", "observation": "installed",
		"path": "new", "phase": "open_state", "query_script": "mixed", "query_truncated": "false",
		"read_allowed": "false", "refresh": "not_requested", "reload": "false", "result": "published",
		"retained_previous_snapshot": "false", "server_kind": "openai_codex_apps", "trigger": "initial", "version": "v1",
	}
	return values[name]
}

func codexMetricResourceService(profile codexTelemetryProfile) string {
	switch strings.ToLower(codexClientName(profile)) {
	case "codex desktop", "codex_vscode", "codex-app-server", "codex-app-server-sdk":
		return "codex-app-server"
	}
	return "codex_cli_rs"
}

func codexMetricSanitizeTag(input string) string {
	value := strings.Map(func(char rune) rune {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("._-/", char) {
			return char
		}
		return '_'
	}, input)
	value = strings.Trim(value, "_")
	if len(value) > 256 {
		return value[:256]
	}
	return value
}

func codexMetricOriginator(profile codexTelemetryProfile) string {
	value := codexMetricSanitizeTag(profile.client.originator)
	switch value {
	case "codex_desktop", "codex-app-server", "codex_mcp_server", "codex_cli_rs", "codex-tui", "codex_vscode", "none", "codex_exec", "codex-cli", "codex_sdk_ts", "codex-app-server-sdk":
		return value
	}
	return "other"
}

func codexMetricProductService(profile codexTelemetryProfile) string {
	// service_name is an optional app-server session parameter. A User-Agent or
	// originator identifies a client, not the configured metric service label.
	return ""
}

func codexMetricSessionSource(profile codexTelemetryProfile) string {
	if source := strings.TrimSpace(profile.input.ThreadSource); source == "subagent" || source == "guardian_review" || source == "guardian" {
		return "subagent"
	}
	switch strings.ToLower(codexClientName(profile)) {
	case "codex desktop", "codex_vscode", "codex-app-server", "codex-app-server-sdk":
		return "vscode"
	case "codex_exec":
		return "exec"
	}
	return "cli"
}

func codexStartupMetricValue(profile codexTelemetryProfile, descriptor codexMetricDescriptor) float64 {
	if descriptor.kind == "sum" {
		return 1
	}
	limit := 64
	if descriptor.unit == "ms" {
		limit = 4000
	} else if strings.Contains(descriptor.name, "bytes") {
		limit = 262144
	}
	switch descriptor.name {
	case "codex.thread.skills.enabled_total", "codex.thread.skills.kept_total":
		return float64(simulatedInt(profile.scenarioSeed+":skills", 64))
	case "codex.thread.skills.truncated", "codex.thread.skills.description_truncated_chars":
		return 0
	}
	return float64(simulatedInt(profile.scenarioSeed+":"+descriptor.name, limit))
}

// The durable store contains aggregates and a whitelisted profile snapshot,
// never bearer tokens, proxy URLs, response contents, or full business inputs.
type codexMetricStoreSnapshot struct {
	CompatibilityReason string                     `json:"compatibility_reason,omitempty"`
	Version             int                        `json:"version"`
	States              []codexMetricStateSnapshot `json:"states"`
	Clients             map[string]time.Time       `json:"clients"`
}

const codexMetricLegacyBytesDiscarded = "legacy_byte_histogram_discarded"

type codexMetricStateSnapshot struct {
	Profile           json.RawMessage                `json:"profile"`
	LastSeen          time.Time                      `json:"last_seen"`
	CollectedAt       time.Time                      `json:"collected_at"`
	Source            string                         `json:"source,omitempty"`
	Turns             int                            `json:"turns"`
	Attempts          int                            `json:"attempts"`
	ExternalAgentSent bool                           `json:"external_agent_sent"`
	Pending           []codexMetricAggregateSnapshot `json:"pending"`
}

type codexMetricAggregateSnapshot struct {
	Name       string    `json:"name"`
	Kind       string    `json:"kind"`
	Unit       string    `json:"unit"`
	Attributes []any     `json:"attributes"`
	Started    time.Time `json:"started"`
	Finished   time.Time `json:"finished"`
	Count      uint64    `json:"count"`
	Sum        float64   `json:"sum"`
	Minimum    float64   `json:"min"`
	Maximum    float64   `json:"max"`
	Buckets    []uint64  `json:"buckets"`
}

func marshalCodexTelemetryMetricStore(store *codexTelemetryMetricStore) ([]byte, error) {
	snapshot := codexMetricStoreSnapshot{Version: 2, Clients: make(map[string]time.Time)}
	if store == nil {
		return json.Marshal(snapshot)
	}
	snapshot.CompatibilityReason = store.compatibilityReason
	for key, value := range store.clients {
		snapshot.Clients[key] = value
	}
	keys := make([]string, 0, len(store.states))
	for key := range store.states {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		state := store.states[key]
		profile, err := marshalCodexTelemetryProfile(state.profile)
		if err != nil {
			return nil, err
		}
		item := codexMetricStateSnapshot{
			Profile: profile, LastSeen: state.lastSeen, CollectedAt: state.collectedAt, Source: state.source,
			Turns: state.turns, Attempts: state.attempts, ExternalAgentSent: state.externalAgentSent,
		}
		pendingKeys := make([]string, 0, len(state.pending))
		for pendingKey := range state.pending {
			pendingKeys = append(pendingKeys, pendingKey)
		}
		sort.Strings(pendingKeys)
		for _, pendingKey := range pendingKeys {
			aggregate := state.pending[pendingKey]
			item.Pending = append(item.Pending, codexMetricAggregateSnapshot{
				Name: aggregate.descriptor.name, Kind: aggregate.descriptor.kind, Unit: aggregate.descriptor.unit,
				Attributes: aggregate.attributes, Started: aggregate.started, Finished: aggregate.finished,
				Count: aggregate.count, Sum: aggregate.sum, Minimum: aggregate.minimum, Maximum: aggregate.maximum,
				Buckets: append([]uint64(nil), aggregate.buckets...),
			})
		}
		snapshot.States = append(snapshot.States, item)
	}
	return json.Marshal(snapshot)
}

func unmarshalCodexTelemetryMetricStore(encoded []byte) (*codexTelemetryMetricStore, error) {
	store := newCodexTelemetryMetricStore()
	if len(encoded) == 0 {
		return store, nil
	}
	var snapshot codexMetricStoreSnapshot
	if err := json.Unmarshal(encoded, &snapshot); err != nil {
		return nil, err
	}
	if (snapshot.Version != 1 && snapshot.Version != 2) || len(snapshot.States) > codexTelemetryMetricStateLimit || len(snapshot.Clients) > codexTelemetryMetricStateLimit {
		return nil, errors.New("unsupported telemetry metric snapshot")
	}
	// Keep only a fixed local compatibility marker. It is persisted with the
	// aggregate transaction and is never part of an exported metric batch.
	if snapshot.CompatibilityReason == codexMetricLegacyBytesDiscarded {
		store.compatibilityReason = codexMetricLegacyBytesDiscarded
	}
	for key, value := range snapshot.Clients {
		store.clients[key] = value
	}
	for _, item := range snapshot.States {
		profile, err := unmarshalCodexTelemetryProfile(item.Profile)
		if err != nil {
			return nil, err
		}
		state := &codexMetricState{
			profile: profile, lastSeen: item.LastSeen, collectedAt: item.CollectedAt, source: item.Source,
			turns: item.Turns, attempts: item.Attempts, externalAgentSent: item.ExternalAgentSent,
			pending: make(map[string]*codexMetricAggregate),
		}
		for _, item := range item.Pending {
			descriptor := codexMetricDescriptor{name: item.Name, kind: item.Kind, unit: item.Unit}
			if snapshot.Version == 1 && codexMetricHasMigratedByteBounds(item.Name) {
				if item.Kind != "histogram" || item.Unit != "" || len(item.Buckets) != len(codexValueHistogramBounds)+1 {
					return nil, errors.New("invalid legacy telemetry byte histogram")
				}
				// Version 1 retained no original samples or bounds. These three
				// pending distributions cannot be rebucketed without inventing data.
				// Preserve the rest of the state; already sealed batches are untouched.
				store.compatibilityReason = codexMetricLegacyBytesDiscarded
				continue
			}
			if !codexStatsigMetricAllowed(item.Name) || (item.Kind != "sum" && item.Kind != "histogram") || len(item.Buckets) != len(codexMetricBounds(descriptor))+1 {
				return nil, errors.New("invalid telemetry metric aggregate")
			}
			if codexMetricIsEventMetric(item.Name) {
				var err error
				item.Attributes, err = state.eventSnapshotAttributes(item.Attributes)
				if err != nil {
					return nil, err
				}
			}
			attributes, err := json.Marshal(item.Attributes)
			if err != nil {
				return nil, err
			}
			key := item.Name + ":" + string(attributes)
			incoming := &codexMetricAggregate{
				descriptor: descriptor, attributes: item.Attributes, started: item.Started, finished: item.Finished,
				count: item.Count, sum: item.Sum, minimum: item.Minimum, maximum: item.Maximum, buckets: append([]uint64(nil), item.Buckets...),
			}
			if existing := state.pending[key]; existing != nil && codexMetricIsEventMetric(item.Name) {
				if existing.descriptor != incoming.descriptor {
					return nil, errors.New("conflicting event metric aggregates")
				}
				mergeCodexMetricAggregate(existing, incoming)
			} else {
				state.pending[key] = incoming
			}
		}
		store.states[codexMetricStateKey(profile)] = state
	}
	return store, nil
}

func codexMetricIsEventMetric(name string) bool {
	switch name {
	case "codex.websocket.event", "codex.websocket.event.duration_ms", "codex.sse_event", "codex.sse_event.duration_ms":
		return true
	default:
		return false
	}
}

// v1 aggregates can lack the kind label. Normalize both snapshot versions so
// only known protocol kinds survive in pending data, and canonicalize the
// attributes before merging any series that now share the unknown label.
func (s *codexMetricState) eventSnapshotAttributes(attributes []any) ([]any, error) {
	values := make(map[string]string, len(attributes)+1)
	for _, raw := range attributes {
		encoded, err := json.Marshal(raw)
		if err != nil {
			return nil, err
		}
		var attribute struct {
			Key   string `json:"key"`
			Value struct {
				StringValue *string `json:"stringValue"`
			} `json:"value"`
		}
		if err := json.Unmarshal(encoded, &attribute); err != nil || attribute.Key == "" || attribute.Value.StringValue == nil {
			return nil, errors.New("invalid event metric attributes")
		}
		values[attribute.Key] = *attribute.Value.StringValue
	}
	values["kind"] = s.eventMetricKind(values["kind"], values["success"])
	return codexOTLPAttributes(values), nil
}

func mergeCodexMetricAggregate(dst, src *codexMetricAggregate) {
	if src.count == 0 {
		return
	}
	if dst.count == 0 {
		dst.started, dst.finished = src.started, src.finished
		dst.minimum, dst.maximum = src.minimum, src.maximum
	} else {
		if src.started.Before(dst.started) {
			dst.started = src.started
		}
		if src.finished.After(dst.finished) {
			dst.finished = src.finished
		}
		dst.minimum, dst.maximum = math.Min(dst.minimum, src.minimum), math.Max(dst.maximum, src.maximum)
	}
	dst.count += src.count
	dst.sum += src.sum
	for index, count := range src.buckets {
		dst.buckets[index] += count
	}
}

func codexMetricHasMigratedByteBounds(name string) bool {
	switch name {
	case "codex.app_server.codex_home.size_bytes", "codex.sqlite.logs.write.bytes", "codex.sqlite.logs.write.max_entry_bytes":
		return true
	}
	return false
}

func (s *codexTelemetryMetricStore) nextFlushAt() time.Time {
	var next time.Time
	for _, state := range s.states {
		if len(state.pending) == 0 {
			continue
		}
		due := state.collectedAt.Add(time.Minute)
		if next.IsZero() || due.Before(next) {
			next = due
		}
	}
	return next
}

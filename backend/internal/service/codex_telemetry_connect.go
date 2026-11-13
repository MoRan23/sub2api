package service

import (
	"context"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

// CodexTelemetryConnectInput is a bounded, credential-free physical route
// snapshot. It is safe to retain for delayed prewarm, unlike an account, Gin
// context, headers or a request body. NewSession requires explicit evidence.
type CodexTelemetryConnectInput struct {
	AccountID, OwnerAccountID, ProxyID, CredentialRevision            int64
	AccountName, OSFamily, CredentialOS, InstallationID               string
	AuthorizationGeneration, SessionID, Route                         string
	UserAgent, Originator, Version, Model, ProductSKU, CodexResidency string
	NewSession                                                        bool
}

type codexTelemetryConnectKey struct {
	owner                            int64
	os, installation, session, route string
}

type codexTelemetryConnectState struct {
	active     int
	conflicts  uint64
	lastSeen   time.Time
	baseline   bool
	generation string
	revision   int64
}

// CodexTelemetryConnectObserver records only an actual Dial, once. It does not
// allocate logical turns or sampling attempts, including failed handshakes.
type CodexTelemetryConnectObserver struct {
	once         sync.Once
	service      *CodexTelemetryService
	input        CodexTelemetryConnectInput
	epoch        uint64
	policyEpoch  int64
	started      time.Time
	key          codexTelemetryConnectKey
	state        *codexTelemetryConnectState
	conflicts    uint64
	ambiguous    bool
	scopeChanged bool
	reauth       *bool
}

type codexTelemetryConnectMeasurement struct {
	profile     codexTelemetryProfile
	pool        CodexTelemetryPoolKey
	epoch       uint64
	policyEpoch int64
	finished    time.Time
	durationMS  float64
	success     bool
	reauth      *bool
}

// prepareCodexTelemetryConnect uses final handshake headers and an already
// resolved OAuth slot. It does not resolve credentials or perform network I/O.
func (s *OpenAIGatewayService) prepareCodexTelemetryConnect(ctx context.Context, account *Account, headers http.Header, endpoint, model string) func() *CodexTelemetryConnectObserver {
	if s == nil || s.codexTelemetry == nil || account == nil || !account.IsOpenAIOAuth() || !s.codexTelemetry.connectEnabled() || s.isAgentIdentityAccount(ctx, account) {
		return nil
	}
	owner := account.OpenAIOAuthCredentialOwnerID
	if owner <= 0 {
		return nil
	}
	ua := codexTelemetryHeader(headers, "User-Agent")
	input := CodexTelemetryConnectInput{
		AccountID: account.ID, OwnerAccountID: owner, AccountName: account.Name,
		CredentialOS:            account.OpenAIOAuthCredentialOS,
		AuthorizationGeneration: account.OpenAIOAuthAuthorizationGeneration,
		CredentialRevision:      account.OpenAIOAuthCredentialRevision,
		OSFamily:                openai.DetectOSFamilyFromUserAgent(ua), UserAgent: ua,
		InstallationID: codexTelemetryConnectUUID(headers, codexInstallationIDKey),
		SessionID:      codexTelemetryConnectUUID(headers, "session_id", "session-id"),
		Originator:     codexTelemetryHeader(headers, "originator"),
		Version:        firstNonEmptyString(codexTelemetryHeader(headers, "version"), codexClientVersionFromUA(ua)),
		Model:          model, ProductSKU: codexTelemetryAuxiliaryHeader(headers, "X-OpenAI-Product-Sku"),
		CodexResidency: codexTelemetryAuxiliaryHeader(headers, "X-OpenAI-Internal-Codex-Residency"),
	}
	if account.ProxyID != nil {
		input.ProxyID = *account.ProxyID
	}
	if ctx != nil {
		if snapshot, ok := ctx.Value(codexTelemetryGatewayContextKey{}).(codexTelemetryGatewaySnapshot); ok {
			if snapshot.ownerID != owner || snapshot.credentialOS != input.CredentialOS || snapshot.authorizationGeneration != input.AuthorizationGeneration {
				return nil
			}
			input.ProxyID = snapshot.route.ProxyID
		}
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" {
		return nil
	}
	// Do not retain URL credentials or arbitrary query values in the tracker.
	input.Route = parsed.Scheme + "://" + parsed.Host + parsed.EscapedPath() + "#proxy=" + strconv.FormatInt(input.ProxyID, 10)
	return s.codexTelemetry.PrepareConnect(input)
}

func codexTelemetryConnectUUID(headers http.Header, names ...string) string {
	var selected string
	for _, name := range names {
		for _, raw := range headerValuesCaseInsensitive(headers, name) {
			value := codexTelemetryUUID(raw)
			if value == "" || selected != "" && selected != value {
				return ""
			}
			selected = value
		}
	}
	return selected
}

// PrepareConnect freezes policy and scalars without starting a measurement. The
// returned factory must be invoked immediately before Dial, never on pool hits.
func (s *CodexTelemetryService) PrepareConnect(input CodexTelemetryConnectInput) func() *CodexTelemetryConnectObserver {
	if s == nil || input.AccountID <= 0 || input.OwnerAccountID <= 0 || input.InstallationID == "" || NormalizeOpenAIOSFamily(input.CredentialOS) == "" || input.AuthorizationGeneration == "" {
		return nil
	}
	input.CredentialOS = NormalizeOpenAIOSFamily(input.CredentialOS)
	s.mu.Lock()
	if !s.enabledLocked() || !s.observationEnabled || s.store == nil {
		s.mu.Unlock()
		return nil
	}
	epoch, policy := s.epoch, s.sharedEpoch
	s.mu.Unlock()
	for _, value := range []*string{&input.AccountName, &input.OSFamily, &input.CredentialOS, &input.InstallationID, &input.AuthorizationGeneration, &input.SessionID, &input.Route, &input.UserAgent, &input.Originator, &input.Version, &input.Model, &input.ProductSKU, &input.CodexResidency} {
		if len(*value) > 512 {
			return nil
		}
		*value = strings.Clone(*value)
	}
	return func() *CodexTelemetryConnectObserver { return s.beginConnect(input, epoch, policy, time.Now()) }
}

func (s *CodexTelemetryService) connectEnabled() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enabledLocked() && s.observationEnabled && s.store != nil
}

func (s *CodexTelemetryService) beginConnect(input CodexTelemetryConnectInput, epoch uint64, policy int64, now time.Time) *CodexTelemetryConnectObserver {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabledLocked() || !s.observationEnabled || s.epoch != epoch || s.sharedEpoch != policy {
		return nil
	}
	o := &CodexTelemetryConnectObserver{service: s, input: input, epoch: epoch, policyEpoch: policy, started: now}
	if input.SessionID == "" || input.Route == "" || input.CredentialRevision <= 0 {
		return o
	}
	o.key = codexTelemetryConnectKey{input.OwnerAccountID, input.CredentialOS, input.InstallationID, input.SessionID, input.Route}
	if s.connectStates == nil {
		s.connectStates = make(map[codexTelemetryConnectKey]*codexTelemetryConnectState)
	}
	state := s.connectStates[o.key]
	if state != nil && state.active == 0 && now.Sub(state.lastSeen) >= codexTelemetryStateTTL {
		delete(s.connectStates, o.key)
		state = nil
	}
	if state == nil {
		if len(s.connectStates) >= codexTelemetryMaxStates {
			var oldestKey codexTelemetryConnectKey
			var oldest time.Time
			for key, candidate := range s.connectStates {
				if candidate.active == 0 && (oldest.IsZero() || candidate.lastSeen.Before(oldest)) {
					oldestKey, oldest = key, candidate.lastSeen
				}
			}
			if !oldest.IsZero() {
				delete(s.connectStates, oldestKey)
			} else {
				return o
			}
		}
		state = &codexTelemetryConnectState{}
		s.connectStates[o.key] = state
	}
	o.state, o.conflicts, o.ambiguous = state, state.conflicts, state.active > 0
	if state.active > 0 {
		state.conflicts++
	} else if state.baseline || input.NewSession {
		value := state.baseline && (state.generation != input.AuthorizationGeneration || state.revision != input.CredentialRevision)
		o.reauth = &value
	}
	state.active++
	state.lastSeen = now
	return o
}

func (o *CodexTelemetryConnectObserver) Finish(success bool) {
	o.FinishWithHeaders(success, nil)
}

// FinishWithHeaders uses the physical connection's final handshake headers,
// after a credential factory or redirect policy. Only small scalar values are
// copied; the header map is never retained. Nil keeps the prepared snapshot.
func (o *CodexTelemetryConnectObserver) FinishWithHeaders(success bool, headers http.Header) {
	if o == nil {
		return
	}
	o.once.Do(func() {
		finished := time.Now()
		if headers != nil {
			installation := codexTelemetryConnectUUID(headers, codexInstallationIDKey)
			session := codexTelemetryConnectUUID(headers, "session_id", "session-id")
			ua := codexTelemetryHeader(headers, "User-Agent")
			if len(ua) > 512 {
				ua = ""
			}
			o.scopeChanged = installation != o.input.InstallationID || session != o.input.SessionID || ua != o.input.UserAgent
			o.input.InstallationID, o.input.SessionID = installation, session
			o.input.UserAgent, o.input.OSFamily = strings.Clone(ua), openai.DetectOSFamilyFromUserAgent(ua)
			o.input.Originator = codexTelemetryConnectScalar(codexTelemetryHeader(headers, "originator"))
			o.input.Version = codexTelemetryConnectScalar(firstNonEmptyString(codexTelemetryHeader(headers, "version"), codexClientVersionFromUA(ua)))
			o.input.ProductSKU = codexTelemetryAuxiliaryHeader(headers, "X-OpenAI-Product-Sku")
			o.input.CodexResidency = codexTelemetryAuxiliaryHeader(headers, "X-OpenAI-Internal-Codex-Residency")
		}
		o.finish(success, finished)
	})
}

func codexTelemetryConnectScalar(value string) string {
	if len(value) > 512 {
		return ""
	}
	return strings.Clone(value)
}

func (o *CodexTelemetryConnectObserver) finish(success bool, finished time.Time) {
	s := o.service
	s.mu.Lock()
	defer s.mu.Unlock()
	if o.epoch != s.epoch || o.policyEpoch != s.sharedEpoch || !s.enabledLocked() || !s.observationEnabled {
		return
	}
	reauth := o.reauth
	if state := o.state; state != nil {
		state.active--
		state.lastSeen = finished
		if o.scopeChanged {
			// The pre-Dial scope was not the final physical identity. Do not
			// relabel its prior success with this connection's authorization.
			reauth = nil
		} else if o.ambiguous || state.conflicts != o.conflicts {
			reauth = nil
			// Failed overlapping handshakes do not change the last successful
			// authorization. A successful overlap has unknown ordering instead.
			if success {
				state.baseline = false
			}
		} else if success {
			state.baseline, state.generation, state.revision = true, o.input.AuthorizationGeneration, o.input.CredentialRevision
		}
	}
	if o.input.InstallationID == "" {
		return // No reliable actual installation pool after header projection.
	}
	profile := o.input.profile(o.started)
	if reauth == nil {
		profile.reasons = []string{"connect_reauth_unknown"}
	}
	measurement := &codexTelemetryConnectMeasurement{profile: profile, pool: CodexTelemetryPoolKey{OwnerAccountID: o.input.OwnerAccountID, OSFamily: o.input.CredentialOS, InstallationID: o.input.InstallationID}, epoch: o.epoch, policyEpoch: o.policyEpoch, finished: finished, durationMS: math.Max(0, float64(finished.Sub(o.started))/float64(time.Millisecond)), success: success, reauth: reauth}
	if s.mutationReservations+1 > codexTelemetryQueueSize {
		return
	}
	select {
	case s.mutations <- codexTelemetryMutation{connect: measurement}:
		s.mutationReservations++
	default:
	}
}

func (i CodexTelemetryConnectInput) profile(started time.Time) codexTelemetryProfile {
	input := CodexTelemetryInput{AccountID: i.AccountID, OwnerAccountID: i.OwnerAccountID, AccountName: i.AccountName, OSFamily: i.OSFamily, CredentialOS: i.CredentialOS, AuthorizationGeneration: i.AuthorizationGeneration, InstallationID: i.InstallationID, UserAgent: i.UserAgent, Originator: i.Originator, Version: i.Version, Model: i.Model, ProductSKU: i.ProductSKU, CodexResidency: i.CodexResidency, SessionID: i.SessionID, WebSocket: true, StartedAt: started}
	if i.ProxyID > 0 {
		id := i.ProxyID
		input.ProxyID = &id
	}
	return codexTelemetryProfile{input: input, client: codexTelemetryClient{localID: i.AccountID, name: i.AccountName, userAgent: i.UserAgent, originator: i.Originator, version: i.Version}, sessionID: i.SessionID, model: i.Model, started: started, websocket: true, observationEnabled: true, source: "observed"}
}

func (s *CodexTelemetryService) applyConnectMutation(m codexTelemetryConnectMeasurement) {
	s.mu.Lock()
	store, valid := s.store, s.enabledLocked() && s.observationEnabled && m.epoch == s.epoch && m.policyEpoch == s.sharedEpoch
	s.mu.Unlock()
	var err error
	if valid && store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, err = store.TransactPool(ctx, m.pool, m.finished, func(tx *CodexTelemetryPoolTransaction) error {
			if !tx.Policy.Enabled || !tx.Policy.Observation {
				return ErrCodexTelemetryPolicyDisabled
			}
			if tx.Policy.Epoch != m.policyEpoch {
				return ErrCodexTelemetryPolicyChanged
			}
			if err := syncCodexTelemetryRuntimeEpoch(tx, m.finished); err != nil {
				return err
			}
			metrics, err := loadRuntimeMetrics(tx)
			if err != nil {
				return err
			}
			m.profile.poolID = tx.Pool.ID
			metrics.recordConnect(m)
			return saveRuntimeMetrics(tx, metrics, m.finished)
		})
		cancel()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if m.epoch == s.epoch {
		s.mutationReservations--
	}
	if valid && err == nil {
		s.wakePersistentRuntime()
	}
	// A failed/ambiguous commit is not retried: no duplicate physical sample.
}

func (s *codexTelemetryMetricStore) recordConnect(m codexTelemetryConnectMeasurement) {
	state, _ := s.state(m.profile, m.finished)
	tags := map[string]string{"success": strconv.FormatBool(m.success)}
	if m.reauth != nil {
		tags["reauth"] = strconv.FormatBool(*m.reauth)
	}
	if sku := m.profile.input.ProductSKU; sku != "" {
		tags["product_sku"] = "other"
		if sku == "codex" {
			tags["product_sku"] = "codex"
		}
	}
	for _, descriptor := range codexMetricDescriptors {
		switch descriptor.name {
		case "codex.websocket.connect":
			state.addWithSource(descriptor, 1, m.profile, "", m.finished, tags, "observed")
		case "codex.websocket.connect.duration_ms":
			state.addWithSource(descriptor, m.durationMS, m.profile, "", m.finished, tags, "observed")
		}
	}
}

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"golang.org/x/sync/singleflight"
)

const (
	codexEngineBillingWorkers     = 8
	codexEngineBillingMaxPages    = 4
	codexEngineBillingLease       = time.Minute
	codexEngineBillingPassTimeout = 45 * time.Second
)

// CodexEngineBillingSettleFunc must atomically deduplicate the immutable event,
// apply the original pricing snapshot, and insert its usage log. It then refreshes
// required caches before acknowledging the received event. An error keeps the retry;
// the committed ledger deduplicates money if a cache refresh needs another pass.
type CodexEngineBillingSettleFunc func(context.Context, *CodexEngineBillingJob, *CodexEngineBillingUsageEvent) error

type codexEngineBillingAccountReader interface {
	GetByID(context.Context, int64) (*Account, error)
}

// CodexEngineBillingService reconciles durable pending requests independently
// of the client's connection. Network requests never hold a ledger transaction.
type CodexEngineBillingService struct {
	store          CodexEngineBillingStore
	accounts       codexEngineBillingAccountReader
	upstream       HTTPUpstream
	cfg            *config.Config
	settle         CodexEngineBillingSettleFunc
	mu             sync.Mutex
	started        bool
	stopped        bool
	cancel         context.CancelFunc
	wg             sync.WaitGroup
	slots          chan struct{}
	wake           chan struct{}
	interval       time.Duration
	identities     map[string]codexEngineBillingIdentityCache
	identityFlight singleflight.Group
}

type codexEngineBillingIdentityCache struct {
	instanceID string
	expires    time.Time
}

func NewCodexEngineBillingService(store CodexEngineBillingStore, accounts AccountRepository, upstream HTTPUpstream, cfg *config.Config) *CodexEngineBillingService {
	return &CodexEngineBillingService{store: store, accounts: accounts, upstream: upstream, cfg: cfg,
		slots: make(chan struct{}, codexEngineBillingWorkers), wake: make(chan struct{}, 1), interval: time.Second, identities: map[string]codexEngineBillingIdentityCache{}}
}

// SetSettler is wired before Start, avoiding a dependency cycle with the gateway.
func (s *CodexEngineBillingService) SetSettler(settle CodexEngineBillingSettleFunc) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started {
		s.settle = settle
	}
}

// Prepare must succeed before dispatching an Engine model request. Unknown or
// missing response headers remain recoverable through the client request ID.
func (s *CodexEngineBillingService) Prepare(ctx context.Context, pending *CodexEngineBillingPending) (*CodexEngineBillingJob, error) {
	if s == nil || s.store == nil || s.accounts == nil || pending == nil || pending.IdentityDigest == "" || !validCodexEngineClientRequestID(pending.ClientRequestID) {
		return nil, ErrCodexEngineBillingConflict
	}
	account, err := s.accounts.GetByID(ctx, pending.AccountID)
	if err != nil {
		return nil, err
	}
	if CodexEngineBillingIdentity(account) != pending.IdentityDigest {
		return nil, ErrCodexEngineBillingConflict
	}
	instanceID, err := s.discoverIdentity(ctx, account, pending.IdentityDigest)
	if err != nil {
		return nil, err
	}
	current, err := s.accounts.GetByID(ctx, pending.AccountID)
	if err != nil {
		return nil, err
	}
	if CodexEngineBillingIdentity(current) != pending.IdentityDigest {
		return nil, ErrCodexEngineBillingConflict
	}
	copyPending := *pending
	copyPending.InstanceID = instanceID
	job, err := s.store.CreatePending(ctx, &copyPending)
	if err == nil {
		s.Wake()
	}
	return job, err
}

// ObserveResponse checks physical response evidence without charging its public
// usage. Missing headers leave the pre-dispatch job available for lookup.
func (s *CodexEngineBillingService) ObserveResponse(job *CodexEngineBillingJob, headers http.Header) error {
	if s == nil || job == nil {
		return ErrCodexEngineBillingProtocol
	}
	s.Wake()
	// Proxies and a disconnect can lose individual response headers. The durable
	// client correlation recovers those receipts without disrupting inference.
	// Supplied evidence must still agree with the pre-dispatch discovered owner.
	for name, want := range map[string]string{CodexEngineBillingContractHeader: CodexEngineBillingContract, CodexEngineBillingInstanceHeader: job.InstanceID} {
		var values []string
		for key, entries := range headers {
			if strings.EqualFold(key, name) {
				values = append(values, entries...)
			}
		}
		if len(values) > 0 && (len(values) != 1 || values[0] != want) {
			return ErrCodexEngineBillingProtocol
		}
	}
	return nil
}

// NotDispatched is reserved for local proof that no business request was sent.
// It is never called for a 404 lookup, disconnect, ambiguous write or timeout.
func (s *CodexEngineBillingService) NotDispatched(ctx context.Context, job *CodexEngineBillingJob) error {
	if s == nil || s.store == nil || job == nil {
		return ErrCodexEngineBillingConflict
	}
	return s.store.MarkNotDispatched(ctx, job.ID)
}

func (s *CodexEngineBillingService) discoverIdentity(ctx context.Context, account *Account, identity string) (string, error) {
	s.mu.Lock()
	cached := s.identities[identity]
	s.mu.Unlock()
	if time.Now().Before(cached.expires) {
		return cached.instanceID, nil
	}
	result := s.identityFlight.DoChan(identity, func() (any, error) {
		s.mu.Lock()
		cached := s.identities[identity]
		s.mu.Unlock()
		if time.Now().Before(cached.expires) {
			return cached.instanceID, nil
		}
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		data, _, err := s.query(fetchCtx, account, "/v1/engine/capacity", nil)
		if err != nil {
			return nil, err
		}
		if err := validateCodexEngineBillingJSON(data); err != nil {
			return nil, err
		}
		var doc struct {
			Object       string `json:"object"`
			InstanceID   string `json:"instance_id"`
			Capabilities struct {
				UsageReceipts   bool   `json:"usage_receipts"`
				BillingContract string `json:"billing_contract"`
			} `json:"capabilities"`
		}
		if err := json.Unmarshal(data, &doc); err != nil || doc.Object != "codex_engine.capacity" || !validCodexEngineInstanceID(doc.InstanceID) || !doc.Capabilities.UsageReceipts || doc.Capabilities.BillingContract != CodexEngineBillingContract {
			return nil, ErrCodexEngineBillingProtocol
		}
		s.mu.Lock()
		// Only short-lived identities are retained; no keys or account snapshots.
		for key, value := range s.identities {
			if time.Now().After(value.expires) {
				delete(s.identities, key)
			}
		}
		s.identities[identity] = codexEngineBillingIdentityCache{instanceID: doc.InstanceID, expires: time.Now().Add(time.Minute)}
		s.mu.Unlock()
		return doc.InstanceID, nil
	})
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case r := <-result:
		if r.Err != nil {
			return "", r.Err
		}
		return r.Val.(string), nil
	}
}

func (s *CodexEngineBillingService) query(ctx context.Context, account *Account, path string, values url.Values) ([]byte, http.Header, error) {
	if s.upstream == nil {
		return nil, nil, errors.New("Engine billing transport unavailable")
	}
	target, err := codexEngineURL(account.GetOpenAIBaseURL(), path)
	if err != nil {
		return nil, nil, ErrCodexEngineBillingProtocol
	}
	target, err = cnValidateProbeURL(s.cfg, target)
	if err != nil {
		return nil, nil, errors.New("Engine billing URL policy rejected")
	}
	u, err := url.Parse(target)
	if err != nil {
		return nil, nil, ErrCodexEngineBillingProtocol
	}
	u.RawQuery = values.Encode()
	proxy := ""
	if account.ProxyID != nil {
		if account.Proxy == nil || account.Proxy.ID != *account.ProxyID {
			return nil, nil, errors.New("Engine billing proxy unavailable")
		}
		proxy = account.Proxy.URL()
	}
	queryCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	requestCtx := WithHTTPUpstreamRedirectsDisabled(WithHTTPUpstreamProfile(queryCtx, HTTPUpstreamProfileOpenAI))
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, nil, ErrCodexEngineBillingProtocol
	}
	req.Header.Set("Authorization", "Bearer "+account.GetOpenAIApiKey())
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cache-Control", "no-store")
	resp, err := s.upstream.Do(req, proxy, account.ID, maxInt(account.Concurrency, 1))
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return nil, nil, errors.New("Engine billing query failed")
	}
	if resp == nil || resp.Body == nil {
		return nil, nil, ErrCodexEngineBillingProtocol
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, resp.Header, fmt.Errorf("Engine billing query HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, codexEngineBillingMaxBody+1))
	if err != nil || len(body) > codexEngineBillingMaxBody {
		return nil, resp.Header, ErrCodexEngineBillingProtocol
	}
	return body, resp.Header, nil
}

func (s *CodexEngineBillingService) reconcile(parent context.Context, job *CodexEngineBillingJob) {
	ctx, cancel := context.WithTimeout(parent, codexEngineBillingPassTimeout)
	defer cancel()
	state, reason := job.State, "query_pending"
	if state != "blocked" {
		state = CodexEngineBillingPendingState
	}
	var seal json.RawMessage
	defer func() { s.saveProgress(parent, job, state, reason, seal) }()
	// Already received facts are independent of future credential validity. A
	// durable local pricing cursor prevents an early missing tariff from starving
	// later received events, including after restarts or account edits.
	pending, settlementErr := s.settleReceived(ctx, job)
	if errors.Is(settlementErr, ErrCodexEngineBillingClaimLost) {
		return
	}
	if errors.Is(settlementErr, ErrCodexEngineBillingConflict) {
		state, reason = CodexEngineBillingBlockedState, "event_facts_conflict"
		return
	}
	if len(job.SourceSeal) > 0 {
		if settlementErr == nil && !pending {
			state, reason, seal = CodexEngineBillingSealedState, "", job.SourceSeal
		} else {
			reason = "event_settlement_pending"
		}
		return
	}
	if state == CodexEngineBillingBlockedState {
		reason = "query_blocked_local_settlement"
		return
	}
	if !validCodexEngineInstanceID(job.InstanceID) {
		state, reason = CodexEngineBillingBlockedState, "instance_identity_missing"
		return
	}
	before := job.ConsumedSequence
	state, reason = s.receivePages(ctx, job)
	if reason == "receipt_linked" {
		return
	}
	if job.ConsumedSequence > before {
		pending, settlementErr = s.settleReceived(ctx, job)
	}
	if errors.Is(settlementErr, ErrCodexEngineBillingClaimLost) {
		return
	}
	if errors.Is(settlementErr, ErrCodexEngineBillingConflict) {
		state, reason = CodexEngineBillingBlockedState, "event_facts_conflict"
		return
	}
	if len(job.SourceSeal) > 0 {
		if settlementErr == nil && !pending {
			state, reason, seal = CodexEngineBillingSealedState, "", job.SourceSeal
		} else {
			reason = "event_settlement_pending"
		}
	}
}

func (s *CodexEngineBillingService) receivePages(ctx context.Context, job *CodexEngineBillingJob) (state, reason string) {
	state, reason = CodexEngineBillingPendingState, "query_pending"
	for pageIndex := 0; pageIndex < codexEngineBillingMaxPages; pageIndex++ {
		account, err := s.currentAccount(ctx, job)
		if err != nil {
			if errors.Is(err, ErrCodexEngineBillingConflict) {
				state, reason = CodexEngineBillingBlockedState, "upstream_identity_changed"
			}
			return state, reason
		}
		path := "/v1/usage/receipts"
		values := url.Values{"limit": []string{"100"}}
		if job.ReceiptID != "" {
			path += "/" + job.ReceiptID
		} else {
			values.Set("client_request_id", job.ClientRequestID)
		}
		if job.Cursor != "" {
			values.Set("cursor", job.Cursor)
		}
		body, headers, err := s.query(ctx, account, path, values)
		if err != nil {
			reason = "receipt_query_failed"
			return state, reason
		}
		// The request was authenticated with the original credential snapshot.
		// An administrator edit during this GET cannot invalidate returned facts;
		// it only prevents the next network request (checked at the loop's start).
		if err = ValidateCodexEngineBillingHeaders(headers, job.InstanceID); err != nil {
			state, reason = CodexEngineBillingBlockedState, "instance_or_contract_changed"
			return state, reason
		}
		page, err := ParseCodexEngineBillingPage(body)
		if err != nil {
			reason = "invalid_billing_contract"
			return state, reason
		}
		if page.InstanceID != job.InstanceID || job.ReceiptID != "" && page.ReceiptID != job.ReceiptID || job.ReceiptID == "" && page.ClientRequestID != job.ClientRequestID {
			state, reason = CodexEngineBillingBlockedState, "receipt_identity_mismatch"
			return state, reason
		}
		if page.Revision < job.Revision || page.PageThroughSequence < job.ConsumedSequence {
			reason = "receipt_revision_regressed"
			return state, reason
		}
		if job.ReceiptID == "" {
			bound, err := s.store.BindReceipt(ctx, job.ID, page.InstanceID, page.ReceiptID, job.ClaimToken)
			if err != nil || bound == nil {
				reason = "receipt_bind_failed"
				return state, reason
			}
			if bound.ID != job.ID {
				reason = "receipt_linked"
				return state, reason
			}
			job.ReceiptID = page.ReceiptID
		}
		sequence := job.ConsumedSequence
		received := make([]CodexEngineBillingReceivedEvent, 0, len(page.Events))
		for i := range page.Events {
			event := &page.Events[i]
			if event.Sequence != sequence+1 {
				reason = "receipt_sequence_gap"
				return state, reason
			}
			received = append(received, CodexEngineBillingReceivedEvent{EventID: event.EventID, Sequence: event.Sequence, Fingerprint: event.Fingerprint, Raw: event.Raw,
				Summary: CodexEngineBillingSummaryGroup{Kind: event.Kind, Purpose: event.Purpose, Model: event.Model, ServiceTier: optionalTrimmedStringPtr(event.ServiceTier), Usage: event.UsageValues}})
			sequence = event.Sequence
		}
		if sequence != page.PageThroughSequence {
			reason = "receipt_sequence_gap"
			return state, reason
		}
		if err := s.store.StageEvents(ctx, &CodexEngineBillingReceivedPage{JobID: job.ID, ClaimToken: job.ClaimToken, Cursor: page.NextCursor, Revision: page.Revision, ConsumedSequence: sequence, Events: received}); err != nil {
			reason = "receipt_stage_pending"
			if errors.Is(err, ErrCodexEngineBillingConflict) {
				state, reason = CodexEngineBillingBlockedState, "event_facts_conflict"
			}
			return state, reason
		}
		job.Cursor = page.NextCursor
		job.ConsumedSequence = sequence
		job.Revision = page.Revision
		if page.SummarySequence == job.ConsumedSequence {
			actual, err := s.store.ReadSummary(ctx, job.ID, job.ConsumedSequence)
			if err != nil {
				reason = "summary_check_pending"
				return state, reason
			}
			if !codexEngineBillingSummariesEqual(actual, page.Summary) {
				state, reason = CodexEngineBillingBlockedState, "receipt_summary_mismatch"
				return state, reason
			}
		}
		if !page.HasMore {
			if page.Finalized && job.ConsumedSequence == page.LastSequence {
				job.SourceSeal = page.Seal
				return state, "source_sealed"
			}
			seconds := page.PollAfterSeconds
			if seconds < 1 {
				seconds = 1
			}
			if seconds > 60 {
				seconds = 60
			}
			delay := time.Duration(seconds) * time.Second
			job.NextAttemptAt = time.Now().Add(delay)
			reason = "awaiting_source_seal"
			return state, reason
		}
	}
	job.NextAttemptAt = time.Now().Add(time.Second)
	reason = "more_receipt_pages"
	return state, reason
}

func (s *CodexEngineBillingService) settleReceived(ctx context.Context, job *CodexEngineBillingJob) (bool, error) {
	if job.ReceiptID == "" {
		return false, nil
	}
	var firstErr error
	for page := 0; page < codexEngineBillingMaxPages; page++ {
		events, err := s.store.ReadPendingReceived(ctx, job.ID, job.ClaimToken, job.SettlementCursor, 100)
		if err != nil {
			return true, err
		}
		if len(events) == 0 {
			job.SettlementCursor = 0
			break
		}
		for _, received := range events {
			event, err := ParseCodexEngineBillingUsageEvent(received.Raw)
			if err == nil && (event.EventID != received.EventID || event.Sequence != received.Sequence || event.Fingerprint != received.Fingerprint) {
				err = ErrCodexEngineBillingConflict
			}
			if err == nil {
				err = s.settle(ctx, job, &event)
			}
			if err != nil && firstErr == nil {
				firstErr = err
			}
			if errors.Is(err, ErrCodexEngineBillingClaimLost) || errors.Is(err, ErrCodexEngineBillingConflict) {
				return true, err
			}
			job.SettlementCursor = received.Sequence
		}
		if len(events) < 100 {
			job.SettlementCursor = 0
			break
		}
	}
	remaining, err := s.store.ReadPendingReceived(ctx, job.ID, job.ClaimToken, 0, 1)
	if err != nil {
		return true, err
	}
	return len(remaining) > 0, firstErr
}

func codexEngineBillingSummariesEqual(a, b []CodexEngineBillingSummaryGroup) bool {
	normalize := func(groups []CodexEngineBillingSummaryGroup) map[string]map[string]int64 {
		out := map[string]map[string]int64{}
		for _, g := range groups {
			tier := ""
			if g.ServiceTier != nil {
				tier = *g.ServiceTier
			}
			keyBytes, _ := json.Marshal([]string{g.Kind, g.Purpose, g.Model, tier})
			key := string(keyBytes)
			out[key] = g.Usage
		}
		return out
	}
	return reflect.DeepEqual(normalize(a), normalize(b))
}

// Wake merely advances polling. It never creates another billable request.
func (s *CodexEngineBillingService) Wake() {
	if s == nil {
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *CodexEngineBillingService) Start() {
	if s == nil || s.store == nil || s.accounts == nil || s.upstream == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started || s.stopped || s.settle == nil {
		return
	}
	s.started = true
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			s.poll(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			case <-s.wake:
			}
		}
	}()
}

func (s *CodexEngineBillingService) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.stopped = true
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *CodexEngineBillingService) poll(ctx context.Context) {
	available := cap(s.slots) - len(s.slots)
	if available <= 0 || ctx.Err() != nil {
		return
	}
	claimCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	jobs, err := s.store.ClaimDue(claimCtx, available, codexEngineBillingLease)
	cancel()
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("Engine billing claim failed")
		}
		return
	}
	for _, job := range jobs {
		if job == nil {
			continue
		}
		s.slots <- struct{}{}
		s.wg.Add(1)
		go func(job *CodexEngineBillingJob) {
			defer s.wg.Done()
			defer func() { <-s.slots }()
			s.reconcile(ctx, job)
		}(job)
	}
}

func codexEngineBillingRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 6 {
		return time.Minute
	}
	delay := time.Duration(1<<uint(attempt)) * time.Second
	if delay > time.Minute {
		return time.Minute
	}
	return delay
}

func (s *CodexEngineBillingService) currentAccount(ctx context.Context, job *CodexEngineBillingJob) (*Account, error) {
	account, err := s.accounts.GetByID(ctx, job.AccountID)
	if errors.Is(err, ErrAccountNotFound) {
		return nil, ErrCodexEngineBillingConflict
	}
	if err != nil {
		return nil, err
	}
	if CodexEngineBillingIdentity(account) == "" || CodexEngineBillingIdentity(account) != job.IdentityDigest {
		return nil, ErrCodexEngineBillingConflict
	}
	return account, nil
}

func (s *CodexEngineBillingService) saveProgress(ctx context.Context, job *CodexEngineBillingJob, state, reason string, seal json.RawMessage) {
	// A short independent write releases the lease even after network cancellation.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	nextAttempt := job.NextAttemptAt
	if !nextAttempt.After(time.Now()) {
		nextAttempt = time.Now().Add(codexEngineBillingRetryDelay(job.AttemptCount))
	}
	err := s.store.SaveProgress(writeCtx, &CodexEngineBillingProgress{JobID: job.ID, ClaimToken: job.ClaimToken,
		Cursor: job.Cursor, Revision: job.Revision, ConsumedSequence: job.ConsumedSequence, SettlementCursor: job.SettlementCursor, State: state, TerminalSeal: seal, SourceSeal: job.SourceSeal, LastError: reason,
		NextAttemptAt: nextAttempt})
	if err != nil && !errors.Is(err, ErrCodexEngineBillingClaimLost) {
		slog.Warn("Engine billing progress save failed", "job_id", job.ID, "account_id", job.AccountID)
	}
}

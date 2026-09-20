package service

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// CodexTurnStateService deliberately fails open at the forwarding boundary:
// callers retain their ordinary request if Prepare or ValidateAttempt fails.
// Only PostgreSQL records are authoritative; there is no fallback token cache.
type CodexTurnStateService struct {
	repo          CodexTurnStateRepository
	accounts      AccountRepository
	encryptor     SecretEncryptor
	collector     CodexTurnStateCollector
	modelPolicy   CodexTurnStateModelPolicy
	now           func() time.Time
	mu            sync.Mutex
	ctx           context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup
	queue         chan CodexTurnStateKey
	queued        map[CodexTurnStateKey]bool
	running       map[CodexTurnStateKey]context.CancelFunc
	runningPolicy map[CodexTurnStateKey]string
	business      map[string]*CodexTurnStateAttempt
}

func NewCodexTurnStateService(repo CodexTurnStateRepository, accounts AccountRepository, encryptor SecretEncryptor, collector CodexTurnStateCollector) *CodexTurnStateService {
	return &CodexTurnStateService{repo: repo, accounts: accounts, encryptor: encryptor, collector: collector, now: time.Now,
		queue: make(chan CodexTurnStateKey, 128), queued: make(map[CodexTurnStateKey]bool), running: make(map[CodexTurnStateKey]context.CancelFunc), runningPolicy: make(map[CodexTurnStateKey]string), business: make(map[string]*CodexTurnStateAttempt)}
}

func (s *CodexTurnStateService) Start(ctx context.Context) {
	if s == nil || s.repo == nil || s.accounts == nil || s.encryptor == nil {
		return
	}
	s.mu.Lock()
	if s.ctx != nil {
		s.mu.Unlock()
		return
	}
	s.ctx, s.cancel = context.WithCancel(ctx)
	runCtx := s.ctx
	s.wg.Add(6)
	s.mu.Unlock()
	for range 4 {
		go func() { defer s.wg.Done(); s.worker(runCtx) }()
	}
	go func() { defer s.wg.Done(); s.maintenance(runCtx) }()
	go func() {
		defer s.wg.Done()
		for runCtx.Err() == nil {
			_ = s.repo.SubscribeCancels(runCtx, s.cancelCollection)
			timer := time.NewTimer(time.Second)
			select {
			case <-runCtx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
	s.startHistoryActivation(runCtx)
}

func (s *CodexTurnStateService) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		s.wg.Wait()
	}
}

func (s *CodexTurnStateService) currentOwner(ctx context.Context, accountID int64) (*Account, error) {
	if s == nil || s.accounts == nil {
		return nil, errors.New("turn_state_unavailable")
	}
	account, err := s.accounts.GetByID(ctx, accountID)
	if err != nil || account == nil {
		return nil, err
	}
	return resolveCredentialAccount(ctx, s.accounts, account)
}

func codexTurnStateEligible(account *Account) bool {
	return account != nil && account.IsOpenAIOAuth() && !account.IsOpenAIPersonalAccessToken() && !account.IsOpenAIAgentIdentity()
}

func (s *CodexTurnStateService) Enabled(ctx context.Context, account *Account) (bool, string, error) {
	if s == nil || account == nil {
		return false, "", nil
	}
	owner, err := s.currentOwner(ctx, account.ID)
	if err != nil {
		return false, "", err
	}
	if !codexTurnStateEligible(owner) {
		return false, "", nil
	}
	return CodexTurnStateConfigForAccount(owner).Enabled, CodexTurnStateGenerationForAccount(owner), nil
}

func (s *CodexTurnStateService) Prepare(ctx context.Context, account *Account, finalModel string) (*CodexTurnStateAttempt, error) {
	if s == nil || account == nil || strings.TrimSpace(finalModel) == "" {
		return nil, nil
	}
	owner, err := s.currentOwner(ctx, account.ID)
	if err != nil {
		return nil, err
	}
	if !codexTurnStateEligible(owner) {
		return nil, nil
	}
	accountEnabled := CodexTurnStateConfigForAccount(owner).Enabled
	passive := func(reason string) *CodexTurnStateAttempt {
		return &CodexTurnStateAttempt{OwnerAccountID: owner.ID, Model: strings.TrimSpace(finalModel), AccountEnabled: accountEnabled,
			MaintenanceReason: reason, accountType: CodexTurnStateAccountTypeForAccount(owner), credentialEpoch: CodexTurnStateCredentialEpochForAccount(owner), historyService: s, preparedAt: s.now()}
	}
	// Preparing a physical request must not grant maintenance from a stale
	// cross-instance settings cache after an administrator removes a model.
	allowed, policyRevision, policyErr := false, "", error(nil)
	if accountEnabled {
		allowed, policyRevision, policyErr = s.checkModelPolicy(ctx, strings.TrimSpace(finalModel), true)
	}
	if !accountEnabled || !allowed || policyErr != nil {
		// Observation is independent of cache maintenance. This request-local
		// attempt has no lease, generation, cached token or collection identity.
		// It must never enter the runtime store, even when a response is delivered.
		reason := ""
		if accountEnabled {
			reason = "model_excluded"
			if policyErr != nil {
				reason = "model_policy_unavailable"
			}
		}
		return passive(reason), nil
	}
	if s.repo == nil || s.encryptor == nil {
		return passive("maintenance_unavailable"), nil
	}
	if !account.IsShadow() {
		for _, key := range CodexTurnStateCredentialKeys {
			if !reflect.DeepEqual(account.Credentials[key], owner.Credentials[key]) {
				return passive("physical_credentials_stale"), nil
			}
		}
	}
	generation := CodexTurnStateGenerationForAccount(owner)
	if generation == "" {
		return passive("generation_unavailable"), nil
	}
	key := CodexTurnStateKey{OwnerAccountID: owner.ID, Model: strings.TrimSpace(finalModel), Generation: generation}
	now := s.now()
	a := passive("")
	a.Generation, a.Enabled, a.key, a.id, a.policyRevision = generation, true, key, uuid.NewString(), policyRevision
	record, err := s.repo.BeginBusiness(ctx, key, a.id, now, now.Add(2*time.Minute))
	if err != nil {
		return passive("maintenance_unavailable"), nil
	}
	if record == nil {
		return passive("generation_changed"), nil
	}
	a.baseVersion = record.Version
	a.baseCacheIdentity = record.cacheIdentity()
	a.Snapshot.Version = record.Version
	if a.accountType != "" && record.EncryptedToken != "" && record.ExpiresAt.After(now) {
		token, decryptErr := s.encryptor.Decrypt(record.EncryptedToken)
		if decryptErr == nil {
			shape, shapeErr := ParseCodexTurnState(token, a.accountType, now)
			if shapeErr == nil && shape.Shape == CodexTurnStateShapeTarget {
				a.Snapshot = CodexTurnStateSnapshot{Token: token, Version: record.Version, Source: record.Source, TokenLength: shape.TokenLength, CipherBlocks: shape.CipherBlocks, ExpiresAt: shape.ExpiresAt}
			}
		}
	}
	s.mu.Lock()
	s.business[a.id] = a
	s.mu.Unlock()
	return a, nil
}

func (s *CodexTurnStateService) ValidateAttempt(ctx context.Context, a *CodexTurnStateAttempt) bool {
	if s == nil || a == nil || !a.Enabled {
		return false
	}
	allowed, revision, policyErr := s.checkModelPolicy(ctx, a.Model, true)
	if policyErr != nil || !allowed || revision != a.policyRevision {
		reason := "model_policy_changed"
		if policyErr != nil {
			reason = "model_policy_unavailable"
		} else if !allowed {
			reason = "model_excluded"
		}
		a.mu.Lock()
		a.validationReason = reason
		a.mu.Unlock()
		return false
	}
	owner, err := s.currentOwner(ctx, a.OwnerAccountID)
	if err != nil || !codexTurnStateEligible(owner) || !CodexTurnStateConfigForAccount(owner).Enabled || CodexTurnStateGenerationForAccount(owner) != a.Generation {
		return false
	}
	if a.Snapshot.Token != "" && (CodexTurnStateAccountTypeForAccount(owner) != a.accountType || !a.Snapshot.ExpiresAt.After(s.now())) {
		return false
	}
	// Also check runtime availability immediately before injection. This prevents
	// sending an earlier snapshot after storage becomes unavailable.
	record, err := s.repo.Get(ctx, a.key)
	return err == nil && record != nil && (a.Snapshot.Token == "" || record.Version == a.Snapshot.Version || a.baseCacheIdentity.matches(record))
}

// ValidateCredentialHeaders binds learning and injection to the credentials
// frozen on this physical request/socket. Shadow accounts have no credentials
// on their routing Account, so transports must call this before their send.
func (s *CodexTurnStateService) ValidateCredentialHeaders(ctx context.Context, a *CodexTurnStateAttempt, headers http.Header) bool {
	if s == nil || a == nil {
		return false
	}
	owner, err := s.currentOwner(ctx, a.OwnerAccountID)
	if err != nil || owner == nil || CodexTurnStateGenerationForAccount(owner) != a.Generation {
		return false
	}
	header := func(name string) string {
		for key, values := range headers {
			if strings.EqualFold(key, name) && len(values) > 0 {
				return strings.TrimSpace(values[0])
			}
		}
		return ""
	}
	accessToken := strings.TrimSpace(owner.GetCredential("access_token"))
	if accessToken == "" || header("Authorization") != "Bearer "+accessToken {
		return false
	}
	if header("ChatGPT-Account-Id") != strings.TrimSpace(owner.GetCredential("chatgpt_account_id")) {
		return false
	}
	return s.ValidateAttempt(ctx, a)
}

func (s *CodexTurnStateService) Observe(a *CodexTurnStateAttempt, token string) {
	s.observe(a, token, "")
}

func (s *CodexTurnStateService) observe(a *CodexTurnStateAttempt, token, source string) {
	if a == nil || token == "" || len(token) > 4096 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.finished {
		return
	}
	now := s.now()
	envelope, _ := InspectCodexTurnStateEnvelope(token, now)
	shape, err := ParseCodexTurnState(token, a.accountType, now)
	a.safeObservation = CodexTurnStateSafeObservation{ObservedAt: time.Now(), TokenLength: len(token), CipherBlocks: envelope.CipherBlocks, Shape: shape.Shape, ResponseSource: source,
		ObservedShape: envelope.ObservedShape, ValidationReason: envelope.ValidationReason}
	if err != nil {
		if a.safeObservation.ValidationReason == "" {
			a.safeObservation.ValidationReason = err.Error()
		}
		a.safeObservation.RefreshReason = "invalid_state"
		if err.Error() == "expired" {
			a.safeObservation.Shape = "expired"
			a.safeObservation.RefreshReason = "expired_state"
		}
	} else if shape.Shape == CodexTurnStateShapeExtended {
		a.safeObservation.RefreshReason = "extended_shape"
	}
	s.observeHistoryEnvelopeLocked(a, token, now)
	if !a.Enabled {
		// Passive observations retain only the safe summary, never token values.
		a.safeObservation.RefreshReason = ""
		return
	}
	for _, existing := range a.candidates {
		if existing == token {
			return
		}
	}
	if len(a.candidates) == 16 {
		copy(a.candidates, a.candidates[1:])
		a.candidates = a.candidates[:15]
	}
	a.candidates = append(a.candidates, token)
}

func (s *CodexTurnStateService) ObserveHeaders(a *CodexTurnStateAttempt, headers http.Header) {
	for key, values := range headers {
		if strings.EqualFold(key, "x-codex-turn-state") {
			for _, token := range values {
				s.observe(a, token, "header")
			}
		}
	}
}

func (s *CodexTurnStateService) ObserveEvent(a *CodexTurnStateAttempt, event []byte) {
	for _, token := range CodexTurnStateTokensFromEvent(event) {
		s.observe(a, token, "metadata")
	}
}

func (s *CodexTurnStateService) Finish(ctx context.Context, a *CodexTurnStateAttempt, delivered bool) error {
	if s == nil || a == nil {
		return nil
	}
	a.mu.Lock()
	if a.finished {
		a.mu.Unlock()
		return nil
	}
	a.finished = true
	a.historyDelivered = delivered
	tokens := append([]string(nil), a.candidates...)
	businessSentAt := a.businessSentAt
	observedAt := a.safeObservation.ObservedAt
	a.candidates = nil
	a.mu.Unlock()
	s.recordDeliveredHistory(a, delivered)
	if !a.Enabled {
		return nil
	}
	s.mu.Lock()
	delete(s.business, a.id)
	s.mu.Unlock()
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	var publishErr error
	if !businessSentAt.IsZero() {
		publishErr = s.repo.MarkBusinessSent(cleanupCtx, a.key, businessSentAt)
	}
	if delivered && publishErr == nil {
		demandAt := time.Time{}
		if !businessSentAt.IsZero() {
			demandAt = observedAt
		}
		_, publishErr = s.publish(cleanupCtx, a.key, tokens, "business", a.baseVersion, false, a.policyRevision, demandAt, a.baseCacheIdentity)
	}
	endErr := s.repo.EndBusiness(cleanupCtx, a.key, a.id)
	if delivered && businessSentAt.IsZero() && endErr == nil {
		s.completeBusinessSent(a)
	}
	if delivered && publishErr == nil && endErr == nil && s.modelPolicyMatches(cleanupCtx, a.Model, a.policyRevision) {
		if record, readErr := s.repo.Get(cleanupCtx, a.key); readErr == nil && record != nil {
			reason := ""
			reason = record.DemandReason
			a.mu.Lock()
			a.safeObservation.RefreshReason = reason
			a.mu.Unlock()
			if reason != "" {
				s.enqueue(cleanupCtx, a.key)
			}
		}
	}
	return errors.Join(publishErr, endErr)
}

// publish returns true only when a target was committed or already present.
// Natural target responses must respect issuance ordering. An anomalous response
// may revoke only the cache it observed, even if scheduling changed its version.
func (s *CodexTurnStateService) publish(ctx context.Context, key CodexTurnStateKey, tokens []string, source string, expected int64, strict bool, policyRevision string, demandAt time.Time, baseCacheIdentity codexTurnStateCacheIdentity) (bool, error) {
	if !s.modelPolicyMatches(ctx, key.Model, policyRevision) {
		return false, nil
	}
	owner, err := s.currentOwner(ctx, key.OwnerAccountID)
	if err != nil {
		return false, err
	}
	if !codexTurnStateEligible(owner) || !CodexTurnStateConfigForAccount(owner).Enabled || CodexTurnStateGenerationForAccount(owner) != key.Generation {
		return false, nil
	}
	accountType := CodexTurnStateAccountTypeForAccount(owner)
	if accountType == "" {
		return false, nil
	}
	var best string
	var bestShape CodexTurnStateShape
	extended := false
	var extendedShape CodexTurnStateShape
	for _, token := range tokens {
		shape, parseErr := ParseCodexTurnState(token, accountType, s.now())
		if parseErr != nil {
			continue
		}
		if shape.Shape == CodexTurnStateShapeExtended {
			extended = true
			if extendedShape.IssuedAt.IsZero() || shape.IssuedAt.After(extendedShape.IssuedAt) {
				extendedShape = shape
			}
			continue
		}
		if best == "" || shape.IssuedAt.After(bestShape.IssuedAt) {
			best = token
			bestShape = shape
		}
	}
	for range 3 {
		if !s.authoritativeModelPolicyMatches(ctx, key.Model, policyRevision) {
			return false, nil
		}
		record, getErr := s.repo.Get(ctx, key)
		if getErr != nil || record == nil {
			return false, getErr
		}
		if strict && record.Version != expected {
			return false, nil
		}
		// A concurrent collector may only have reserved work or updated backoff.
		// Those writes must not revoke a business response's cache identity; a
		// genuinely changed token still protects against late invalidation.
		cacheUnchanged := record.Version == expected || baseCacheIdentity.matches(record)
		if best != "" {
			if bestShape.IssuedAt.Before(record.IssuedAt) || (bestShape.IssuedAt.Equal(record.IssuedAt) && record.EncryptedToken == "" && !cacheUnchanged) {
				return false, nil
			}
			if !bestShape.IssuedAt.After(record.IssuedAt) && record.EncryptedToken != "" {
				if record.ExpiresAt.After(s.now()) {
					// Reobserving a still-expiring token cannot satisfy renewal or
					// cancel the independent request already trying to replace it.
					if record.ExpiresAt.After(s.now().Add(CodexTurnStateRefreshAhead)) {
						s.cancelAndNotify(ctx, key)
					}
					return true, nil
				}
				return false, nil
			}
			encrypted, encryptErr := s.encryptor.Encrypt(best)
			if encryptErr != nil {
				return false, encryptErr
			}
			record.EncryptedToken = encrypted
			record.IssuedAt = bestShape.IssuedAt
			record.ExpiresAt = bestShape.ExpiresAt
			record.TokenLength = bestShape.TokenLength
			record.CipherBlocks = bestShape.CipherBlocks
			record.Source = source
			record.Shape = bestShape.Shape
			now := s.now()
			if bestShape.ExpiresAt.After(now.Add(CodexTurnStateRefreshAhead)) {
				record.RefreshReason = ""
				completeCodexTurnStateDemand(record, now)
			} else {
				// A newer natural token can still be due for renewal. Preserve
				// the existing retry fence when business preempts a collector.
				record.DemandReason, record.RefreshReason = "expiring", "expiring"
				if record.DemandAt.IsZero() {
					record.DemandAt = now
				}
				switch {
				case record.CollectorPaused:
					record.CollectionStatus, record.CollectionReason = "paused", record.LastError
				case record.NextCollectAt.After(now):
					record.CollectionStatus, record.CollectionReason = "backoff", record.LastError
					if record.CollectionReason == "" {
						record.CollectionReason = "target_still_expiring"
					}
				default:
					record.CollectionStatus, record.CollectionReason = "pending", "queued"
				}
			}
			if source == "collector" {
				record.LastCollectedAt = s.now()
			}
			if !s.authoritativeModelPolicyMatches(ctx, key.Model, policyRevision) {
				return false, nil
			}
			record.ModelPolicyRevision = policyRevision
			ok, saveErr := s.repo.SaveCAS(ctx, *record, record.Version)
			if saveErr != nil {
				return false, saveErr
			}
			if ok {
				if bestShape.ExpiresAt.After(s.now().Add(CodexTurnStateRefreshAhead)) {
					s.cancelAndNotify(ctx, key)
				}
				return true, nil
			}
			continue
		}
		if extended && cacheUnchanged && !demandAt.IsZero() {
			record.EncryptedToken = ""
			record.ExpiresAt = time.Time{}
			record.Shape = CodexTurnStateShapeExtended
			record.TokenLength = extendedShape.TokenLength
			record.CipherBlocks = extendedShape.CipherBlocks
			if extendedShape.IssuedAt.After(record.IssuedAt) {
				record.IssuedAt = extendedShape.IssuedAt
			}
			record.RefreshReason = "extended_shape"
			record.DemandReason, record.DemandAt = "extended_shape", demandAt
			if record.CollectionStatus != "collecting" || !record.LastCollectedAt.Add(CodexTurnStateCollectTimeout).After(s.now()) {
				record.CollectionStatus, record.CollectionReason = "pending", "queued"
			}
			if source == "collector" {
				record.LastCollectedAt = s.now()
			}
			if !s.authoritativeModelPolicyMatches(ctx, key.Model, policyRevision) {
				return false, nil
			}
			record.ModelPolicyRevision = policyRevision
			ok, saveErr := s.repo.SaveCAS(ctx, *record, record.Version)
			if saveErr != nil || ok {
				return false, saveErr
			}
			// A concurrent scheduling write may have advanced only the CAS
			// version. Reload and recheck cache identity before invalidating.
			continue
		}
		return false, nil
	}
	return false, nil
}

func (s *CodexTurnStateService) cancelCollection(key CodexTurnStateKey) {
	s.mu.Lock()
	cancel := s.running[key]
	delete(s.queued, key)
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *CodexTurnStateService) cancelAndNotify(ctx context.Context, key CodexTurnStateKey) {
	s.cancelCollection(key)
	_ = s.repo.PublishCancel(ctx, key)
}

func (s *CodexTurnStateService) enqueue(ctx context.Context, key CodexTurnStateKey) {
	allowed, _, err := s.modelAllowed(ctx, key.Model)
	if s.collector == nil || err != nil || !allowed {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx == nil || s.ctx.Err() != nil || s.queued[key] || s.running[key] != nil {
		return
	}
	s.queued[key] = true
	select {
	case s.queue <- key:
	default:
		delete(s.queued, key)
	}
}

func (s *CodexTurnStateService) maintenance(ctx context.Context) {
	ticker := time.NewTicker(CodexTurnStateScanInterval)
	defer ticker.Stop()
	due := time.NewTicker(CodexTurnStateDueInterval)
	defer due.Stop()
	s.scan(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.scan(ctx)
		case <-due.C:
			s.pumpDue(ctx)
		}
	}
}

func (s *CodexTurnStateService) scan(ctx context.Context) {
	s.CancelExcludedModels(ctx)
	s.mu.Lock()
	active := make([]*CodexTurnStateAttempt, 0, len(s.business))
	for _, a := range s.business {
		active = append(active, a)
	}
	s.mu.Unlock()
	for _, a := range active {
		a.mu.Lock()
		if !a.finished {
			owner, err := s.currentOwner(ctx, a.OwnerAccountID)
			if err == nil && owner != nil && CodexTurnStateConfigForAccount(owner).Enabled && CodexTurnStateGenerationForAccount(owner) == a.Generation && s.modelPolicyMatches(ctx, a.Model, a.policyRevision) {
				now := s.now()
				if !a.businessSentAt.IsZero() {
					_ = s.repo.MarkBusinessSent(ctx, a.key, a.businessSentAt)
				}
				_, _ = s.repo.BeginBusiness(ctx, a.key, a.id, now, now.Add(2*time.Minute))
			} else {
				s.cancelCollection(a.key)
			}
		}
		a.mu.Unlock()
	}
	s.mu.Lock()
	running := make([]CodexTurnStateKey, 0, len(s.running))
	for key := range s.running {
		running = append(running, key)
	}
	s.mu.Unlock()
	for _, key := range running {
		owner, err := s.currentOwner(ctx, key.OwnerAccountID)
		if err != nil || owner == nil || !CodexTurnStateConfigForAccount(owner).Enabled || CodexTurnStateGenerationForAccount(owner) != key.Generation {
			s.cancelCollection(key)
		}
	}
	s.scanHistory(ctx)
	s.pumpDue(ctx)
}

func (s *CodexTurnStateService) pumpDue(ctx context.Context) {
	if s == nil || s.repo == nil {
		return
	}
	records, err := s.repo.ListActive(ctx, s.now().Add(-CodexTurnStateActiveWindow), 512)
	if err != nil {
		return
	}
	for _, record := range records {
		if s.ensureCodexTurnStateDemand(ctx, &record) {
			s.enqueue(ctx, record.Key())
		}
	}
}

// An empty cache never creates demand. The only time-based demand starts from
// a previously accepted token reaching its renewal window.
func (s *CodexTurnStateService) ensureCodexTurnStateDemand(ctx context.Context, record *CodexTurnStateRecord) bool {
	if record == nil || record.LastBusinessAt.Before(s.now().Add(-CodexTurnStateActiveWindow)) {
		return false
	}
	if codexTurnStateWaitsForProxyCacheExpiry(record, s.now()) {
		return false
	}
	if record.DemandReason != "" {
		return true
	}
	if record.EncryptedToken == "" || record.ExpiresAt.After(s.now().Add(CodexTurnStateRefreshAhead)) {
		return false
	}
	allowed, revision, err := s.checkModelPolicy(ctx, record.Model, true)
	if err != nil || !allowed {
		return false
	}
	record.DemandReason, record.RefreshReason, record.DemandAt = "expiring", "expiring", s.now()
	record.CollectionStatus, record.CollectionReason = "pending", "queued"
	record.ModelPolicyRevision = revision
	ok, err := s.repo.SaveCAS(ctx, *record, record.Version)
	if ok {
		record.Version++
	}
	return err == nil && ok
}

func (s *CodexTurnStateService) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case key := <-s.queue:
			s.mu.Lock()
			pending := s.queued[key]
			delete(s.queued, key)
			s.mu.Unlock()
			if pending {
				s.collect(ctx, key)
			}
		}
	}
}

func (s *CodexTurnStateService) collect(ctx context.Context, key CodexTurnStateKey) {
	allowed, policyRevision, policyErr := s.checkModelPolicy(ctx, key.Model, true)
	if s.collector == nil || policyErr != nil || !allowed {
		return
	}
	// Include database lookups in the deadline; otherwise a delayed lookup can
	// leave a 20s network probe alive after the 30s distributed lease expires.
	ctx, cancelAttempt := context.WithTimeout(ctx, CodexTurnStateCollectTimeout)
	defer cancelAttempt()
	lockID := uuid.NewString()
	locked, err := s.repo.AcquireCollector(ctx, key.OwnerAccountID, lockID, CodexTurnStateCollectTimeout+10*time.Second)
	if err != nil || !locked {
		return
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_ = s.repo.ReleaseCollector(releaseCtx, key.OwnerAccountID, lockID)
	}()
	owner, err := s.currentOwner(ctx, key.OwnerAccountID)
	if err != nil || !codexTurnStateEligible(owner) || !CodexTurnStateConfigForAccount(owner).Enabled || CodexTurnStateGenerationForAccount(owner) != key.Generation || CodexTurnStateAccountTypeForAccount(owner) == "" {
		return
	}
	if owner.Status != StatusActive || !owner.Schedulable || (owner.ExpiresAt != nil && !owner.ExpiresAt.After(s.now())) {
		return
	}
	cfg := CodexTurnStateConfigForAccount(owner)
	if cfg.CollectorProxyID == nil || *cfg.CollectorProxyID <= 0 {
		return
	}
	record, err := s.repo.Get(ctx, key)
	if err != nil || record == nil {
		return
	}
	now := s.now()
	if record.LastBusinessAt.Before(now.Add(-CodexTurnStateActiveWindow)) {
		if record.DemandReason != "" && s.authoritativeModelPolicyMatches(ctx, key.Model, policyRevision) {
			clearIdleCodexTurnStateDemand(record)
			record.ModelPolicyRevision = policyRevision
			_, _ = s.repo.SaveCAS(ctx, *record, record.Version)
		}
		return
	}
	if record.CollectorPaused || record.NextCollectAt.After(now) || !s.ensureCodexTurnStateDemand(ctx, record) {
		return
	}
	var cooldownUntil time.Time
	for _, until := range []*time.Time{owner.RateLimitResetAt, owner.OverloadUntil, owner.TempUnschedulableUntil} {
		if until != nil && until.After(now) && until.After(cooldownUntil) {
			cooldownUntil = *until
		}
	}
	if !cooldownUntil.IsZero() {
		record.NextCollectAt = cooldownUntil
		record.LastError = "account_cooldown"
		record.CollectionStatus, record.CollectionReason = "backoff", "account_cooldown"
		if s.authoritativeModelPolicyMatches(ctx, key.Model, policyRevision) {
			record.ModelPolicyRevision = policyRevision
			_, _ = s.repo.SaveCAS(ctx, *record, record.Version)
		}
		return
	}
	// Authentication failures pause the credential owner, not only one model.
	all, err := s.repo.ListByAccount(ctx, key.OwnerAccountID)
	if err != nil {
		return
	}
	for _, other := range all {
		if other.Generation == key.Generation && other.CollectorPaused {
			return
		}
		if other.Generation == key.Generation && !other.LastCollectedAt.IsZero() && other.NextCollectAt.After(now) {
			// Failure pacing (including upstream Retry-After) belongs to the
			// credential owner even when another model is next in the queue.
			return
		}
	}
	probeCtx, cancel := context.WithTimeout(ctx, CodexTurnStateCollectTimeout)
	s.mu.Lock()
	s.running[key] = cancel
	s.runningPolicy[key] = policyRevision
	s.mu.Unlock()
	defer func() { cancel(); s.mu.Lock(); delete(s.running, key); delete(s.runningPolicy, key); s.mu.Unlock() }()
	if probeCtx.Err() != nil || !s.modelPolicyMatches(probeCtx, key.Model, policyRevision) {
		return
	}
	// Config writes are independent of this service; poll the authoritative owner
	// while a probe is alive so disable/credential replacement cancels promptly
	// even if its Redis notification was unavailable.
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-probeCtx.Done():
				return
			case <-ticker.C:
				current, checkErr := s.currentOwner(probeCtx, key.OwnerAccountID)
				if checkErr != nil || current == nil || current.Status != StatusActive || !current.Schedulable ||
					(current.ExpiresAt != nil && !current.ExpiresAt.After(s.now())) ||
					!CodexTurnStateConfigForAccount(current).Enabled || CodexTurnStateGenerationForAccount(current) != key.Generation || !s.modelPolicyMatches(probeCtx, key.Model, policyRevision) {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-watchDone }()
	// Persist pacing before network I/O: process death must not immediately retry.
	// PostgreSQL timestamps retain microseconds. Keep the local reservation at
	// the same precision so completion can distinguish it from a new cooldown.
	record.NextCollectAt = now.Add(CodexTurnStateCollectTimeout + CodexTurnStateRetryInterval).UTC().Truncate(time.Microsecond)
	record.LastCollectedAt = now
	record.RefreshReason = record.DemandReason
	record.CollectionStatus, record.CollectionReason = "collecting", "collecting"
	if !s.authoritativeModelPolicyMatches(probeCtx, key.Model, policyRevision) {
		return
	}
	record.ModelPolicyRevision = policyRevision
	if ok, saveErr := s.repo.SaveCAS(probeCtx, *record, record.Version); saveErr != nil || !ok {
		return
	}
	if probeCtx.Err() != nil || !s.authoritativeModelPolicyMatches(probeCtx, key.Model, policyRevision) {
		return
	}
	result, collectErr := s.collector.Collect(probeCtx, CodexTurnStateCollectRequest{Account: owner, Model: key.Model, ProxyID: *cfg.CollectorProxyID,
		validateModelPolicy: func(sendCtx context.Context) bool {
			return s.authoritativeModelPolicyMatches(sendCtx, key.Model, policyRevision)
		}})
	s.recordCollectorObservation(owner, key.Model, result)
	if probeCtx.Err() != nil && !errors.Is(probeCtx.Err(), context.DeadlineExceeded) {
		return
	}
	if errors.Is(probeCtx.Err(), context.DeadlineExceeded) {
		collectErr = context.DeadlineExceeded
	}
	s.finishCollectorOutcome(ctx, owner, key, *record, policyRevision, result, collectErr)
}

func (s *CodexTurnStateService) GetStatus(ctx context.Context, accountID int64) (*CodexTurnStateStatus, error) {
	owner, err := s.currentOwner(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if owner == nil {
		return nil, errors.New("account_not_found")
	}
	var records []CodexTurnStateRecord
	if s.repo != nil {
		records, err = s.repo.ListByAccount(ctx, owner.ID)
		if err != nil {
			return nil, err
		}
	}
	models, policyErr := s.statusModelPolicy(ctx)
	result := projectCodexTurnStateStatus(accountID, owner, records, models, policyErr, s.statusNow())
	observationEnabled, observations := globalCodexTurnStateSummaryStore.snapshot([]int64{owner.ID})
	attachCodexTurnStateObservations(result, observationEnabled, observations[owner.ID])
	return result, nil
}

func codexStateTimePtr(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

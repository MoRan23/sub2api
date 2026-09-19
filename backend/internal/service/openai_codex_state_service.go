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
	repo      CodexTurnStateRepository
	accounts  AccountRepository
	encryptor SecretEncryptor
	collector CodexTurnStateCollector
	now       func() time.Time
	mu        sync.Mutex
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	queue     chan CodexTurnStateKey
	queued    map[CodexTurnStateKey]bool
	running   map[CodexTurnStateKey]context.CancelFunc
	business  map[string]*CodexTurnStateAttempt
}

func NewCodexTurnStateService(repo CodexTurnStateRepository, accounts AccountRepository, encryptor SecretEncryptor, collector CodexTurnStateCollector) *CodexTurnStateService {
	return &CodexTurnStateService{repo: repo, accounts: accounts, encryptor: encryptor, collector: collector, now: time.Now,
		queue: make(chan CodexTurnStateKey, 128), queued: make(map[CodexTurnStateKey]bool), running: make(map[CodexTurnStateKey]context.CancelFunc), business: make(map[string]*CodexTurnStateAttempt)}
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
	if s == nil || account == nil || s.repo == nil || s.encryptor == nil || strings.TrimSpace(finalModel) == "" {
		return nil, nil
	}
	owner, err := s.currentOwner(ctx, account.ID)
	if err != nil {
		return nil, err
	}
	if !codexTurnStateEligible(owner) || !CodexTurnStateConfigForAccount(owner).Enabled {
		return nil, nil
	}
	if !account.IsShadow() {
		for _, key := range CodexTurnStateCredentialKeys {
			if !reflect.DeepEqual(account.Credentials[key], owner.Credentials[key]) {
				return nil, errors.New("turn_state_physical_credentials_stale")
			}
		}
	}
	generation := CodexTurnStateGenerationForAccount(owner)
	if generation == "" {
		return nil, errors.New("turn_state_generation_unavailable")
	}
	key := CodexTurnStateKey{OwnerAccountID: owner.ID, Model: strings.TrimSpace(finalModel), Generation: generation}
	now := s.now()
	a := &CodexTurnStateAttempt{OwnerAccountID: owner.ID, Model: key.Model, Generation: generation, Enabled: true, key: key, id: uuid.NewString(), accountType: CodexTurnStateAccountTypeForAccount(owner)}
	record, err := s.repo.BeginBusiness(ctx, key, a.id, now, now.Add(2*time.Minute))
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, errors.New("turn_state_generation_changed")
	}
	a.baseVersion = record.Version
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
	if a.Snapshot.Token == "" {
		// A newly arrived normal request takes priority over a cold collection.
		s.cancelAndNotify(ctx, key)
	}
	return a, nil
}

func (s *CodexTurnStateService) ValidateAttempt(ctx context.Context, a *CodexTurnStateAttempt) bool {
	if s == nil || a == nil {
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
	return err == nil && record != nil && (a.Snapshot.Token == "" || record.Version == a.Snapshot.Version)
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
	if a == nil || token == "" || len(token) > 4096 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.finished {
		return
	}
	shape, err := ParseCodexTurnState(token, a.accountType, s.now())
	a.safeObservation = CodexTurnStateSafeObservation{TokenLength: len(token), CipherBlocks: shape.CipherBlocks, Shape: shape.Shape}
	if err != nil {
		a.safeObservation.RefreshReason = "invalid_state"
		if err.Error() == "expired" {
			a.safeObservation.Shape = "expired"
			a.safeObservation.RefreshReason = "expired_state"
		}
	} else if shape.Shape == CodexTurnStateShapeExtended {
		a.safeObservation.RefreshReason = "extended_shape"
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
				s.Observe(a, token)
			}
		}
	}
}

func (s *CodexTurnStateService) ObserveEvent(a *CodexTurnStateAttempt, event []byte) {
	for _, token := range CodexTurnStateTokensFromEvent(event) {
		s.Observe(a, token)
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
	tokens := append([]string(nil), a.candidates...)
	a.candidates = nil
	a.mu.Unlock()
	s.mu.Lock()
	delete(s.business, a.id)
	s.mu.Unlock()
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	var publishErr error
	if delivered {
		_, publishErr = s.publish(cleanupCtx, a.key, tokens, "business", a.baseVersion, false)
	}
	endErr := s.repo.EndBusiness(cleanupCtx, a.key, a.id)
	if delivered && publishErr == nil && endErr == nil {
		if record, readErr := s.repo.Get(cleanupCtx, a.key); readErr == nil && record != nil {
			reason := ""
			if record.EncryptedToken == "" {
				reason = record.RefreshReason
				if reason == "" {
					reason = "missing"
				}
			} else if !record.ExpiresAt.After(s.now()) {
				reason = "expired"
			} else if !record.ExpiresAt.After(s.now().Add(CodexTurnStateRefreshAhead)) {
				reason = "expiring"
			}
			a.mu.Lock()
			a.safeObservation.RefreshReason = reason
			a.mu.Unlock()
		}
		s.enqueue(a.key)
	}
	return errors.Join(publishErr, endErr)
}

// publish returns true only when a target was committed or already present.
// Natural responses may replace a concurrent record only with a newer signed
// timestamp; collector results require the exact version they started with.
func (s *CodexTurnStateService) publish(ctx context.Context, key CodexTurnStateKey, tokens []string, source string, expected int64, strict bool) (bool, error) {
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
		record, getErr := s.repo.Get(ctx, key)
		if getErr != nil || record == nil {
			return false, getErr
		}
		if strict && record.Version != expected {
			return false, nil
		}
		if best != "" {
			if bestShape.IssuedAt.Before(record.IssuedAt) || (bestShape.IssuedAt.Equal(record.IssuedAt) && record.EncryptedToken == "" && record.Version != expected) {
				return false, nil
			}
			if !bestShape.IssuedAt.After(record.IssuedAt) && record.EncryptedToken != "" {
				if record.ExpiresAt.After(s.now()) {
					s.cancelAndNotify(ctx, key)
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
			record.RefreshReason = ""
			record.LastError = ""
			record.NextCollectAt = time.Time{}
			if source == "collector" {
				record.LastCollectedAt = s.now()
			}
			ok, saveErr := s.repo.SaveCAS(ctx, *record, record.Version)
			if saveErr != nil {
				return false, saveErr
			}
			if ok {
				s.cancelAndNotify(ctx, key)
				return true, nil
			}
			continue
		}
		if extended && record.Version == expected {
			record.EncryptedToken = ""
			record.ExpiresAt = time.Time{}
			record.Shape = CodexTurnStateShapeExtended
			record.TokenLength = extendedShape.TokenLength
			record.CipherBlocks = extendedShape.CipherBlocks
			if extendedShape.IssuedAt.After(record.IssuedAt) {
				record.IssuedAt = extendedShape.IssuedAt
			}
			record.RefreshReason = "extended_shape"
			if source == "collector" {
				record.LastCollectedAt = s.now()
			}
			_, saveErr := s.repo.SaveCAS(ctx, *record, record.Version)
			return false, saveErr
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

func (s *CodexTurnStateService) enqueue(key CodexTurnStateKey) {
	if s.collector == nil {
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
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		s.scan(ctx)
	}
}

func (s *CodexTurnStateService) scan(ctx context.Context) {
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
			if err == nil && owner != nil && CodexTurnStateConfigForAccount(owner).Enabled && CodexTurnStateGenerationForAccount(owner) == a.Generation {
				now := s.now()
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
	records, err := s.repo.ListActive(ctx, s.now().Add(-CodexTurnStateActiveWindow), 512)
	if err != nil {
		return
	}
	for _, record := range records {
		s.enqueue(record.Key())
	}
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
	if s.collector == nil {
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
	if record.LastBusinessAt.Before(now.Add(-CodexTurnStateActiveWindow)) || record.CollectorPaused || record.NextCollectAt.After(now) || (record.EncryptedToken != "" && record.ExpiresAt.After(now.Add(CodexTurnStateRefreshAhead))) {
		return
	}
	for _, until := range []*time.Time{owner.RateLimitResetAt, owner.OverloadUntil, owner.TempUnschedulableUntil} {
		if until != nil && until.After(now) {
			record.NextCollectAt = *until
			record.LastError = "account_cooldown"
			_, _ = s.repo.SaveCAS(ctx, *record, record.Version)
			return
		}
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
	inflight, err := s.repo.HasBusiness(ctx, key, now)
	if err != nil || inflight {
		return
	}
	probeCtx, cancel := context.WithTimeout(ctx, CodexTurnStateCollectTimeout)
	s.mu.Lock()
	s.running[key] = cancel
	s.mu.Unlock()
	defer func() { cancel(); s.mu.Lock(); delete(s.running, key); s.mu.Unlock() }()
	// Register cancellation before the final lease check. A business request
	// arriving between the first check and registration must not lose its cancel.
	inflight, err = s.repo.HasBusiness(probeCtx, key, s.now())
	if err != nil || inflight || probeCtx.Err() != nil {
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
					!CodexTurnStateConfigForAccount(current).Enabled || CodexTurnStateGenerationForAccount(current) != key.Generation {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-watchDone }()
	// Persist pacing before network I/O: process death must not immediately retry.
	record.NextCollectAt = now.Add(CodexTurnStateRetryInterval)
	record.LastCollectedAt = now
	if record.RefreshReason == "" {
		if record.EncryptedToken == "" {
			record.RefreshReason = "missing"
		} else {
			record.RefreshReason = "expiring"
		}
	}
	if ok, saveErr := s.repo.SaveCAS(probeCtx, *record, record.Version); saveErr != nil || !ok {
		return
	}
	expected := record.Version + 1
	if probeCtx.Err() != nil {
		return
	}
	result, collectErr := s.collector.Collect(probeCtx, CodexTurnStateCollectRequest{Account: owner, Model: key.Model, ProxyID: *cfg.CollectorProxyID})
	if probeCtx.Err() != nil && !errors.Is(probeCtx.Err(), context.DeadlineExceeded) {
		return
	}
	if errors.Is(probeCtx.Err(), context.DeadlineExceeded) {
		collectErr = context.DeadlineExceeded
	}
	publishCtx, publishCancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer publishCancel()
	if collectErr == nil && result.StatusCode >= 200 && result.StatusCode < 300 {
		accepted, publishErr := s.publish(publishCtx, key, result.Tokens, "collector", expected, true)
		if accepted || publishErr != nil {
			return
		}
	}
	latest, getErr := s.repo.Get(publishCtx, key)
	if getErr != nil || latest == nil || latest.Version != expected {
		return
	}
	latest.LastError = "no_target_state"
	if collectErr != nil {
		latest.LastError = "collection_failed"
	}
	if errors.Is(collectErr, ErrCodexTurnStateCollectorProxyUnavailable) {
		latest.LastError = "collector_proxy_unavailable"
	}
	if errors.Is(probeCtx.Err(), context.DeadlineExceeded) {
		latest.LastError = "collection_timeout"
	}
	switch result.StatusCode {
	case 401, 403:
		latest.CollectorPaused = true
		latest.LastError = "collector_auth_rejected"
	case 429:
		latest.LastError = "collector_rate_limited"
	}
	retry := CodexTurnStateRetryInterval
	if result.RetryAfter > retry {
		retry = result.RetryAfter
	}
	latest.NextCollectAt = s.now().Add(retry)
	for _, until := range []*time.Time{owner.RateLimitResetAt, owner.OverloadUntil, owner.TempUnschedulableUntil} {
		if until != nil && until.After(latest.NextCollectAt) {
			latest.NextCollectAt = *until
		}
	}
	_, _ = s.repo.SaveCAS(publishCtx, *latest, expected)
}

func (s *CodexTurnStateService) GetStatus(ctx context.Context, accountID int64) (*CodexTurnStateStatus, error) {
	owner, err := s.currentOwner(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if owner == nil {
		return nil, errors.New("account_not_found")
	}
	cfg := CodexTurnStateConfigForAccount(owner)
	result := &CodexTurnStateStatus{AccountID: accountID, OwnerAccountID: owner.ID, Inherited: owner.ID != accountID, Enabled: cfg.Enabled && codexTurnStateEligible(owner), AccountType: cfg.AccountType, ResolvedAccountType: CodexTurnStateAccountTypeForAccount(owner), CollectorProxyID: cfg.CollectorProxyID, Models: []CodexTurnStateModelStatus{}}
	if result.ResolvedAccountType == "personal" {
		result.ExpectedLength = 292
	} else if result.ResolvedAccountType == "team_business" {
		result.ExpectedLength = 332
	}
	if !result.Enabled {
		result.Reason = "disabled"
	} else if result.ExpectedLength == 0 {
		result.Reason = "account_type_unknown"
	} else if cfg.CollectorProxyID == nil {
		result.Reason = "business_learning_only"
	}
	if s.repo == nil {
		return result, nil
	}
	records, err := s.repo.ListByAccount(ctx, owner.ID)
	if err != nil {
		return nil, err
	}
	now := s.now()
	for _, record := range records {
		if record.Generation != CodexTurnStateGenerationForAccount(owner) {
			continue
		}
		item := CodexTurnStateModelStatus{Model: record.Model, State: "missing", Shape: record.Shape, Source: record.Source, TokenLength: record.TokenLength, CipherBlocks: record.CipherBlocks, CollectorPaused: record.CollectorPaused, LastError: record.LastError, RefreshReason: record.RefreshReason}
		if record.EncryptedToken != "" {
			item.State = "expired"
			if record.ExpiresAt.After(now) {
				item.State = "ready"
				item.RemainingSeconds = int64(record.ExpiresAt.Sub(now) / time.Second)
			}
		}
		if record.CollectorPaused {
			item.State = "paused"
		}
		item.ExpiresAt = codexStateTimePtr(record.ExpiresAt)
		item.LastBusinessAt = codexStateTimePtr(record.LastBusinessAt)
		item.LastCollectedAt = codexStateTimePtr(record.LastCollectedAt)
		item.NextCollectAt = codexStateTimePtr(record.NextCollectAt)
		result.Models = append(result.Models, item)
	}
	return result, nil
}

func codexStateTimePtr(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

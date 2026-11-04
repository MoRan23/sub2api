package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

const (
	CodexEngineCapacitySyncExtraKey = "codex_engine_capacity_sync"
	codexEngineCapacitySyncInterval = time.Minute
	codexEngineCapacitySyncTimeout  = 15 * time.Second
	codexEngineCapacityPageSize     = 100
	codexEngineCapacityMaxBody      = 16 * 1024
	codexEngineCapacityParallelism  = 8
)

// CodexEngineCapacityRepository keeps the sync write separate from admin edits.
// Implementations recheck the endpoint, credential and scheduling revision under
// the account row lock before applying a response.
type CodexEngineCapacityRepository interface {
	ListCodexEngineCapacityCandidates(context.Context, int64, int) ([]Account, error)
	ApplyCodexEngineCapacity(context.Context, *Account, int) (bool, error)
}

// CodexEngineCapacityIdentity is a non-secret binding for persisted pause
// ownership. It deliberately excludes the revision changed by our own pause.
func CodexEngineCapacityIdentity(account *Account) string {
	if account == nil {
		return ""
	}
	var proxyID int64
	if account.ProxyID != nil {
		proxyID = *account.ProxyID
	}
	value, _ := json.Marshal([]any{account.ID, account.Platform, account.Type,
		account.Extra[OpenAIAPIKeyModeExtraKey], account.GetOpenAIBaseURL(), account.GetOpenAIApiKey(), proxyID})
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func CodexEngineCapacityIdentityMatches(current, expected *Account) bool {
	return current != nil && expected != nil && current.IsCodexEngine() && expected.IsCodexEngine() &&
		current.SchedulingRevision == expected.SchedulingRevision && current.Schedulable == expected.Schedulable &&
		CodexEngineCapacityIdentity(current) == CodexEngineCapacityIdentity(expected)
}

// CodexEngineCapacitySyncService observes the Engine key's available group
// limit. Busy request slots do not reduce this limit. Each account progresses
// independently, including accounts paused by a previous zero-capacity result.
type CodexEngineCapacitySyncService struct {
	repo     CodexEngineCapacityRepository
	upstream HTTPUpstream
	cfg      *config.Config
	interval time.Duration
	timeout  time.Duration
	mu       sync.Mutex
	started  bool
	stopped  bool
	cancel   context.CancelFunc
	inFlight map[int64]bool
	slots    chan struct{}
	wg       sync.WaitGroup
}

func NewCodexEngineCapacitySyncService(repo CodexEngineCapacityRepository, upstream HTTPUpstream, cfg *config.Config) *CodexEngineCapacitySyncService {
	return &CodexEngineCapacitySyncService{repo: repo, upstream: upstream, cfg: cfg,
		interval: codexEngineCapacitySyncInterval, timeout: codexEngineCapacitySyncTimeout, inFlight: make(map[int64]bool),
		slots: make(chan struct{}, codexEngineCapacityParallelism)}
}

func (s *CodexEngineCapacitySyncService) Start() {
	if s == nil || s.repo == nil || s.upstream == nil || s.interval <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started || s.stopped {
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
		// Existing accounts are synchronized at startup; newly configured Engine
		// accounts are discovered on the next bounded minute scan.
		s.scan(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.scan(ctx)
			}
		}
	}()
}

func (s *CodexEngineCapacitySyncService) Stop() {
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

func (s *CodexEngineCapacitySyncService) scan(ctx context.Context) {
	afterID := int64(0)
	for ctx.Err() == nil {
		readCtx, cancel := context.WithTimeout(ctx, s.timeout)
		accounts, err := s.repo.ListCodexEngineCapacityCandidates(readCtx, afterID, codexEngineCapacityPageSize)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("Codex Engine capacity scan failed")
			}
			return
		}
		if len(accounts) == 0 {
			return
		}
		for i := range accounts {
			account := accounts[i]
			if account.ID > afterID {
				afterID = account.ID
			}
			if !account.IsCodexEngine() || account.Status != StatusActive || ctx.Err() != nil {
				continue
			}
			s.mu.Lock()
			if s.stopped || s.inFlight[account.ID] {
				s.mu.Unlock()
				continue
			}
			s.mu.Unlock()
			// Bound both active requests and pending goroutines. At most one
			// database page is held while a slot becomes available.
			select {
			case s.slots <- struct{}{}:
			case <-ctx.Done():
				return
			}
			s.mu.Lock()
			if s.stopped || ctx.Err() != nil || s.inFlight[account.ID] {
				s.mu.Unlock()
				<-s.slots
				continue
			}
			s.inFlight[account.ID] = true
			s.wg.Add(1)
			s.mu.Unlock()
			go func() {
				defer s.wg.Done()
				defer func() { <-s.slots }()
				defer func() {
					s.mu.Lock()
					delete(s.inFlight, account.ID)
					s.mu.Unlock()
				}()
				if ctx.Err() != nil {
					return
				}
				if err := s.syncAccount(ctx, &account); err != nil && ctx.Err() == nil {
					// Do not log URLs, API keys, proxy credentials or upstream bodies.
					slog.Warn("Codex Engine capacity sync failed; retained last capacity", "account_id", account.ID)
				}
			}()
		}
		if len(accounts) < codexEngineCapacityPageSize {
			return
		}
	}
}

func (s *CodexEngineCapacitySyncService) syncAccount(ctx context.Context, account *Account) error {
	if account == nil || !account.IsCodexEngine() || strings.TrimSpace(account.GetOpenAIApiKey()) == "" {
		return errors.New("Engine account credentials unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	target, err := codexEngineURL(account.GetOpenAIBaseURL(), "/v1/engine/capacity")
	if err != nil {
		return errors.New("invalid Engine capacity endpoint")
	}
	// Use the same outbound URL policy and HTTP/proxy path as gateway probes.
	target, err = cnValidateProbeURL(s.cfg, target)
	if err != nil {
		return errors.New("Engine capacity endpoint rejected by URL policy")
	}
	proxy := ""
	if account.ProxyID != nil {
		if account.Proxy == nil || account.Proxy.ID != *account.ProxyID {
			return errors.New("Engine account proxy unavailable")
		}
		proxy = account.Proxy.URL()
	}
	requestCtx := WithHTTPUpstreamRedirectsDisabled(WithHTTPUpstreamProfile(ctx, HTTPUpstreamProfileOpenAI))
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, target, nil)
	if err != nil {
		return errors.New("invalid Engine capacity request")
	}
	req.Header.Set("Authorization", "Bearer "+account.GetOpenAIApiKey())
	req.Header.Set("Accept", "application/json")
	response, err := s.upstream.Do(req, proxy, account.ID, maxInt(account.Concurrency, 1))
	if err != nil {
		return errors.New("Engine capacity request failed")
	}
	if response == nil || response.Body == nil {
		return errors.New("empty Engine capacity response")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Engine capacity response HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, codexEngineCapacityMaxBody+1))
	if err != nil || len(body) > codexEngineCapacityMaxBody {
		return errors.New("invalid Engine capacity response body")
	}
	var capacity struct {
		Object           string `json:"object"`
		ConcurrencyLimit *int   `json:"concurrency_limit"`
	}
	if err := json.Unmarshal(body, &capacity); err != nil || capacity.Object != "codex_engine.capacity" ||
		capacity.ConcurrencyLimit == nil || *capacity.ConcurrencyLimit < 0 || *capacity.ConcurrencyLimit > math.MaxInt32 {
		return errors.New("invalid Engine capacity document")
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	_, err = s.repo.ApplyCodexEngineCapacity(ctx, account, *capacity.ConcurrencyLimit)
	return err
}

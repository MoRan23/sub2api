package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/sync/singleflight"
)

const (
	publicCodexCatalogURL            = "https://raw.githubusercontent.com/openai/codex/main/codex-rs/models-manager/models.json"
	publicCodexCatalogInterval       = 6 * time.Hour
	publicCodexCatalogTimeout        = 15 * time.Second
	publicCodexCatalogMaxBytes       = 1 << 20
	publicCodexCatalogFailureBackoff = time.Minute
)

// PublicCodexCatalogSnapshot contains only public model metadata. It is never
// used as evidence of an account's authorization or Daybreak capabilities.
type PublicCodexCatalogSnapshot struct {
	// Bytes serialize as base64 so Redis round trips preserve whitespace and
	// escaped/unknown JSON fields exactly instead of reformatting RawMessage.
	Body         []byte    `json:"body"`
	ETag         string    `json:"etag,omitempty"`
	LastModified string    `json:"last_modified,omitempty"`
	CheckedAt    time.Time `json:"checked_at"`
	bodyETag     string
}

// PublicCodexCatalogStore retains one last successful snapshot across restarts.
// Save ignores older observations so overlapping instances cannot roll it back.
type PublicCodexCatalogStore interface {
	Load(context.Context) (*PublicCodexCatalogSnapshot, error)
	Save(context.Context, *PublicCodexCatalogSnapshot) error
}

type publicCodexHTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

// PublicCodexCatalogService refreshes an immutable, anonymous public manifest.
// Consumers receive a read-only body and must copy it before applying policy.
type PublicCodexCatalogService struct {
	client      publicCodexHTTPClient
	store       PublicCodexCatalogStore
	mu          sync.RWMutex
	snapshot    *PublicCodexCatalogSnapshot
	nextAttempt time.Time
	flight      singleflight.Group
	ctx         context.Context
	cancel      context.CancelFunc
	startOnce   sync.Once
	wg          sync.WaitGroup
	now         func() time.Time
}

func NewPublicCodexCatalogService(client publicCodexHTTPClient, store PublicCodexCatalogStore) *PublicCodexCatalogService {
	ctx, cancel := context.WithCancel(context.Background())
	return &PublicCodexCatalogService{client: client, store: store, ctx: ctx, cancel: cancel, now: time.Now}
}

func (s *PublicCodexCatalogService) Start() {
	if s == nil {
		return
	}
	s.startOnce.Do(func() {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for {
				_, _ = s.Get(s.ctx)
				select {
				case <-s.ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
	})
}

func (s *PublicCodexCatalogService) Stop() {
	if s == nil {
		return
	}
	s.cancel()
	s.wg.Wait()
	_, _, _ = s.flight.Do("public", func() (any, error) { return nil, nil })
}

func (s *PublicCodexCatalogService) current() (*PublicCodexCatalogSnapshot, time.Time) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshot, s.nextAttempt
}

func (s *PublicCodexCatalogService) publish(snapshot *PublicCodexCatalogSnapshot) {
	if snapshot.bodyETag == "" {
		snapshot.bodyETag = codexModelsManifestBodyETag(snapshot.Body)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snapshot == nil || !snapshot.CheckedAt.Before(s.snapshot.CheckedAt) {
		s.snapshot = snapshot
	}
}

func (s *PublicCodexCatalogService) Get(ctx context.Context) (*OpenAIModelsResponse, error) {
	if s == nil {
		return bundledPublicCodexModels()
	}
	current, next := s.current()
	now := s.now()
	if (current != nil && now.Sub(current.CheckedAt) < publicCodexCatalogInterval) || now.Before(next) {
		return publicCodexModelsOrBundled(current)
	}
	if s.ctx.Err() != nil {
		return publicCodexModelsOrBundled(current)
	}
	ch := s.flight.DoChan("public", func() (any, error) {
		// A cancelled caller must not cancel the shared refresh. Stop still does.
		refreshCtx, cancel := context.WithTimeout(s.ctx, publicCodexCatalogTimeout)
		defer cancel()
		return nil, s.refresh(refreshCtx)
	})
	if current != nil {
		return publicCodexModelsOrBundled(current)
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-ch:
		current, _ = s.current()
		return publicCodexModelsOrBundled(current)
	}
}

func (s *PublicCodexCatalogService) refresh(ctx context.Context) error {
	current, next := s.current()
	now := s.now()
	if (current != nil && now.Sub(current.CheckedAt) < publicCodexCatalogInterval) || now.Before(next) {
		return nil
	}
	// Read the shared last-good copy before fetching, including after Redis recovers.
	if s.store != nil {
		loadCtx, cancel := context.WithTimeout(ctx, time.Second)
		cached, err := s.store.Load(loadCtx)
		cancel()
		if err == nil && validatePublicCodexSnapshot(cached, now) == nil {
			// Store implementations may reuse buffers; take ownership once here.
			owned := *cached
			owned.Body = bytes.Clone(cached.Body)
			s.publish(&owned)
			current, _ = s.current()
			if now.Sub(current.CheckedAt) < publicCodexCatalogInterval {
				return nil
			}
		}
	}
	updated, err := s.fetch(ctx, current, now)
	if err != nil {
		s.mu.Lock()
		s.nextAttempt = s.now().Add(publicCodexCatalogFailureBackoff)
		s.mu.Unlock()
		slog.Warn("public_codex_catalog_refresh_failed", "error", err)
		return err
	}
	s.publish(updated)
	if s.store != nil {
		saveCtx, cancel := context.WithTimeout(ctx, time.Second)
		err = s.store.Save(saveCtx, updated)
		cancel()
		if err != nil {
			slog.Warn("public_codex_catalog_cache_write_failed", "error", err)
		}
	}
	return nil
}

func (s *PublicCodexCatalogService) fetch(ctx context.Context, previous *PublicCodexCatalogSnapshot, observed time.Time) (*PublicCodexCatalogSnapshot, error) {
	if s.client == nil {
		return nil, errors.New("public catalog HTTP client unavailable")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, publicCodexCatalogURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if previous != nil {
		if validPublicCatalogValidator(previous.ETag) {
			req.Header.Set("If-None-Match", previous.ETag)
		}
		if validPublicCatalogValidator(previous.LastModified) {
			req.Header.Set("If-Modified-Since", previous.LastModified)
		}
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		if previous == nil {
			return nil, errors.New("public catalog returned 304 without cached body")
		}
		updated := *previous
		updated.CheckedAt = observed
		if v := resp.Header.Get("ETag"); validPublicCatalogValidator(v) {
			updated.ETag = v
		}
		if v := resp.Header.Get("Last-Modified"); validPublicCatalogValidator(v) {
			updated.LastModified = v
		}
		return &updated, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("public catalog HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, publicCodexCatalogMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if err := validatePublicCodexCatalog(body); err != nil {
		return nil, err
	}
	snapshot := &PublicCodexCatalogSnapshot{Body: body, CheckedAt: observed}
	if v := resp.Header.Get("ETag"); validPublicCatalogValidator(v) {
		snapshot.ETag = v
	}
	if v := resp.Header.Get("Last-Modified"); validPublicCatalogValidator(v) {
		snapshot.LastModified = v
	}
	return snapshot, nil
}

func validPublicCatalogValidator(value string) bool {
	return value != "" && len(value) <= 4096 && !strings.ContainsAny(value, "\r\n\x00")
}

func validatePublicCodexSnapshot(snapshot *PublicCodexCatalogSnapshot, now time.Time) error {
	if snapshot == nil || snapshot.CheckedAt.IsZero() || snapshot.CheckedAt.After(now.Add(time.Minute)) {
		return errors.New("invalid public catalog timestamp")
	}
	return validatePublicCodexCatalog(snapshot.Body)
}

// ValidatePublicCodexCatalogSnapshot lets persistence apply the same trust
// boundary before comparing observations, including after cache corruption.
func ValidatePublicCodexCatalogSnapshot(snapshot *PublicCodexCatalogSnapshot, now time.Time) error {
	return validatePublicCodexSnapshot(snapshot, now)
}

func validatePublicCodexCatalog(body []byte) error {
	if len(body) == 0 || len(body) > publicCodexCatalogMaxBytes {
		return errors.New("public catalog size is invalid")
	}
	if err := validateCodexModelsManifestEnvelope(body); err != nil {
		return fmt.Errorf("invalid public catalog envelope: %w", err)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("invalid public catalog JSON: %w", err)
	}
	var models []json.RawMessage
	if err := json.Unmarshal(envelope["models"], &models); err != nil {
		return fmt.Errorf("invalid public catalog models: %w", err)
	}
	if len(models) == 0 {
		return errors.New("public catalog has no models")
	}
	seen := make(map[string]struct{}, len(models))
	for _, raw := range models {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return errors.New("public catalog has an invalid model entry")
		}
		var slug string
		if err := json.Unmarshal(fields["slug"], &slug); err != nil || slug == "" || len(slug) > 256 || strings.ContainsAny(slug, "*?") || strings.IndexFunc(slug, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
			return errors.New("public catalog has an invalid model slug")
		}
		if _, ok := seen[slug]; ok {
			return errors.New("public catalog has duplicate model slugs")
		}
		seen[slug] = struct{}{}
	}
	return nil
}

func publicCodexModelsOrBundled(snapshot *PublicCodexCatalogSnapshot) (*OpenAIModelsResponse, error) {
	if snapshot == nil {
		return bundledPublicCodexModels()
	}
	return &OpenAIModelsResponse{Body: snapshot.Body, ETag: snapshot.bodyETag}, nil
}

var bundledPublicCodexModelsETag = codexModelsManifestBodyETag(bundledOfficialCodexCatalogJSON)

func bundledPublicCodexModels() (*OpenAIModelsResponse, error) {
	// Unlike account manifests, this response has no observedAt authorization evidence.
	return &OpenAIModelsResponse{Body: bundledOfficialCodexCatalogJSON, ETag: bundledPublicCodexModelsETag}, nil
}

func (s *OpenAIGatewayService) getPublicOfficialCodexModels(ctx context.Context) (*OpenAIModelsResponse, error) {
	if s == nil || s.publicCodexCatalog == nil {
		return bundledPublicCodexModels()
	}
	return s.publicCodexCatalog.Get(ctx)
}

// StopPublicCodexCatalog cancels the refresh before Redis and transports close.
func (s *OpenAIGatewayService) StopPublicCodexCatalog() {
	if s != nil {
		s.publicCodexCatalog.Stop()
	}
}

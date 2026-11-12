package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
)

const CodexEngineModelsSyncExtraKey = "codex_engine_models_sync"

// CodexEngineSourceRevisionExtraKey is a server-owned fence for source edits.
const CodexEngineSourceRevisionExtraKey = "codex_engine_source_revision"

// CodexEngineModelsSyncState distinguishes an observed empty catalog from an
// unconfigured mapping. Only the conditional repository writer publishes it.
type CodexEngineModelsSyncState struct {
	Identity      string `json:"identity"`
	Known         bool   `json:"known"`
	ModelCount    int    `json:"model_count"`
	ObservationID string `json:"observation_id"`
}

// codexEngineKnownModelCount reads a few scalars without marshaling extra or
// hashing credentials on the model-admission hot path.
func codexEngineKnownModelCount(account *Account) (int, bool) {
	if account == nil || !account.IsCodexEngine() || account.IsShadow() {
		return 0, false
	}
	raw, ok := account.Extra[CodexEngineModelsSyncExtraKey].(map[string]any)
	if !ok || raw["known"] != true {
		return 0, false
	}
	identity, _ := raw["identity"].(string)
	observation, _ := raw["observation_id"].(string)
	if identity == "" || observation == "" {
		return 0, false
	}
	var count int
	switch v := raw["model_count"].(type) {
	case int:
		count = v
	case int64:
		count = int(v)
		if int64(count) != v {
			return 0, false
		}
	case float64:
		count = int(v)
		if float64(count) != v {
			return 0, false
		}
	case json.Number:
		v64, err := v.Int64()
		if err != nil {
			return 0, false
		}
		count = int(v64)
		if int64(count) != v64 {
			return 0, false
		}
	default:
		return 0, false
	}
	return count, count >= 0
}

func CodexEngineModelsStateFromAccount(account *Account) *CodexEngineModelsSyncState {
	count, known := codexEngineKnownModelCount(account)
	if !known {
		return nil
	}
	raw := account.Extra[CodexEngineModelsSyncExtraKey].(map[string]any)
	return &CodexEngineModelsSyncState{Known: true, ModelCount: count,
		Identity: raw["identity"].(string), ObservationID: raw["observation_id"].(string)}
}

// Each account occupies one scan slot until both independent observations have
// finished. Workers share only the immutable account snapshot, not result maps.
func (s *CodexEngineCapacitySyncService) syncAccountObservations(ctx context.Context, account *Account) error {
	capacityDone := make(chan error, 1)
	go func() { capacityDone <- s.syncAccount(ctx, account) }()
	modelsErr := s.syncAccountModels(ctx, account)
	return errors.Join(<-capacityDone, modelsErr)
}

func (s *CodexEngineCapacitySyncService) syncAccountModels(ctx context.Context, account *Account) error {
	if account == nil || !account.IsCodexEngine() || strings.TrimSpace(account.GetOpenAIApiKey()) == "" {
		return errors.New("Engine account credentials unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	target, err := codexEngineURL(account.GetOpenAIBaseURL(), "/v1/models")
	if err != nil {
		return errors.New("invalid Engine models endpoint")
	}
	target, err = cnValidateProbeURL(s.cfg, target)
	if err != nil {
		return errors.New("Engine models endpoint rejected by URL policy")
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
		return errors.New("invalid Engine models request")
	}
	req.Header.Set("Authorization", "Bearer "+account.GetOpenAIApiKey())
	req.Header.Set("Accept", "application/json")
	response, err := s.upstream.Do(req, proxy, account.ID, maxInt(account.Concurrency, 1))
	if err != nil {
		return errors.New("Engine models request failed")
	}
	if response == nil || response.Body == nil {
		return errors.New("empty Engine models response")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Engine models response HTTP %d", response.StatusCode)
	}
	if strings.EqualFold(strings.TrimSpace(response.Header.Get("X-Codex-Catalog-Degraded")), "true") {
		return errors.New("Engine models catalog is degraded")
	}
	limit := resolveModelsListReadLimit(s.cfg)
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		return errors.New("invalid Engine models response body")
	}
	models, err := decodeCodexEngineModelIDs(body)
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	_, err = s.repo.ApplyCodexEngineModels(ctx, account, models)
	return err
}

func decodeCodexEngineModelIDs(body []byte) ([]string, error) {
	var catalog struct {
		Object string `json:"object"`
		Data   *[]struct {
			ID *string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &catalog); err != nil || catalog.Object != "list" || catalog.Data == nil {
		return nil, errors.New("invalid Engine models document")
	}
	models := make([]string, 0, len(*catalog.Data))
	for _, entry := range *catalog.Data {
		if entry.ID == nil {
			return nil, errors.New("missing Engine model ID")
		}
		id := strings.TrimSpace(*entry.ID)
		if id == "" || strings.ContainsAny(id, "*\x00\r\n") {
			return nil, errors.New("invalid Engine model ID")
		}
		models = append(models, id)
	}
	slices.Sort(models)
	return slices.Compact(models), nil
}

package service

import (
	"encoding/json"
	"strings"
	"sync"
	"time"
)

const (
	codexModelCapabilityCacheMaxEntries = 512
	codexModelCapabilityCacheTTL        = 5 * time.Minute
)

// CodexModelCapabilities is the stable subset of the models manifest that
// changes Codex request metadata. Missing manifest fields use the same false
// defaults as the Codex model schema.
type CodexModelCapabilities struct {
	UseResponsesLite           bool
	NodeREPLAutoReviewRequired bool
	NodeREPLDisabled           bool
	Known                      bool
}

type codexModelCapabilityEntry struct {
	capabilities CodexModelCapabilities
	expiresAt    time.Time
	order        uint64
}

type codexModelCapabilityCache struct {
	mu        sync.Mutex
	entries   map[string]codexModelCapabilityEntry
	nextOrder uint64
}

func codexModelCapabilityKey(namespace, model string) string {
	return strings.TrimSpace(namespace) + "\x00" + strings.ToLower(strings.TrimSpace(model))
}

func (c *codexModelCapabilityCache) get(namespace, model string, now time.Time) CodexModelCapabilities {
	if c == nil || strings.TrimSpace(namespace) == "" || strings.TrimSpace(model) == "" {
		return CodexModelCapabilities{}
	}
	key := codexModelCapabilityKey(namespace, model)
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return CodexModelCapabilities{}
	}
	if !now.Before(entry.expiresAt) {
		// Keep the last observed value dormant until the next manifest response.
		// A conditional models request can return 304 without a body; retaining the
		// entry lets that response refresh the observation without treating stale
		// capability data as authoritative in the meantime.
		return CodexModelCapabilities{}
	}
	return entry.capabilities
}

// refreshNamespace renews only capability values previously observed from a
// validated manifest. It deliberately cannot manufacture entries: a 304 has no
// response body from which to learn a model's capabilities.
func (c *codexModelCapabilityCache) refreshNamespace(namespace string, now time.Time) {
	if c == nil || strings.TrimSpace(namespace) == "" {
		return
	}
	prefix := strings.TrimSpace(namespace) + "\x00"
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, entry := range c.entries {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		entry.expiresAt = now.Add(codexModelCapabilityCacheTTL)
		c.entries[key] = entry
	}
}

func effectiveCodexModelCapabilities(observed CodexModelCapabilities, explicitResponsesLite bool) CodexModelCapabilities {
	if observed.Known {
		return observed
	}
	observed.UseResponsesLite = explicitResponsesLite
	return observed
}

func (c *codexModelCapabilityCache) observeManifest(namespace string, body []byte, now time.Time) {
	if c == nil || strings.TrimSpace(namespace) == "" || len(body) == 0 {
		return
	}
	var envelope struct {
		Models []struct {
			Slug                       string `json:"slug"`
			UseResponsesLite           bool   `json:"use_responses_lite"`
			NodeREPLAutoReviewRequired bool   `json:"node_repl_auto_review_required"`
			NodeREPLDisabled           bool   `json:"node_repl_disabled"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]codexModelCapabilityEntry)
	}
	for _, model := range envelope.Models {
		if strings.TrimSpace(model.Slug) == "" {
			continue
		}
		for len(c.entries) >= codexModelCapabilityCacheMaxEntries {
			oldestKey := ""
			var oldestOrder uint64
			for key, entry := range c.entries {
				if !now.Before(entry.expiresAt) {
					delete(c.entries, key)
					continue
				}
				if oldestKey == "" || entry.order < oldestOrder {
					oldestKey, oldestOrder = key, entry.order
				}
			}
			if len(c.entries) < codexModelCapabilityCacheMaxEntries || oldestKey == "" {
				break
			}
			delete(c.entries, oldestKey)
		}
		c.nextOrder++
		c.entries[codexModelCapabilityKey(namespace, model.Slug)] = codexModelCapabilityEntry{
			capabilities: CodexModelCapabilities{
				UseResponsesLite:           model.UseResponsesLite,
				NodeREPLAutoReviewRequired: model.NodeREPLAutoReviewRequired,
				NodeREPLDisabled:           model.NodeREPLDisabled,
				Known:                      true,
			},
			expiresAt: now.Add(codexModelCapabilityCacheTTL),
			order:     c.nextOrder,
		}
	}
}

func (s *OpenAIGatewayService) openAICodexModelCapabilities(namespace, model string) CodexModelCapabilities {
	if s == nil {
		return CodexModelCapabilities{}
	}
	return s.codexModelCapabilities.get(namespace, model, time.Now())
}

package service

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/tidwall/gjson"
)

// openAICodexCompactionDelivery observes only events that have actually been
// delivered to the downstream client. Remote v2 follows Codex's event contract
// directly; the item maps retain the broader legacy compact compatibility rules.
type openAICodexCompactionDelivery struct {
	terminalSuccessful          bool
	completedDelivered          bool
	terminalFailed              bool
	remoteV2CompactionDoneCount int
	authoritativeItems          map[string]struct{}
	addedItems                  map[string]struct{}
}

func (d *openAICodexCompactionDelivery) ObserveDeliveredEvent(payload []byte) {
	if d == nil || len(payload) == 0 || !gjson.ValidBytes(payload) {
		return
	}
	eventType := strings.TrimSpace(gjson.GetBytes(payload, "type").String())
	switch eventType {
	case "response.output_item.added":
		d.addItem(&d.addedItems, gjson.GetBytes(payload, "item"))
	case "response.output_item.done":
		d.addDoneItem(gjson.GetBytes(payload, "item"))
	case "response.completed":
		status := strings.TrimSpace(gjson.GetBytes(payload, "response.status").String())
		if status != "" && status != "completed" && status != "done" {
			d.terminalFailed = true
			return
		}
		d.terminalSuccessful = true
		// Older remote compact implementations have emitted a completed event
		// whose embedded response uses status=done. Preserve that remote
		// compatibility, but local Responses compaction requires an exact
		// completed status when the status field is present.
		d.completedDelivered = status == "" || status == "completed"
		for _, item := range gjson.GetBytes(payload, "response.output").Array() {
			d.addItem(&d.authoritativeItems, item)
		}
	case "response.done":
		status := strings.TrimSpace(gjson.GetBytes(payload, "response.status").String())
		if status != "" && status != "completed" && status != "done" {
			d.terminalFailed = true
			return
		}
		d.terminalSuccessful = true
		for _, item := range gjson.GetBytes(payload, "response.output").Array() {
			d.addItem(&d.authoritativeItems, item)
		}
	case "response.failed", "response.incomplete", "response.cancelled", "response.canceled", "error":
		d.terminalFailed = true
	}
}

func (d *openAICodexCompactionDelivery) addDoneItem(item gjson.Result) {
	if d == nil || !item.Exists() || !item.IsObject() || !isResponsesCompactionItemType(item.Get("type").String()) {
		return
	}
	// Remote v2 requires exactly one compaction output_item.done event. Count
	// before item identity deduplication so replaying the same item is rejected.
	d.remoteV2CompactionDoneCount++
	d.addItem(&d.authoritativeItems, item)
}

func (d *openAICodexCompactionDelivery) addItem(items *map[string]struct{}, item gjson.Result) {
	if d == nil || items == nil || !item.Exists() || !item.IsObject() || !isResponsesCompactionItemType(item.Get("type").String()) {
		return
	}
	if *items == nil {
		*items = make(map[string]struct{}, 1)
	}
	key := openAICodexCompactionItemKey(item)
	(*items)[key] = struct{}{}
}

func (d *openAICodexCompactionDelivery) Valid() bool {
	if d == nil || !d.terminalSuccessful || d.terminalFailed {
		return false
	}
	if len(d.authoritativeItems) > 0 {
		return len(d.authoritativeItems) == 1
	}
	return len(d.addedItems) == 1
}

// ValidForMode applies the result contract of the finalized physical compact
// implementation. Local Responses compaction consumes an ordinary sampling
// stream and advances only after response.completed is delivered; it does not
// produce a compaction output item. Remote v2 requires exactly one delivered
// compaction done event; legacy keeps its distinct-item compatibility contract.
func (d *openAICodexCompactionDelivery) ValidForMode(mode CodexCompactionMode) bool {
	if d == nil {
		return false
	}
	switch mode {
	case CodexCompactionModeLocalResponses:
		return d.completedDelivered && !d.terminalFailed
	case CodexCompactionModeRemoteV2:
		return d.completedDelivered &&
			d.remoteV2CompactionDoneCount == 1 &&
			!d.terminalFailed
	case CodexCompactionModeLegacy:
		return d.Valid()
	default:
		return false
	}
}

func openAICodexJSONCompactionOutputValid(payload []byte) bool {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return false
	}
	count := 0
	seen := make(map[string]struct{}, 1)
	for _, item := range gjson.GetBytes(payload, "output").Array() {
		if !item.IsObject() || !isResponsesCompactionItemType(item.Get("type").String()) {
			continue
		}
		key := openAICodexCompactionItemKey(item)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		count++
	}
	return count == 1
}

func openAICodexCompactionItemKey(item gjson.Result) string {
	if id := strings.TrimSpace(item.Get("id").String()); id != "" {
		return "id:" + id
	}
	sum := sha256.Sum256([]byte(item.Raw))
	return "raw:" + hex.EncodeToString(sum[:])
}

package service

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/tidwall/gjson"
)

// openAICodexCompactionDelivery observes only events that have actually been
// delivered to the downstream client. A window may advance only after a
// successful terminal event and exactly one distinct compaction output item.
type openAICodexCompactionDelivery struct {
	terminalSuccessful bool
	terminalFailed     bool
	items              map[string]struct{}
}

func (d *openAICodexCompactionDelivery) ObserveDeliveredEvent(payload []byte) {
	if d == nil || len(payload) == 0 || !gjson.ValidBytes(payload) {
		return
	}
	eventType := strings.TrimSpace(gjson.GetBytes(payload, "type").String())
	switch eventType {
	case "response.output_item.done":
		d.addItem(gjson.GetBytes(payload, "item"))
	case "response.completed", "response.done":
		status := strings.TrimSpace(gjson.GetBytes(payload, "response.status").String())
		if status != "" && status != "completed" && status != "done" {
			d.terminalFailed = true
			return
		}
		d.terminalSuccessful = true
		for _, item := range gjson.GetBytes(payload, "response.output").Array() {
			d.addItem(item)
		}
	case "response.failed", "response.incomplete", "response.cancelled", "response.canceled", "error":
		d.terminalFailed = true
	}
}

func (d *openAICodexCompactionDelivery) addItem(item gjson.Result) {
	if d == nil || !item.Exists() || !item.IsObject() || !isResponsesCompactionItemType(item.Get("type").String()) {
		return
	}
	if d.items == nil {
		d.items = make(map[string]struct{}, 1)
	}
	key := strings.TrimSpace(item.Get("id").String())
	if key == "" {
		sum := sha256.Sum256([]byte(item.Raw))
		key = hex.EncodeToString(sum[:])
	}
	d.items[key] = struct{}{}
}

func (d *openAICodexCompactionDelivery) Valid() bool {
	return d != nil && d.terminalSuccessful && !d.terminalFailed && len(d.items) == 1
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
		key := strings.TrimSpace(item.Get("id").String())
		if key == "" {
			sum := sha256.Sum256([]byte(item.Raw))
			key = hex.EncodeToString(sum[:])
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		count++
	}
	return count == 1
}

package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// New snapshots persist request identity and billing policy, never price cards.
// Versions 1 and 2 remain readable for previously persisted recovery jobs.
func marshalCodexEnginePricingSnapshot(f *codexEnginePricingSnapshot) (json.RawMessage, error) {
	if f == nil || f.Version != codexEnginePricingSnapshotVersion {
		return nil, errors.New("unsupported engine pricing snapshot")
	}
	if len(f.Prices) != 0 || f.SearchPrice != 0 {
		return nil, errors.New("current engine pricing snapshot must not freeze prices")
	}
	raw, err := json.Marshal(f)
	if err != nil {
		return nil, err
	}
	if len(raw) > codexEnginePricingSnapshotLimit {
		return nil, &codexEngineBillingSnapshotError{Reason: "billing_snapshot_size_limit", SizeBytes: len(raw), LimitBytes: codexEnginePricingSnapshotLimit}
	}
	return raw, nil
}

func unmarshalCodexEnginePricingSnapshot(raw json.RawMessage) (*codexEnginePricingSnapshot, error) {
	// JSONB adds whitespace on read. Bound allocation, then enforce the logical
	// compact JSON limit rather than rejecting its database rendering.
	if len(raw) > codexEnginePricingSnapshotLimit*2 {
		return nil, errors.New("engine pricing snapshot exceeds storage limit")
	}
	if len(raw) > codexEnginePricingSnapshotLimit {
		var compact bytes.Buffer
		if err := json.Compact(&compact, raw); err != nil {
			return nil, fmt.Errorf("decode engine pricing snapshot: %w", err)
		}
		if compact.Len() > codexEnginePricingSnapshotLimit {
			return nil, errors.New("engine pricing snapshot exceeds storage limit")
		}
		raw = compact.Bytes()
	}
	var f codexEnginePricingSnapshot
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("decode engine pricing snapshot: %w", err)
	}
	if f.Version == codexEnginePricingSnapshotVersion {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, fmt.Errorf("decode engine pricing snapshot: %w", err)
		}
		for _, name := range []string{"prices", "search_price", "price_refs", "price_cards", "token_prices"} {
			if _, exists := fields[name]; exists {
				return nil, errors.New("current engine pricing snapshot must not freeze prices")
			}
		}
	}
	return &f, nil
}

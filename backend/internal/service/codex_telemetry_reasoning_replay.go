package service

import "github.com/tidwall/gjson"

// CodexReasoningReplayEvidence is a response-local, bounded summary. A nil
// pointer means this producer did not observe a completed Responses terminal;
// unavailable means it did, but the optional upstream report was absent/invalid.
// No transformation items, response text or unrecognized reason strings survive.
type CodexReasoningReplayEvidence struct {
	Report        string                      `json:"report"`
	RemovedCounts CodexReasoningRemovalCounts `json:"removed_counts"`
}

type CodexReasoningRemovalCounts struct {
	OrganizationMismatch int64 `json:"organization_mismatch"`
	EndUserMismatch      int64 `json:"end_user_mismatch"`
	ModelMismatch        int64 `json:"model_mismatch"`
	PrefixMismatch       int64 `json:"prefix_mismatch"`
	TurnBoundary         int64 `json:"turn_boundary"`
	Other                int64 `json:"other"`
}

var codexReasoningRemovalReasons = [...]string{
	"organization_mismatch", "end_user_mismatch", "model_mismatch",
	"prefix_mismatch", "turn_boundary", "other",
}

func (c CodexReasoningRemovalCounts) values() [6]int64 {
	return [6]int64{c.OrganizationMismatch, c.EndUserMismatch, c.ModelMismatch, c.PrefixMismatch, c.TurnBoundary, c.Other}
}

// The parsed view is borrowed for this call only. This helper is shared by
// stream telemetry and fingerprint evidence; neither retains the JSON tree.
func codexReasoningReplayFromTerminal(root gjson.Result, eventType string) *CodexReasoningReplayEvidence {
	switch eventType {
	case "response.completed", "response.incomplete", "":
	default:
		return nil
	}
	var response gjson.Result
	var rootError bool
	if eventType == "" {
		response = root
	} else {
		root.ForEach(func(key, value gjson.Result) bool {
			switch key.Str {
			case "response":
				response = value
			case "error":
				rootError = value.IsObject()
			}
			return true
		})
	}
	if !response.IsObject() || rootError {
		return nil
	}
	var transformations gjson.Result
	var hasID, hasError bool
	var status, incompleteReason string
	// A terminal response may include a large output. Scan its fields once;
	// skip output values and retain only this call's borrowed transformation view.
	response.ForEach(func(key, value gjson.Result) bool {
		switch key.Str {
		case "id":
			hasID = value.Type == gjson.String
		case "error":
			hasError = value.IsObject()
		case "status":
			status = value.String()
		case "incomplete_details":
			incompleteReason = value.Get("reason").String()
		case "input_transformations":
			transformations = value
		}
		return true
	})
	if !hasID || hasError {
		return nil
	}
	if eventType == "" {
		eventType = "response." + status
	}
	switch eventType {
	case "response.completed":
		if status != "" && status != "completed" {
			return nil
		}
	case "response.incomplete":
		if incompleteReason != "interrupted" {
			return nil
		}
	default:
		return nil
	}
	return codexReasoningReplayFromTransformations(transformations)
}

func codexReasoningReplayFromTransformations(transformations gjson.Result) *CodexReasoningReplayEvidence {
	result := &CodexReasoningReplayEvidence{Report: "unavailable"}
	if !transformations.IsArray() {
		return result
	}
	result.Report = "no_removals_reported"
	transformations.ForEach(func(_, item gjson.Result) bool {
		if item.Get("type").String() != "reasoning" || item.Get("action").String() != "removed" {
			return true
		}
		result.Report = "removals_reported"
		switch item.Get("reason").String() {
		case "organization_mismatch":
			result.RemovedCounts.OrganizationMismatch++
		case "end_user_mismatch":
			result.RemovedCounts.EndUserMismatch++
		case "model_mismatch":
			result.RemovedCounts.ModelMismatch++
		case "prefix_mismatch":
			result.RemovedCounts.PrefixMismatch++
		case "turn_boundary":
			result.RemovedCounts.TurnBoundary++
		default:
			result.RemovedCounts.Other++
		}
		return true
	})
	if result.RemovedCounts.PrefixMismatch > 0 {
		result.Report = "prefix_mismatch_reported"
	}
	return result
}

func cloneCodexReasoningReplay(e *CodexReasoningReplayEvidence) *CodexReasoningReplayEvidence {
	if e == nil {
		return nil
	}
	copy := *e
	return &copy
}

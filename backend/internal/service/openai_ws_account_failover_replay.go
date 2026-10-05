package service

import (
	"encoding/json"
	"strings"
)

// Keep account-failover history separate from the tool-only replay used to
// repair an existing upstream connection. A replacement socket needs assistant
// output too, and cannot reconstruct a chain that began outside this attempt.
type openAIWSAccountFailoverReplay struct {
	timezone *openAIWSTimezoneReplayLedger

	lastResponseID string
	history        []json.RawMessage
	complete       bool

	payload       []byte
	model         string
	input         []json.RawMessage
	inputComplete bool
	state         *RequestTimezoneState
}

func (r *openAIWSAccountFailoverReplay) Prepare(payload []byte, model, previousResponseID string, state *RequestTimezoneState, invalidDigests map[string]struct{}) {
	if r.timezone == nil {
		r.timezone = newOpenAIWSTimezoneReplayLedger()
	}
	r.payload, r.model, r.state = payload, model, state
	r.input, r.inputComplete = nil, false
	items, exists, err := r.timezone.Record(payload, state)
	if err != nil || !exists {
		return
	}
	if len(invalidDigests) > 0 {
		items, _ = stripOpenAIInvalidEncryptedContentFromReplayItems(items, invalidDigests)
		r.history, _ = stripOpenAIInvalidEncryptedContentFromReplayItems(r.history, invalidDigests)
	}
	previousResponseID = strings.TrimSpace(previousResponseID)
	if previousResponseID != "" && (!r.complete || previousResponseID != r.lastResponseID) {
		return
	}
	r.input, r.inputComplete = buildOpenAIWSReplayInputSequenceFromItems(
		r.history, r.complete, items, true, previousResponseID != "",
	)
}

func (r *openAIWSAccountFailoverReplay) Commit(result *OpenAIForwardResult) {
	r.lastResponseID = strings.TrimSpace(result.RequestID)
	r.complete = r.inputComplete && result.wsAccountFailoverReplayComplete
	r.history = nil
	if r.complete {
		r.timezone.Commit(r.input)
		r.history = combineOpenAIWSReplayItems(r.input, result.wsAccountFailoverReplayInput)
	}
	r.timezone.Trim(r.history)
}

func (r *openAIWSAccountFailoverReplay) Failover(err error, capture OpenAIOAuthIdentityCapture) error {
	// Even an unavailable snapshot must be marked as a current-turn failure;
	// otherwise the outer handler would fall back to the connection's first frame.
	var payload []byte
	var state *RequestTimezoneState
	if r.inputComplete {
		projected, projectedState, ok := r.timezone.ProjectReplay(r.payload, r.input, openAIWSTimezoneTarget(r.state), r.state)
		if ok {
			if retry, safe, buildErr := buildOpenAIWSCurrentTurnRetryPayload(r.payload, projected, true, r.model); buildErr == nil && safe {
				payload, state = retry, projectedState
			}
		}
	}
	return withOpenAIWSCurrentTurnRetryTimezoneState(newOpenAIWSCurrentTurnFailoverError(err, payload, capture), state)
}

package service

import (
	"context"
	"errors"
	"strconv"
	"time"
)

var ErrOpenAICodexClientWindowStale = errors.New("openai Codex client window is stale or conflicts with the current lineage")

// OpenAICodexClientWindowSignal is captured only after the canonical metadata
// and the client's complete developer context-window block agree.
type OpenAICodexClientWindowSignal struct {
	Number                   uint64
	First, Current, Previous string
	Valid                    bool
}

// Client UUIDs are represented by mapping-key-scoped HMACs in durable state.
type OpenAICodexClientWindowIdentity struct {
	Number        uint64 `json:"number"`
	FirstToken    string `json:"first_token"`
	CurrentToken  string `json:"current_token"`
	PreviousToken string `json:"previous_token"`
}

type OpenAICodexClientWindowBinding struct {
	Client OpenAICodexClientWindowIdentity `json:"client"`
	Server OpenAICodexWindowSnapshot       `json:"server"`
}

type OpenAICodexClientWindowTransition struct {
	Expected                OpenAICodexWindowSnapshot
	Client                  OpenAICodexClientWindowIdentity
	ProposedContextWindowID string
	RolloverDigest          string
}

type OpenAICodexClientWindowStatus string

const (
	OpenAICodexClientWindowBound     OpenAICodexClientWindowStatus = "bound"
	OpenAICodexClientWindowUnchanged OpenAICodexClientWindowStatus = "unchanged"
	OpenAICodexClientWindowAdvanced  OpenAICodexClientWindowStatus = "advanced"
	OpenAICodexClientWindowStale     OpenAICodexClientWindowStatus = "stale"
)

type OpenAICodexClientWindowResult struct {
	Snapshot OpenAICodexWindowSnapshot
	Status   OpenAICodexClientWindowStatus
}

// This optional interface leaves ordinary window stores and their fallback
// contract unchanged. Client rollover cannot fall back across Redis failures.
type OpenAICodexClientWindowStore interface {
	ResolveOpenAICodexClientWindow(context.Context, string, OpenAICodexClientWindowTransition, time.Duration) (OpenAICodexClientWindowResult, error)
}

func ValidateOpenAICodexClientWindowIdentity(client OpenAICodexClientWindowIdentity) error {
	if client.Number > OpenAICodexWindowMaxNumber || !validOpenAICodexWindowDigest(client.FirstToken) || !validOpenAICodexWindowDigest(client.CurrentToken) {
		return errors.New("invalid openai Codex client window identity")
	}
	if client.Number == 0 {
		if client.FirstToken != client.CurrentToken || client.PreviousToken != "" {
			return errors.New("invalid openai Codex initial client window")
		}
	} else if !validOpenAICodexWindowDigest(client.PreviousToken) || client.PreviousToken == client.CurrentToken || client.FirstToken == client.CurrentToken {
		return errors.New("invalid openai Codex advanced client window")
	}
	return nil
}

func ValidateOpenAICodexClientWindowBinding(binding OpenAICodexClientWindowBinding) error {
	if err := ValidateOpenAICodexClientWindowIdentity(binding.Client); err != nil {
		return err
	}
	return ValidateOpenAICodexWindowSnapshot(binding.Server)
}

func ValidateOpenAICodexClientWindowTransition(mappingKey string, transition OpenAICodexClientWindowTransition) error {
	if !validOpenAICodexWindowMappingKey(mappingKey) || !validOpenAICodexWindowDigest(transition.RolloverDigest) {
		return errors.New("invalid openai Codex client window transition key")
	}
	if err := ValidateOpenAICodexWindowSnapshot(transition.Expected); err != nil {
		return err
	}
	if err := ValidateOpenAICodexClientWindowIdentity(transition.Client); err != nil {
		return err
	}
	if _, err := canonicalUUIDv7(transition.ProposedContextWindowID); err != nil {
		return err
	}
	return nil
}

func sameOpenAICodexWindowIdentity(a, b OpenAICodexWindowSnapshot) bool {
	return a.ThreadID == b.ThreadID && a.Number == b.Number && a.ContextWindowID == b.ContextWindowID
}

func directOpenAICodexWindowSuccessor(next, previous OpenAICodexWindowSnapshot) bool {
	return previous.Number < OpenAICodexWindowMaxNumber && next.ThreadID == previous.ThreadID &&
		next.Number == previous.Number+1 && next.PreviousContextWindowID == previous.ContextWindowID &&
		next.ContextWindowID != previous.ContextWindowID && next.FirstContextWindowID == previous.FirstContextWindowID
}

func validateOpenAICodexClientWindowResult(transition OpenAICodexClientWindowTransition, result OpenAICodexClientWindowResult) error {
	if ValidateOpenAICodexWindowSnapshot(result.Snapshot) != nil || result.Snapshot.ThreadID != transition.Expected.ThreadID {
		return ErrOpenAICodexWindowStoredInvalid
	}
	expected := normalizeOpenAICodexWindowHistory(transition.Expected)
	switch result.Status {
	case OpenAICodexClientWindowAdvanced:
		if !directOpenAICodexWindowSuccessor(result.Snapshot, expected) || result.Snapshot.ContextWindowID != transition.ProposedContextWindowID || result.Snapshot.LastCompactDigest != transition.RolloverDigest {
			return ErrOpenAICodexWindowStoredInvalid
		}
	case OpenAICodexClientWindowBound:
		if result.Snapshot != expected && !directOpenAICodexWindowSuccessor(result.Snapshot, expected) {
			return ErrOpenAICodexWindowStoredInvalid
		}
	case OpenAICodexClientWindowUnchanged:
		if result.Snapshot != expected && !directOpenAICodexWindowSuccessor(result.Snapshot, expected) && !directOpenAICodexWindowSuccessor(expected, result.Snapshot) {
			return ErrOpenAICodexWindowStoredInvalid
		}
	default:
		return ErrOpenAICodexWindowStoredInvalid
	}
	return nil
}

// ApplyOpenAICodexClientWindowTransition is the shared state machine used inside
// the local mutex and the Redis WATCH/MULTI transaction. It never writes client
// UUIDs into a server snapshot, and it never changes a stale stored winner.
func ApplyOpenAICodexClientWindowTransition(current OpenAICodexWindowSnapshot, binding *OpenAICodexClientWindowBinding, transition OpenAICodexClientWindowTransition) (OpenAICodexClientWindowResult, *OpenAICodexClientWindowBinding, error) {
	if ValidateOpenAICodexWindowSnapshot(current) != nil || current.ThreadID != transition.Expected.ThreadID {
		return OpenAICodexClientWindowResult{}, nil, ErrOpenAICodexWindowStoredInvalid
	}
	storedCurrent := current
	stale := func() (OpenAICodexClientWindowResult, *OpenAICodexClientWindowBinding, error) {
		return OpenAICodexClientWindowResult{Snapshot: storedCurrent, Status: OpenAICodexClientWindowStale}, nil, ErrOpenAICodexClientWindowStale
	}
	if binding == nil {
		if !sameOpenAICodexWindowIdentity(current, transition.Expected) {
			return stale()
		}
		next := &OpenAICodexClientWindowBinding{Client: transition.Client, Server: current}
		result := OpenAICodexClientWindowResult{Snapshot: current, Status: OpenAICodexClientWindowBound}
		if validateOpenAICodexClientWindowResult(transition, result) != nil {
			return stale()
		}
		return result, next, nil
	}
	if ValidateOpenAICodexClientWindowBinding(*binding) != nil || binding.Server.ThreadID != current.ThreadID {
		return OpenAICodexClientWindowResult{}, nil, ErrOpenAICodexWindowStoredInvalid
	}
	if transition.Client == binding.Client {
		if !sameOpenAICodexWindowIdentity(current, binding.Server) &&
			(binding.Server.Number >= OpenAICodexWindowMaxNumber || current.Number != binding.Server.Number+1 || current.PreviousContextWindowID != binding.Server.ContextWindowID || current.FirstContextWindowID != binding.Server.FirstContextWindowID) {
			return stale()
		}
		// Preserve the original mapping after compact has advanced the main
		// window; a physical retry still belongs to this client window.
		copy := *binding
		result := OpenAICodexClientWindowResult{Snapshot: binding.Server, Status: OpenAICodexClientWindowUnchanged}
		if validateOpenAICodexClientWindowResult(transition, result) != nil {
			return stale()
		}
		return result, &copy, nil
	}
	if binding.Client.Number >= OpenAICodexWindowMaxNumber || transition.Client.Number != binding.Client.Number+1 ||
		transition.Client.FirstToken != binding.Client.FirstToken || transition.Client.PreviousToken != binding.Client.CurrentToken || transition.Client.CurrentToken == binding.Client.CurrentToken {
		return stale()
	}
	status := OpenAICodexClientWindowBound
	if sameOpenAICodexWindowIdentity(current, binding.Server) {
		if !sameOpenAICodexWindowIdentity(current, transition.Expected) {
			return stale()
		}
		if current.Number >= OpenAICodexWindowMaxNumber || transition.ProposedContextWindowID == current.ContextWindowID {
			return OpenAICodexClientWindowResult{}, nil, errors.New("invalid openai Codex client rollover proposal")
		}
		current.Number++
		current.PreviousContextWindowID = current.ContextWindowID
		current.ContextWindowID = transition.ProposedContextWindowID
		current.LastCompactDigest = transition.RolloverDigest
		status = OpenAICodexClientWindowAdvanced
	} else if binding.Server.Number >= OpenAICodexWindowMaxNumber || current.Number != binding.Server.Number+1 || current.PreviousContextWindowID != binding.Server.ContextWindowID || current.FirstContextWindowID != binding.Server.FirstContextWindowID {
		return stale()
	}
	next := &OpenAICodexClientWindowBinding{Client: transition.Client, Server: current}
	result := OpenAICodexClientWindowResult{Snapshot: current, Status: status}
	if validateOpenAICodexClientWindowResult(transition, result) != nil {
		return stale()
	}
	return result, next, nil
}

func (s *openAICodexWindowLocalStore) ResolveOpenAICodexClientWindow(_ context.Context, mappingKey string, transition OpenAICodexClientWindowTransition, ttl time.Duration) (OpenAICodexClientWindowResult, error) {
	if err := ValidateOpenAICodexClientWindowTransition(mappingKey, transition); err != nil {
		return OpenAICodexClientWindowResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.liveEntryLocked(mappingKey, time.Now())
	if entry == nil {
		return OpenAICodexClientWindowResult{}, ErrOpenAICodexWindowStoredInvalid
	}
	result, binding, err := ApplyOpenAICodexClientWindowTransition(entry.snapshot, entry.clientWindow, transition)
	if err != nil {
		return result, err
	}
	if result.Status == OpenAICodexClientWindowAdvanced {
		entry.snapshot = result.Snapshot
	}
	entry.clientWindow = binding
	entry.expiresAt = time.Now().Add(normalizeOpenAICodexWindowTTL(ttl))
	s.recency.MoveToFront(entry.recency)
	return result, nil
}

func (s *OpenAICodexWindowRuntimeStore) ResolveOpenAICodexClientWindow(ctx context.Context, mappingKey string, transition OpenAICodexClientWindowTransition, ttl time.Duration) (OpenAICodexClientWindowResult, error) {
	if s == nil || s.local == nil {
		return OpenAICodexClientWindowResult{}, ErrOpenAICodexWindowStoreUnavailable
	}
	if err := ValidateOpenAICodexClientWindowTransition(mappingKey, transition); err != nil {
		return OpenAICodexClientWindowResult{}, err
	}
	if s.primary == nil {
		return s.local.ResolveOpenAICodexClientWindow(ctx, mappingKey, transition, ttl)
	}
	primary, ok := s.primary.(OpenAICodexClientWindowStore)
	if !ok {
		return OpenAICodexClientWindowResult{}, ErrOpenAICodexWindowStoreUnavailable
	}
	primaryCtx, cancel := context.WithTimeout(ctx, openAICodexWindowStoreTimeout)
	defer cancel()
	result, err := primary.ResolveOpenAICodexClientWindow(primaryCtx, mappingKey, transition, ttl)
	if err == nil {
		if validateOpenAICodexClientWindowResult(transition, result) != nil {
			return OpenAICodexClientWindowResult{}, ErrOpenAICodexWindowStoredInvalid
		}
		s.local.acceptPrimary(mappingKey, result.Snapshot, ttl)
	}
	return result, err
}

func (s *OpenAIGatewayService) ResolveOpenAICodexClientWindowSnapshot(ctx context.Context, mappingKey string, expected OpenAICodexWindowSnapshot, signal OpenAICodexClientWindowSignal, proposed string) (OpenAICodexClientWindowResult, error) {
	if !signal.Valid {
		return OpenAICodexClientWindowResult{}, errors.New("openai Codex client window signal is not validated")
	}
	secret := ""
	if s != nil && s.cfg != nil {
		secret = s.cfg.JWT.Secret
	}
	identity := OpenAICodexClientWindowIdentity{Number: signal.Number}
	for _, field := range []struct {
		raw   string
		token *string
	}{{signal.First, &identity.FirstToken}, {signal.Current, &identity.CurrentToken}, {signal.Previous, &identity.PreviousToken}} {
		if field.raw == "" && field.token == &identity.PreviousToken {
			continue
		}
		if _, err := canonicalUUIDv7(field.raw); err != nil {
			return OpenAICodexClientWindowResult{}, err
		}
		token, err := openAICodexHMAC(secret, "sub2api/openai-codex-client-window/v1/token", mappingKey, field.raw)
		if err != nil {
			return OpenAICodexClientWindowResult{}, err
		}
		*field.token = token
	}
	digest, err := openAICodexHMAC(secret, "sub2api/openai-codex-client-window/v1/rollover", mappingKey, expected.ThreadID, strconv.FormatUint(expected.Number, 10), expected.ContextWindowID, strconv.FormatUint(identity.Number, 10), identity.FirstToken, "previous:"+identity.PreviousToken, identity.CurrentToken)
	if err != nil {
		return OpenAICodexClientWindowResult{}, err
	}
	return s.openAICodexWindowRuntimeStore().ResolveOpenAICodexClientWindow(ctx, mappingKey, OpenAICodexClientWindowTransition{Expected: expected, Client: identity, ProposedContextWindowID: proposed, RolloverDigest: digest}, OpenAICodexWindowTTL)
}

var _ OpenAICodexClientWindowStore = (*openAICodexWindowLocalStore)(nil)
var _ OpenAICodexClientWindowStore = (*OpenAICodexWindowRuntimeStore)(nil)

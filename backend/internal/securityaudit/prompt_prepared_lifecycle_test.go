package securityaudit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

// Keep the pre-preparation snapshot algorithm as an independent reference for
// metadata and full/latest selection. Protocol extraction itself is unchanged.
func originalPromptSnapshot(req Request, latestOnly bool) (PromptSnapshot, error) {
	var document any
	if err := json.Unmarshal(req.Body, &document); err != nil {
		return PromptSnapshot{}, errors.New("prompt audit request JSON is invalid")
	}
	extracted := extractProtocolSegments(req.Protocol, document)
	segments := normalizeSegmentsLatestUserFirst(extracted)
	if latestOnly {
		segments = blockingSegmentsLatestUserAndPreviousOutput(extracted)
	}
	if len(segments) == 0 {
		return PromptSnapshot{}, ErrNoPromptText
	}
	scanText, metadataText := buildPrioritizedScanText(segments)
	digest := sha256.Sum256([]byte(metadataText))
	stage := strings.TrimSpace(req.Stage)
	if stage == "" {
		stage = "http"
	}
	return PromptSnapshot{
		RequestID: req.RequestID, UserID: req.UserID, UsernameSnapshot: req.Username,
		UserEmailSnapshot: req.UserEmail, APIKeyID: req.APIKeyID, APIKeyNameSnapshot: req.APIKeyName,
		GroupID: cloneInt64Ptr(req.GroupID), GroupName: req.GroupName, Provider: req.Provider,
		Endpoint: req.Endpoint, Protocol: req.Protocol, Model: req.Model,
		PromptHash: hex.EncodeToString(digest[:]), RedactedPreview: BuildPromptPreview(metadataText, DefaultPromptPreviewMaxRunes),
		FullPrompt:   BuildFullPrompt(metadataText, DefaultFullPromptMaxRunes),
		PromptLength: utf8.RuneCountInString(metadataText), MessageCount: len(segments), Stage: stage,
		ScanText: scanText,
	}, nil
}

func TestPreparedPromptSnapshotsPreserveOriginalBehavior(t *testing.T) {
	for _, tc := range []struct {
		name, protocol, body string
	}{
		{"chat multipart", "openai_chat_completions", `{"messages":[{"role":"system","content":"system"},{"role":"user","content":"older orchid"},{"role":"assistant","content":"previous output"},{"role":"user","content":[{"type":"text","text":"  prune 中文  "},{"type":"text","text":"😀é\u0000tail"},{"type":"image_url","image_url":{"url":"data:image/png;base64,IMAGE_ONLY"}}]}]}`},
		{"responses", "responses", `{"instructions":"secret=abcdefgh123","input":[{"role":"user","content":"old"},{"role":"assistant","content":"previous"},{"role":"user","content":[{"type":"input_text","text":"最新 orchid"},{"type":"input_text","text":"prune"}]}]}`},
		{"websocket nested", "responses_websocket", `{"type":"response.create","response":{"instructions":"system","input":"prune orchid"}}`},
		{"websocket direct", "responses_websocket", `{"type":"response.create","input":"direct","response":{"input":"ignored"}}`},
		{"anthropic", "anthropic_messages", `{"system":[{"type":"text","text":"system"}],"messages":[{"role":"user","content":"old"},{"role":"assistant","content":[{"type":"text","text":"previous"}]},{"role":"user","content":[{"type":"text","text":"prune orchid"},{"type":"image","source":{"data":"IMAGE_ONLY"}}]}]}`},
		{"gemini batch and instances", "gemini", `{"systemInstruction":{"parts":[{"text":"shared"}]},"contents":[{"role":"user","parts":[{"text":"old"}]},{"role":"model","parts":[{"text":"previous"}]},{"role":"user","parts":[{"text":"prune orchid"}]}],"instances":[{"prompt":"one"},{"prompt":"two"}],"requests":[{"system_instruction":{"parts":[{"text":"member system"}]},"contents":[{"role":"user","parts":[{"text":"member"}]}],"instances":[{"prompt":"member instance"}]}]}`},
		{"images", "openai_images", `{"prompt":"draw orchid","image":"IMAGE_ONLY"}`},
		{"system one", "typesafe_systemone", `{"questions":{"q1":{"type":"boolean","instructions":"orchid","criteria":"prune"}},"state":{"latest":"中文😀"},"extension":"extra"}`},
		{"no user fallback", "responses", `{"instructions":"system only","input":[{"role":"assistant","content":"assistant only"}]}`},
		{"empty text", "responses", `{"input":"   "}`},
		{"image only", "responses", `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"IMAGE_ONLY"}]}]}`},
		{"non-create websocket", "responses_websocket", `{"type":"response.cancel","input":"ignored"}`},
		{"invalid JSON", "responses", `{"input":`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			group := int64(17)
			req := Request{
				RequestID: "prepared-request", UserID: 11, Username: "user", UserEmail: "person@example.test",
				APIKeyID: 13, APIKeyName: "key", GroupID: &group, GroupName: "group", Provider: "provider",
				Endpoint: "/test", Protocol: tc.protocol, Model: "model", Body: []byte(tc.body), Stage: " websocket ",
			}
			for _, latestFirst := range []bool{false, true} {
				t.Run(fmt.Sprintf("latest_first=%t", latestFirst), func(t *testing.T) {
					input := req.Clone()
					wantFull, fullErr := originalPromptSnapshot(input, false)
					wantLatest, latestErr := originalPromptSnapshot(input, true)
					input = preparePromptRequest(input)
					_ = input.prepared.prepare(input)
					// Simulate the ingress buffer being reused before lazy snapshots.
					for index := range input.Body {
						input.Body[index] = '!'
					}
					input.Body = nil
					for _, latest := range []bool{latestFirst, !latestFirst, latestFirst} {
						want, wantErr := wantFull, fullErr
						if latest {
							want, wantErr = wantLatest, latestErr
						}
						got, err := input.prepared.snapshot(input, latest)
						if wantErr != nil {
							require.EqualError(t, err, wantErr.Error())
						} else {
							require.NoError(t, err)
						}
						require.Equal(t, want, got)
					}
				})
			}
		})
	}
}

func TestPreparedPromptCombinationAndSnapshotsShareOwnedText(t *testing.T) {
	cfg, err := ActiveFromStorage(storageConfig{
		KeywordAllGroups:    true,
		CombinationPolicies: []CombinationPolicy{{ID: "garden", Name: "garden", Enabled: true, TopicRules: []string{"orchid"}, IntentRules: []string{"prune"}}},
	}, true, prefixEncryptor{})
	require.NoError(t, err)
	for _, tc := range []struct {
		name, protocol, body string
		matched              bool
	}{
		{"history and normalized latest", "responses", `{"input":[{"role":"user","content":"ＯＲＣＨＩＤ"},{"role":"assistant","content":"context"},{"role":"user","content":"ＰＲＵＮＥ"}]}`, true},
		{"gemini shared system", "gemini", `{"systemInstruction":{"parts":[{"text":"ＯＲＣＨＩＤ"}]},"instances":[{"prompt":"hello"},{"prompt":"ＰＲＵＮＥ"}]}`, true},
		{"gemini siblings isolated", "gemini", `{"requests":[{"contents":[{"role":"user","parts":[{"text":"orchid"}]}]},{"contents":[{"role":"user","parts":[{"text":"prune"}]}]}]}`, false},
		{"gemini instances isolated", "gemini", `{"instances":[{"prompt":"orchid"},{"prompt":"prune"}]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := Request{Protocol: tc.protocol, Body: []byte(tc.body)}
			wantMatch, err := cfg.MatchCombination(req)
			require.NoError(t, err)
			require.Equal(t, tc.matched, wantMatch != nil)
			wantFull, err := originalPromptSnapshot(req, false)
			require.NoError(t, err)
			wantLatest, err := originalPromptSnapshot(req, true)
			require.NoError(t, err)
			req = preparePromptRequest(req)
			scan, err := req.prepared.scanText(req)
			require.NoError(t, err)
			require.Equal(t, wantFull.ScanText, scan)
			for index := range req.Body {
				req.Body[index] = '!'
			}
			req.Body = nil
			// Full/latest snapshot materialization and lazily normalized combination
			// evidence can be requested concurrently from the same prepared input.
			var wg sync.WaitGroup
			for range 8 {
				wg.Go(func() {
					match, err := cfg.MatchCombination(req)
					if err != nil {
						t.Errorf("combination: %v", err)
						return
					}
					if !reflect.DeepEqual(wantMatch, match) {
						t.Errorf("combination changed after body reuse: got %+v, want %+v", match, wantMatch)
					}
					for _, latest := range []bool{false, true} {
						want := wantFull
						if latest {
							want = wantLatest
						}
						got, err := req.prepared.snapshot(req, latest)
						if err != nil || !reflect.DeepEqual(want, got) {
							t.Errorf("snapshot latest=%t changed after body reuse: %v", latest, err)
						}
					}
				})
			}
			wg.Wait()
		})
	}
}

type preparedLifecycleEngine struct {
	mode                         Mode
	keyword, evaluated, enqueued Request
}

func (e *preparedLifecycleEngine) EffectiveMode() Mode { return e.mode }
func (e *preparedLifecycleEngine) CheckKeyword(_ context.Context, req Request) (*PromptDecision, error) {
	e.keyword = req
	return &PromptDecision{Kind: DecisionAllow, AllowNextStage: true}, nil
}
func (e *preparedLifecycleEngine) Evaluate(_ context.Context, req Request) (*PromptDecision, error) {
	e.evaluated = req
	return &PromptDecision{Kind: DecisionAllow, AllowNextStage: true}, nil
}
func (e *preparedLifecycleEngine) Enqueue(_ context.Context, req Request) error {
	e.enqueued = req
	return nil
}

type preparedLifecycleBorrowingEngine struct{ *preparedLifecycleEngine }

func (*preparedLifecycleBorrowingEngine) prepareRequest(req Request) Request {
	return preparePromptRequest(req)
}

func TestCoordinatorPreparedInputBorrowsBodyOnlyForOptedInEngine(t *testing.T) {
	for _, mode := range []Mode{ModeOff, ModeAsync, ModeBlocking} {
		for _, borrowed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/borrowed=%t", mode, borrowed), func(t *testing.T) {
				body := []byte(`{"input":"original"}`)
				spy := &preparedLifecycleEngine{mode: mode}
				var engine PromptEngine = spy
				if borrowed {
					engine = &preparedLifecycleBorrowingEngine{spy}
				}
				decision := NewCoordinator(nil, engine).Check(context.Background(), Request{Protocol: "responses", Body: body})
				require.Equal(t, DecisionAllow, decision.Kind)
				seen := []Request{spy.keyword}
				if mode == ModeAsync {
					seen = append(seen, spy.enqueued)
				} else if mode == ModeBlocking {
					seen = append(seen, spy.evaluated)
				}
				for _, req := range seen {
					require.Equal(t, body, req.Body)
					require.Equal(t, borrowed, &body[0] == &req.Body[0])
					if borrowed {
						require.NotNil(t, req.prepared)
						require.Same(t, spy.keyword.prepared, req.prepared)
					} else {
						require.Nil(t, req.prepared)
					}
				}
				require.Equal(t, `{"input":"original"}`, string(body))
			})
		}
	}
}

func TestPreparedPromptServiceReusesKeywordInputForGuard(t *testing.T) {
	cfg, err := ActiveFromStorage(storageConfig{
		Enabled: true, BlockingEnabled: true, BlockingLatestTurnOnly: true, AllGroups: true,
		KeywordBlockingEnabled: true, KeywordAllGroups: true, BlockedKeywords: []string{"forbidden"},
		CombinationPolicies: []CombinationPolicy{{ID: "garden", Name: "garden", Enabled: true, TopicRules: []string{"orchid"}, IntentRules: []string{"prune"}}},
	}, true, prefixEncryptor{})
	require.NoError(t, err)
	cfg.Endpoints = []ActiveEndpoint{{ID: "guard", Enabled: true, InputLimit: 4096, TimeoutMS: 1000}}
	var scanned []string
	evaluator := newGuardEvaluator(PromptScannerFunc(func(_ context.Context, _ ActiveEndpoint, chunk string, _ []string) (*NormalizedResult, error) {
		scanned = append(scanned, chunk)
		return &NormalizedResult{Decision: EventPass, RiskLevel: RiskLow, Action: ActionAllow, ScannerScores: map[string]float64{}, ScannerEvidence: map[string]string{}}, nil
	}), nil, nil, 1, 1)
	service := &PromptService{config: &fakeConfigStore{active: true, cfg: cfg}, evaluator: evaluator}
	req := preparePromptRequest(Request{Protocol: "responses", Body: []byte(`{"input":[{"role":"user","content":"old orchid"},{"role":"assistant","content":"previous"},{"role":"user","content":"hello 中文😀"}]}`)})
	want, err := originalPromptSnapshot(req, true)
	require.NoError(t, err)
	decision, err := service.CheckKeyword(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, DecisionAllow, decision.Kind)
	require.Equal(t, PromptSnapshot{}, req.prepared.full, "a local miss does not need snapshot metadata")
	for index := range req.Body {
		req.Body[index] = '!'
	}
	decision, err = service.Evaluate(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, DecisionAllow, decision.Kind)
	require.Equal(t, SplitRunes(want.ScanText, 4096), scanned)
}

type preparedLifecycleGatedConfig struct {
	*fakeConfigStore
	entered chan struct{}
	release chan struct{}
}

func (c *preparedLifecycleGatedConfig) Active() (ActiveConfig, bool) {
	close(c.entered)
	<-c.release
	return c.fakeConfigStore.Active()
}

func TestPreparedPromptAsyncEnqueueOwnsTextBeforeReturning(t *testing.T) {
	group := int64(9)
	config := &fakeConfigStore{active: true, cfg: ActiveConfig{
		RiskControlEnabled: true, Enabled: true, GroupIDs: []int64{group}, QueueCapacity: 10,
		Endpoints: []ActiveEndpoint{{ID: "guard", Enabled: true}},
	}}
	gate := &preparedLifecycleGatedConfig{fakeConfigStore: config, entered: make(chan struct{}), release: make(chan struct{})}
	repo := &fakeJobRepository{}
	payload := &fakePayloadStore{values: map[int64]string{}}
	service := &PromptService{
		config: config, enqueuer: NewEnqueuer(gate, repo, payload),
		background: context.Background(), enqueueSlots: make(chan struct{}, 1),
	}
	req := preparePromptRequest(Request{
		RequestID: "async-owned", Protocol: "responses", GroupID: &group,
		Body: []byte(`{"input":[{"role":"user","content":[{"type":"input_text","text":"owned 中文😀"},{"type":"input_image","image_url":"data:image/png;base64,` + strings.Repeat("A", 64<<10) + `"}]}]}`),
	})
	want, err := originalPromptSnapshot(req, false)
	require.NoError(t, err)
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(gate.release) })
		service.enqueueWG.Wait()
	})
	require.NoError(t, service.Enqueue(context.Background(), req))
	select {
	case <-gate.entered:
	case <-time.After(time.Second):
		t.Fatal("async enqueuer did not reach controlled handoff")
	}
	require.NotEmpty(t, req.prepared.segments, "text must be owned before Enqueue returns")
	require.Equal(t, PromptSnapshot{}, req.prepared.full, "snapshot metadata remains lazy until the worker needs it")
	for index := range req.Body {
		req.Body[index] = '!'
	}
	req.Body = nil
	group = 99
	releaseOnce.Do(func() { close(gate.release) })
	service.enqueueWG.Wait()
	require.Equal(t, want.Redacted(), repo.createdSnapshot)
	require.Equal(t, want.ScanText, payload.values[1])
	require.Empty(t, service.enqueueSlots)
}

func TestPreparedPromptAsyncSkipsExcludedInputWithoutParsing(t *testing.T) {
	group := int64(9)
	for _, tc := range []struct {
		name      string
		groups    []int64
		endpoints []ActiveEndpoint
	}{
		{"out of scope", []int64{10}, []ActiveEndpoint{{ID: "guard", Enabled: true}}},
		{"no endpoint", []int64{9}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := &fakeConfigStore{active: true, cfg: ActiveConfig{RiskControlEnabled: true, Enabled: true, GroupIDs: tc.groups, Endpoints: tc.endpoints}}
			repo := &fakeJobRepository{}
			service := &PromptService{config: config, enqueuer: NewEnqueuer(config, repo, &fakePayloadStore{}), background: context.Background(), enqueueSlots: make(chan struct{}, 1)}
			req := preparePromptRequest(Request{Protocol: "responses", GroupID: &group, Body: []byte(`invalid JSON`)})
			require.NoError(t, service.Enqueue(context.Background(), req))
			require.Nil(t, req.prepared.err)
			require.Nil(t, req.prepared.segments)
			require.Empty(t, service.enqueueSlots)
			require.Error(t, req.prepared.prepare(req), "the skip path must not consume parsing")
		})
	}
}

func originalTrimRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}

func TestTrimRunesMatchesOriginalUnicodeAndInvalidUTF8(t *testing.T) {
	values := []string{
		"", "ascii", "中文日本語", "😀🧑‍💻🌳", "éä", "already…", "a\x00b",
		"\xff", "\xffa", "a\xffb", "\xc0\xafxyz", "\xe2\x82", "a\xf0\x9f\x92b", "\xed\xa0\x80",
		"�\xff😀", "\xf4\x90\x80\x80tail", strings.Repeat("长😀a", 1024),
	}
	for _, value := range values {
		for _, limit := range []int{-2, -1, 0, 1, 2, 3, 4, 5, utf8.RuneCountInString(value), utf8.RuneCountInString(value) + 1, int(^uint(0) >> 1)} {
			if got, want := TrimRunes(value, limit), originalTrimRunes(value, limit); got != want {
				t.Fatalf("TrimRunes(%q, %d) = %q, want %q", value, limit, got, want)
			}
		}
	}
	// Every one- and two-byte sequence exercises invalid continuation bytes,
	// truncated encodings and valid two-byte runes at the truncation boundary.
	for first := range 256 {
		for second := -1; second < 256; second++ {
			value := string([]byte{byte(first)})
			if second >= 0 {
				value += string([]byte{byte(second)})
			}
			for limit := 1; limit <= 3; limit++ {
				if got, want := TrimRunes(value, limit), originalTrimRunes(value, limit); got != want {
					t.Fatalf("TrimRunes(%x, %d) = %x, want %x", value, limit, got, want)
				}
			}
		}
	}
}

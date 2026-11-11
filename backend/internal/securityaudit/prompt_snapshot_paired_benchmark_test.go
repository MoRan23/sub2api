package securityaudit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// The old full-snapshot path is retained only for measurement. Unlike the
// behavior oracle, this also uses the pre-optimization redaction and truncation.
func originalMeasuredPromptSnapshot(req Request) (PromptSnapshot, error) {
	var document any
	if err := json.Unmarshal(req.Body, &document); err != nil {
		return PromptSnapshot{}, err
	}
	segments := normalizeSegmentsLatestUserFirst(extractProtocolSegments(req.Protocol, document))
	if len(segments) == 0 {
		return PromptSnapshot{}, ErrNoPromptText
	}
	scan, metadata := buildPrioritizedScanText(segments)
	digest := sha256.Sum256([]byte(metadata))
	stage := strings.TrimSpace(req.Stage)
	if stage == "" {
		stage = "http"
	}
	return PromptSnapshot{
		RequestID: req.RequestID, UserID: req.UserID, UsernameSnapshot: req.Username,
		UserEmailSnapshot: req.UserEmail, APIKeyID: req.APIKeyID, APIKeyNameSnapshot: req.APIKeyName,
		GroupID: cloneInt64Ptr(req.GroupID), GroupName: req.GroupName, Provider: req.Provider,
		Endpoint: req.Endpoint, Protocol: req.Protocol, Model: req.Model,
		PromptHash: hex.EncodeToString(digest[:]), RedactedPreview: originalHeapPromptPreview(metadata, DefaultPromptPreviewMaxRunes),
		FullPrompt:   originalTrimRunes(strings.TrimSpace(strings.ReplaceAll(metadata, "\x00", "")), DefaultFullPromptMaxRunes),
		PromptLength: utf8.RuneCountInString(metadata), MessageCount: len(segments), Stage: stage, ScanText: scan,
	}, nil
}

// Each iteration alternates old/new/new/old using the same immutable body.
// Fixture construction, equality checks and forced GC are outside measured time.
func BenchmarkPromptSnapshotPaired(b *testing.B) {
	for _, size := range []int{1 << 20, 4 << 20} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			body, err := json.Marshal(map[string]any{"model": "test-model", "input": []any{
				map[string]any{"role": "user", "content": strings.Repeat("safe text ", size/10)},
				map[string]any{"role": "assistant", "content": "context"},
				map[string]any{"role": "user", "content": "hello"},
			}})
			if err != nil {
				b.Fatal(err)
			}
			req := Request{Protocol: "responses", Body: body}
			oldValue, err := originalMeasuredPromptSnapshot(req)
			if err != nil {
				b.Fatal(err)
			}
			newValue, err := ExtractPromptSnapshot(req)
			if err != nil || !reflect.DeepEqual(oldValue, newValue) {
				b.Fatal("old and new full snapshots differ", err)
			}
			oldValue, newValue = PromptSnapshot{}, PromptSnapshot{}
			runtime.GC()
			runtime.GC()
			var oldTime, newTime time.Duration
			b.ResetTimer()
			for b.Loop() {
				for _, old := range []bool{true, false, false, true} {
					started := time.Now()
					var snapshot PromptSnapshot
					if old {
						snapshot, err = originalMeasuredPromptSnapshot(req)
						oldTime += time.Since(started)
					} else {
						snapshot, err = ExtractPromptSnapshot(req)
						newTime += time.Since(started)
					}
					if err != nil {
						b.Fatal(err)
					}
					runtime.KeepAlive(snapshot)
				}
			}
			b.ReportMetric(float64(oldTime.Nanoseconds())/float64(2*b.N), "old-ns/call")
			b.ReportMetric(float64(newTime.Nanoseconds())/float64(2*b.N), "new-ns/call")
		})
	}
}

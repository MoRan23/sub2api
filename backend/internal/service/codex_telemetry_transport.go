package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/codexnative"
)

func (s *CodexTelemetryService) enqueueAnalyticsLocked(profile codexTelemetryProfile, attemptID uint64, events []codexAnalyticsEvent) {
	if len(events) == 0 {
		return
	}
	body, err := json.Marshal(map[string]any{"events": events})
	if err != nil {
		s.skipLocked(profile, attemptID, "event_encoding_failed")
		return
	}
	names := make([]string, 0, len(events))
	seen := make(map[string]bool)
	for _, event := range events {
		if !seen[event.EventType] {
			names = append(names, event.EventType)
			seen[event.EventType] = true
		}
	}
	entry := s.newObservationLocked(profile, attemptID, "analytics", names, 1)
	for _, event := range events {
		if value, present := event.EventParams["is_worktree"]; event.EventType == "codex_thread_initialized" && present && value == nil {
			entry.IsWorktree = json.RawMessage("null")
			break
		}
	}
	s.enqueueLocked(codexTelemetryJob{profile: profile, body: body, epoch: s.epoch, entry: entry})
}

func (s *CodexTelemetryService) enqueueMetricBatchLocked(batch codexTelemetryMetricBatch) {
	batch.profile.source = batch.source
	entry := s.newObservationLocked(batch.profile, 0, "metrics", batch.names, batch.turns)
	entry.AttemptCount = batch.attempts
	s.enqueueLocked(codexTelemetryJob{profile: batch.profile, body: batch.body, metrics: true, epoch: s.epoch, entry: entry})
}

func (s *CodexTelemetryService) enqueueLocked(job codexTelemetryJob) {
	if !s.enabledLocked() {
		s.finishObservationLocked(job.entry, "cancelled", 0, "telemetry_disabled")
		return
	}
	if s.queueDepth >= codexTelemetryQueueSize {
		s.finishObservationLocked(job.entry, "dropped", 0, "queue_full")
		return
	}
	shard := int(uint64(job.profile.client.localID) % uint64(len(s.queues)))
	s.queues[shard] = append(s.queues[shard], job)
	s.queueDepth++
	s.counters.Queued++
	select {
	case s.wake[shard] <- struct{}{}:
	default:
	}
}

func (s *CodexTelemetryService) worker(shard int) {
	defer s.wg.Done()
	for {
		select {
		case <-s.stop:
			return
		case <-s.wake[shard]:
		}
		for {
			s.mu.Lock()
			if s.stopped || len(s.queues[shard]) == 0 {
				s.mu.Unlock()
				break
			}
			job := s.queues[shard][0]
			s.queues[shard][0] = codexTelemetryJob{}
			s.queues[shard] = s.queues[shard][1:]
			s.queueDepth--
			if job.epoch != s.epoch || !s.enabledLocked() {
				s.finishObservationLocked(job.entry, "cancelled", 0, "telemetry_disabled")
				s.mu.Unlock()
				continue
			}
			ctx := s.epochCtx
			sender := s.sender
			s.mu.Unlock()
			code, reason, status := s.deliverTelemetryJob(ctx, sender, job)
			s.mu.Lock()
			switch {
			case status == "unknown":
				s.finishObservationLocked(job.entry, status, code, reason)
			case status == "cancelled" || job.epoch != s.epoch || !s.enabledLocked():
				s.finishObservationLocked(job.entry, "cancelled", code, "telemetry_disabled")
			case reason != "":
				s.finishObservationLocked(job.entry, "failed", code, reason)
			default:
				s.finishObservationLocked(job.entry, "sent", code, "")
			}
			s.mu.Unlock()
		}
	}
}

func (s *CodexTelemetryService) deliverTelemetryJob(parent context.Context, sender CodexTelemetrySender, job codexTelemetryJob) (int, string, string) {
	s.mu.Lock()
	store := s.store
	s.mu.Unlock()
	if job.persisted == nil {
		code, reason, cancelled := s.send(parent, sender, job)
		if cancelled {
			return code, reason, "cancelled"
		}
		if reason != "" {
			return code, reason, "failed"
		}
		return code, "", "sent"
	}
	if store == nil {
		return 0, "storage_unavailable", "failed"
	}
	batch := job.persisted
	ctx, cancel := context.WithTimeout(parent, codexTelemetryTimeout)
	defer cancel()
	reason := s.hydrateTelemetryTransport(ctx, &job)
	if reason != "" {
		_, _ = store.CompleteBatch(ctx, batch.ID, batch.ClaimID, CodexTelemetryBatchResult{Status: "failed", ErrorCode: reason}, time.Now().UTC())
		return 0, reason, "failed"
	}
	started, err := store.MarkSending(ctx, batch.ID, batch.ClaimID, batch.PolicyEpoch, time.Now().UTC())
	if err != nil {
		return 0, "storage_unavailable", "failed"
	}
	if !started {
		return 0, "telemetry_disabled", "cancelled"
	}
	code, reason, cancelled := s.send(parent, sender, job)
	status := "sent"
	if reason != "" {
		status = "failed"
	}
	if cancelled || (code == 0 && (reason == "timeout" || reason == "transport_error" || reason == "empty_response")) {
		// Once sending began, cancellation cannot prove that the remote endpoint
		// did not accept the body. Never automatically retry an ambiguous send.
		status = "unknown"
		if cancelled {
			reason = "delivery_unknown"
		}
	}
	completeCtx, completeCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer completeCancel()
	_, _ = store.CompleteBatch(completeCtx, batch.ID, batch.ClaimID, CodexTelemetryBatchResult{Status: status, HTTPStatus: code, ErrorCode: reason}, time.Now().UTC())
	return code, reason, status
}

func (s *CodexTelemetryService) send(parent context.Context, sender CodexTelemetrySender, job codexTelemetryJob) (code int, reason string, cancelled bool) {
	ctx, cancel := context.WithTimeout(parent, codexTelemetryTimeout)
	defer cancel()
	ctx = WithHTTPUpstreamRedirectsDisabled(WithHTTPUpstreamProfile(ctx, HTTPUpstreamProfileCodexAuxiliary))
	ctx = codexnative.WithScope(ctx, job.profile.client.nativeHTTPScope)
	url := s.analyticsURL
	if job.metrics {
		url = s.metricsURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(job.body))
	if err != nil {
		return 0, "invalid_endpoint", false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")
	client := job.profile.client
	if job.metrics {
		key := strings.TrimSpace(os.Getenv("CODEX_STATSIG_API_KEY"))
		if key == "" {
			key = codexStatsigAPIKeyDefault
		}
		req.Header.Set("User-Agent", "OTel-OTLP-Exporter-Rust/0.31.0")
		req.Header.Set("statsig-api-key", key)
	} else {
		req.Header.Set("Authorization", "Bearer "+client.accessToken)
		req.Header.Set("Chatgpt-Account-Id", client.accountID)
		req.Header.Set("User-Agent", client.userAgent)
		req.Header.Set("Originator", client.originator)
		if client.version != "" {
			req.Header.Set("Version", client.version)
		}
	}
	var resp *http.Response
	if sender != nil {
		resp, err = sender(ctx, req, job.profile.input, job.metrics)
	} else if s.upstream != nil {
		resp, err = s.upstream.Do(req, client.proxyURL, client.localID, 0)
	} else {
		return 0, "transport_unavailable", false
	}
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		switch {
		case parent.Err() != nil:
			return 0, "telemetry_disabled", true
		case errors.Is(err, context.DeadlineExceeded) || ctx.Err() == context.DeadlineExceeded:
			return 0, "timeout", false
		default:
			return 0, "transport_error", false
		}
	}
	if resp == nil {
		return 0, "empty_response", false
	}
	if resp.Body != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, "upstream_http_error", false
	}
	return resp.StatusCode, "", false
}

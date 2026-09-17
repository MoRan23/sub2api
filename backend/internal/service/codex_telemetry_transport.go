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
	s.enqueueLocked(codexTelemetryJob{profile: profile, body: body, epoch: s.epoch, entry: entry})
}

func (s *CodexTelemetryService) enqueueMetricBatchLocked(batch codexTelemetryMetricBatch) {
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
			code, reason, cancelled := s.send(ctx, sender, job)
			s.mu.Lock()
			switch {
			case cancelled || job.epoch != s.epoch || !s.enabledLocked():
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

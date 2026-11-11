package securityaudit

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
)

// preparedPromptInput belongs to one audit invocation. It owns decoded text,
// never the request body or the decoded document (which may contain images).
// Snapshot metadata is materialized only when an event or Guard needs it.
type preparedPromptInput struct {
	parseOnce     sync.Once
	err           error
	segments      []promptSegment
	combination   *preparedPromptCombination
	combinationMu sync.Mutex
	scanOnce      sync.Once
	scan          string
	fullOnce      sync.Once
	full          PromptSnapshot
	latestOnce    sync.Once
	latest        PromptSnapshot
}

func preparePromptRequest(req Request) Request {
	if req.prepared == nil {
		req.prepared = &preparedPromptInput{}
	}
	return req
}

func (s *PromptService) prepareRequest(req Request) Request { return preparePromptRequest(req) }

// promptEngineRequest preserves isolation for third-party implementations of
// PromptEngine. The built-in engine explicitly accepts immutable borrowed input.
func promptEngineRequest(req Request) Request {
	if req.prepared != nil {
		return req
	}
	return req.Clone()
}

func (p *preparedPromptInput) prepare(req Request) error {
	p.parseOnce.Do(func() {
		var document any
		if err := json.Unmarshal(req.Body, &document); err != nil {
			p.err = errors.New("prompt audit request JSON is invalid")
			return
		}
		p.segments = extractProtocolSegments(req.Protocol, document)
		p.combination = preparePromptCombinationDocument(req.Protocol, document)
	})
	return p.err
}

func (p *preparedPromptInput) scanText(req Request) (string, error) {
	if err := p.prepare(req); err != nil {
		return "", err
	}
	p.scanOnce.Do(func() {
		segments := normalizeSegmentsLatestUserFirst(p.segments)
		if len(segments) == 1 {
			p.scan = segments[0]
		} else if len(segments) > 1 {
			p.scan = segments[0] + promptAuditPrioritySeparator + strings.Join(segments[1:], "\n\n")
		}
	})
	if p.scan == "" {
		return "", ErrNoPromptText
	}
	return p.scan, nil
}

func (p *preparedPromptInput) snapshot(req Request, latestOnly bool) (PromptSnapshot, error) {
	if err := p.prepare(req); err != nil {
		return PromptSnapshot{}, err
	}
	if !p.hasText() {
		return PromptSnapshot{}, ErrNoPromptText
	}
	if latestOnly {
		p.latestOnce.Do(func() {
			p.latest = snapshotFromPromptSegments(req, blockingSegmentsLatestUserAndPreviousOutput(p.segments), "")
		})
		return p.latest, nil
	}
	_, _ = p.scanText(req)
	p.fullOnce.Do(func() {
		p.full = snapshotFromPromptSegments(req, normalizeSegmentsLatestUserFirst(p.segments), p.scan)
	})
	return p.full, nil
}

func (p *preparedPromptInput) hasText() bool {
	for _, segment := range p.segments {
		if strings.TrimSpace(segment.text) != "" {
			return true
		}
	}
	return false
}

package runtimeobs

import (
	"errors"
	"io"
	"runtime/pprof"
	"sync"
	"time"
)

const ProfileTTL = 30 * time.Minute

var ErrProfileDisabled = errors.New("heap profile download is disabled")
var ErrProfileBusy = errors.New("a heap profile download is already in progress")
var ErrInstanceMismatch = errors.New("selected instance is not the current process")

type ProfileState struct {
	Enabled    bool       `json:"enabled"`
	ExpiresAt  *time.Time `json:"expires_at"`
	InProgress bool       `json:"in_progress"`
}

// ProfileGate controls download access, not runtime allocation sampling.
type ProfileGate struct {
	mu      sync.Mutex
	expires time.Time
	busy    bool
}

func (p *ProfileGate) Enable() ProfileState {
	p.mu.Lock()
	p.expires = time.Now().UTC().Add(ProfileTTL)
	p.mu.Unlock()
	return p.State()
}
func (p *ProfileGate) Disable() ProfileState {
	p.mu.Lock()
	p.expires = time.Time{}
	p.mu.Unlock()
	return p.State()
}
func (p *ProfileGate) State() ProfileState {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := ProfileState{Enabled: time.Now().Before(p.expires), InProgress: p.busy}
	if s.Enabled {
		t := p.expires
		s.ExpiresAt = &t
	}
	return s
}

// Acquire must run before writing HTTP headers. Release is called after Write.
func (p *ProfileGate) Acquire(instance string) (func(), error) {
	if instance != processID {
		return nil, ErrInstanceMismatch
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !time.Now().Before(p.expires) {
		return nil, ErrProfileDisabled
	}
	if p.busy {
		return nil, ErrProfileBusy
	}
	p.busy = true
	return func() { p.mu.Lock(); p.busy = false; p.mu.Unlock() }, nil
}

// WriteHeap streams the existing sampled heap profile directly to the writer.
func WriteHeap(w io.Writer) error { return pprof.Lookup("heap").WriteTo(w, 0) }

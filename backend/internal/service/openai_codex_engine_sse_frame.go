package service

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/runtimeobs"
)

const codexEngineRetainedSSEBuffer = 64 << 10

type codexEngineSSESpan struct{ start, end int }

// Offsets survive raw's append growth. Slices are exposed only after the reader
// transfers the complete frame, and remain valid until release.
type codexEngineSSEFrame struct {
	raw, scratch []byte
	firstData    codexEngineSSESpan
	dataCount    int
	event        codexEngineSSESpan
	err          error
	oversized    bool
	bufferGauge  *runtimeobs.SSEBufferGauge
}

func (f *codexEngineSSEFrame) payload() []byte {
	if f.dataCount == 0 {
		return nil
	}
	first := f.raw[f.firstData.start:f.firstData.end]
	if f.dataCount == 1 {
		return first
	}
	return f.scratch
}

func (f *codexEngineSSEFrame) eventName() string {
	return strings.TrimSpace(string(f.raw[f.event.start:f.event.end]))
}

func (f *codexEngineSSEFrame) reset() {
	previousRaw, previousScratch := cap(f.raw), cap(f.scratch)
	if cap(f.raw) > codexEngineRetainedSSEBuffer {
		f.raw = nil
	} else {
		f.raw = f.raw[:0]
	}
	if cap(f.scratch) > codexEngineRetainedSSEBuffer {
		f.scratch = nil
	} else {
		f.scratch = f.scratch[:0]
	}
	f.firstData, f.event = codexEngineSSESpan{}, codexEngineSSESpan{}
	f.dataCount, f.err, f.oversized = 0, nil, false
	f.trackCapacity(previousRaw, previousScratch)
}

func (f *codexEngineSSEFrame) trackCapacity(previousRaw, previousScratch int) {
	if f.bufferGauge != nil && (cap(f.raw) != previousRaw || cap(f.scratch) != previousScratch) {
		f.bufferGauge.Update(cap(f.raw), cap(f.scratch))
	}
}

// Two leases bound the entire pipeline: a frame being written and one being
// read/queued. A buffered output channel alone would allow a third allocation.
type codexEngineSSEReader struct {
	frames    chan *codexEngineSSEFrame
	available chan *codexEngineSSEFrame
	progress  chan struct{}
	done      chan struct{}
	finished  chan struct{}
	stopOnce  sync.Once
	body      io.ReadCloser
	started   time.Time
	lastRead  atomic.Int64
	slots     [2]codexEngineSSEFrame
}

func newCodexEngineSSEReader(body io.ReadCloser, limit int, trackActivity bool) *codexEngineSSEReader {
	r := &codexEngineSSEReader{
		frames: make(chan *codexEngineSSEFrame, 1), available: make(chan *codexEngineSSEFrame, 2),
		done: make(chan struct{}), finished: make(chan struct{}),
		body: body, started: time.Now(),
	}
	if trackActivity {
		r.progress = make(chan struct{}, 1)
	}
	for i := range r.slots {
		r.slots[i].bufferGauge = runtimeobs.Default.TrackSSEBuffers()
		r.available <- &r.slots[i]
	}
	go r.read(limit)
	return r
}

func (r *codexEngineSSEReader) read(limit int) {
	defer close(r.finished)
	defer close(r.frames)
	reader := bufio.NewReader(r.body)
	for {
		var frame *codexEngineSSEFrame
		select {
		case frame = <-r.available:
		case <-r.done:
			return
		}
		r.readFrame(reader, frame, limit)
		err := frame.err
		select {
		case r.frames <- frame:
		case <-r.done:
			return
		}
		if err != nil {
			return
		}
	}
}

func (r *codexEngineSSEReader) readFrame(reader *bufio.Reader, frame *codexEngineSSEFrame, limit int) {
	lineStart := 0
	for {
		piece, err := reader.ReadSlice('\n')
		if len(piece) > limit-len(frame.raw) {
			frame.err = fmt.Errorf("upstream SSE event exceeds configured limit")
			frame.oversized = true
			return
		}
		previousRaw, previousScratch := cap(frame.raw), cap(frame.scratch)
		frame.raw = append(frame.raw, piece...)
		frame.trackCapacity(previousRaw, previousScratch)
		if err == bufio.ErrBufferFull {
			continue
		}
		// A complete line, including comments, refreshes idle activity even
		// when the event itself is still incomplete.
		if r.progress != nil {
			r.lastRead.Store(time.Since(r.started).Nanoseconds())
			select {
			case r.progress <- struct{}{}:
			default:
			}
		}
		line := bytes.TrimRight(frame.raw[lineStart:], "\r\n")
		if bytes.HasPrefix(line, []byte("data:")) {
			start := lineStart + 5
			if start < lineStart+len(line) && frame.raw[start] == ' ' {
				start++
			}
			span := codexEngineSSESpan{start, lineStart + len(line)}
			if frame.dataCount == 0 {
				frame.firstData = span
			} else {
				previousRaw, previousScratch := cap(frame.raw), cap(frame.scratch)
				if frame.dataCount == 1 {
					frame.scratch = append(frame.scratch[:0], frame.raw[frame.firstData.start:frame.firstData.end]...)
				}
				// Match the original adapters: empty leading data fields do not
				// add a newline until the payload contains some bytes.
				if len(frame.scratch) > 0 {
					frame.scratch = append(frame.scratch, '\n')
				}
				frame.scratch = append(frame.scratch, frame.raw[span.start:span.end]...)
				frame.trackCapacity(previousRaw, previousScratch)
			}
			frame.dataCount++
		} else if bytes.HasPrefix(line, []byte("event:")) {
			frame.event = codexEngineSSESpan{lineStart + 6, lineStart + len(line)}
		}
		if len(line) == 0 || err != nil {
			frame.err = err
			return
		}
		lineStart = len(frame.raw)
	}
}

func (r *codexEngineSSEReader) idleRemaining(idle time.Duration) time.Duration {
	return idle - (time.Since(r.started) - time.Duration(r.lastRead.Load()))
}

func (r *codexEngineSSEReader) release(frame *codexEngineSSEFrame) {
	frame.reset()
	select {
	case r.available <- frame:
	case <-r.done:
	}
}

func (r *codexEngineSSEReader) stop() {
	r.stopOnce.Do(func() {
		close(r.done)
		// Close first: a pending ReadSlice must finish before slots can be
		// reset. This also releases terminal streams whose EOF never arrives.
		_ = r.body.Close()
		<-r.finished
		for range r.frames {
		}
		for i := range r.slots {
			r.slots[i].reset()
			r.slots[i].raw, r.slots[i].scratch = nil, nil
			r.slots[i].bufferGauge.Release()
		}
	})
}

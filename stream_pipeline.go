package simdjson

// This file implements the optional pipelined framing behind Reader.SetPipelined.
//
// The default Reader (stream_reader.go) runs framing, validation, and decode on
// the caller's goroutine, interleaved with the blocking io.Reader.Read. When
// the source has non-trivial read latency — a socket, a pipe, a slow file — or
// the per-value decode is substantial, the caller stalls on Read (or blocks the
// worker's next read behind its own decode) even though the two could proceed
// at once. Pipelining moves framing and validation to a worker goroutine that
// runs one batch ahead: while the caller decodes batch N, the worker reads and
// frames batch N+1. The two overlap, so end to end the stream costs roughly
// max(read+frame, decode) per batch instead of their sum.
//
// A batch is a run of complete top-level values that share one buffer. The
// worker frames as many whole values as fit, validates each, records their
// extents, and hands the batch off; a value straddling the batch boundary has
// only its partial tail copied into the head of the next batch's buffer. Each
// batch buffer stays alive for the whole batch, so the Reader can alias it into
// buf/valStart/valEnd and every reader method (Bytes, Cursor, DecodeFrom,
// DecodeNext, InputOffset) works unchanged under the same "valid until the next
// Next" contract. Buffers recycle through a two-slot free list once the caller
// advances past them, so steady state allocates nothing.

import (
	"errors"
	"fmt"
	"io"
	"unsafe"
)

// extent locates one framed, validated value inside a batch buffer.
type extent struct {
	start int
	end   int
}

// batch is one worker-produced unit handed to the consumer: a buffer, the
// values framed within it, and the input offset of buffer byte zero. err
// travels with the batch and is reported only after the caller has consumed
// every value that preceded it, matching the io.Reader convention that data
// arriving with an error is valid and comes first.
type batch struct {
	buf     []byte
	vals    []extent
	baseOff int64 // input offset of buf[0], for InputOffset and error offsets
	err     error // terminal error, delivered after this batch's values
}

// pipeline is the Reader-side handle to the worker: the channels that carry
// batches and recycle buffers, plus the batch currently being consumed.
type pipeline struct {
	ch   chan batch    // cap-1: the worker runs at most one batch ahead
	free chan []byte   // cap-2 free list of batch buffers to reuse
	done chan struct{} // closed by close to stop the worker

	cur     batch // batch being consumed
	valIdx  int   // 1-based index of the current value within cur.vals
	tailErr error // terminal error to report once cur is drained
	closed  bool
}

// errCleanEOF marks a batch whose stream ended without error, distinguishing a
// clean end (Err stays nil) from a real terminal error.
var errCleanEOF = errors.New("simdjson: clean end of stream")

// newPipeline starts the worker over in with the given batch size and value
// bound. size is the Reader's buffer length; a floor keeps batches useful.
func newPipeline(in io.Reader, size, maxValue int) *pipeline {
	if size < 512 {
		size = defaultReaderSize
	}
	p := &pipeline{
		ch:   make(chan batch, 1),
		free: make(chan []byte, 2),
		done: make(chan struct{}),
	}
	// Seed the free list so the worker can begin without allocating.
	p.free <- make([]byte, size)
	p.free <- make([]byte, size)
	w := &pipelineWorker{
		in:       in,
		out:      p.ch,
		free:     p.free,
		done:     p.done,
		batchCap: size,
		maxValue: maxValue,
	}
	go w.run()
	return p
}

// pipeNext advances a pipelined Reader to the next value, aliasing the current
// batch buffer into buf/valStart/valEnd so every reader method sees the value
// exactly as the sequential path leaves it. It returns false at end of stream
// or on error; Err distinguishes the two.
func (r *Reader) pipeNext() bool {
	if r.err != nil {
		return false
	}
	p := r.pipe
	r.hasValue = false
	for {
		if p.valIdx < len(p.cur.vals) {
			p.valIdx++
			e := p.cur.vals[p.valIdx-1]
			r.buf = p.cur.buf
			r.valStart, r.valEnd = e.start, e.end
			r.consumed = p.cur.baseOff
			r.hasValue = true
			return true
		}
		// The current batch is drained. Surface its terminal error, if any,
		// now that every value ahead of it has been delivered.
		if p.tailErr != nil {
			if p.tailErr != errCleanEOF {
				r.err = p.tailErr
			}
			p.tailErr = nil
			return false
		}
		if !p.recv() {
			return false
		}
	}
}

// recv returns the current batch's buffer to the free list and receives the
// next batch. It reports false when the channel is closed with no batch, which
// happens after close or after the worker's final error batch is consumed.
func (p *pipeline) recv() bool {
	if p.cur.buf != nil {
		// Recycle without blocking: the free list has room for both live
		// buffers, but a closed or abandoned reader must never block here.
		select {
		case p.free <- p.cur.buf[:cap(p.cur.buf)]:
		default:
		}
		p.cur = batch{}
	}
	b, ok := <-p.ch
	if !ok {
		return false
	}
	p.cur = b
	p.valIdx = 0
	p.tailErr = b.err
	return true
}

// close stops the worker goroutine and drains any in-flight batch so a final
// send cannot leave the worker blocked. It is idempotent.
func (p *pipeline) close() {
	if p.closed {
		return
	}
	p.closed = true
	close(p.done)
	// The worker may be committed to a send into the cap-1 channel that raced
	// past its done check; drain in the background so it unblocks and exits,
	// then closes the channel on return.
	ch := p.ch
	go func() {
		for range ch {
		}
	}()
}

// pipelineWorker frames and validates batches on its own goroutine. All of its
// fields are owned by that goroutine; only completed batches cross to the
// consumer, through the channel.
type pipelineWorker struct {
	in       io.Reader
	out      chan<- batch
	free     chan []byte
	done     <-chan struct{}
	batchCap int
	maxValue int

	baseOff int64 // input offset of the next batch's buf[0]

	// Per-batch fill state, reset for every batch:
	buf    []byte
	filled int // valid bytes in buf
	framed int // bytes already framed into vals (start of the unframed region)

	fr         valueFrame // resumable frame of the value in progress
	framing    bool       // fr is mid-value
	frameStart int        // buf offset where fr's current value began
	vals       []extent
	batchErr   error // terminal error discovered while framing this batch
}

// run produces batches until the input ends, an error occurs, or close signals
// done, then closes the output channel so the consumer's receive unblocks.
func (w *pipelineWorker) run() {
	defer close(w.out)

	// carry holds a partial value's bytes that straddled the previous batch's
	// tail; it seeds the head of each new batch's buffer.
	var carry []byte
	for {
		buf, ok := w.getBuf(len(carry))
		if !ok {
			return // close signalled while we waited for a buffer.
		}
		w.startBatch(buf, carry)

		tail, err := w.fill()
		b := batch{buf: w.buf, vals: w.vals, baseOff: w.baseOff, err: w.batchErr}
		if !w.send(b) {
			return
		}
		if err != nil {
			return // terminal: clean EOF, read error, or invalid value.
		}
		// The tail (an unfinished value plus trailing whitespace) carries into
		// the next batch. baseOff advances by the bytes this batch consumed and
		// did not carry forward.
		w.baseOff += int64(tail)
		carry = append(carry[:0], w.buf[tail:w.filled]...)
	}
}

// startBatch resets per-batch state and seeds the buffer with the carried tail.
func (w *pipelineWorker) startBatch(buf, carry []byte) {
	w.buf = buf
	w.filled = copy(buf, carry)
	w.framed = 0
	w.framing = false
	w.frameStart = 0
	w.vals = nil
	w.batchErr = nil
}

// getBuf takes a buffer from the free list, growing it when the carried tail
// (which must fit before any new bytes are read) approaches a batch. It reports
// false only when close signals before a buffer is available.
func (w *pipelineWorker) getBuf(need int) ([]byte, bool) {
	select {
	case buf := <-w.free:
		if cap(buf) < need+512 {
			buf = make([]byte, need+w.batchCap)
		}
		return buf[:cap(buf)], true
	case <-w.done:
		return nil, false
	}
}

// send hands a batch to the consumer, unblocking if close signals first.
func (w *pipelineWorker) send(b batch) bool {
	select {
	case w.out <- b:
		return true
	case <-w.done:
		return false
	}
}

// fill reads into w.buf (already holding the carried prefix) until the buffer
// holds at least one complete value, the buffer is full, or the input ends,
// framing and validating every complete top-level value along the way. It
// returns the offset just past the last complete value (the tail to carry
// forward) and a terminal error when the stream ends here.
func (w *pipelineWorker) fill() (int, error) {
	eof := false
	short := false // the last read returned fewer bytes than requested
	var readErr error
	for {
		if !eof && w.filled < len(w.buf) {
			want := len(w.buf) - w.filled
			m, err := w.in.Read(w.buf[w.filled:])
			w.filled += m
			short = m < want
			switch {
			case err == io.EOF:
				eof = true
			case err != nil:
				eof = true
				readErr = err
			}
		}

		tail, grow := w.frame(eof)
		if w.batchErr != nil {
			return tail, w.batchErr // an invalid value stops the stream here.
		}
		if grow {
			// One value is larger than the batch and not yet complete: grow
			// this batch's own buffer (never the shared free list) and read on.
			if w.maxValue > 0 && w.filled-tail > w.maxValue {
				w.batchErr = fmt.Errorf("simdjson: value at input offset %d exceeds the %d byte limit",
					w.baseOff+int64(tail), w.maxValue)
				return tail, w.batchErr
			}
			if eof {
				return w.finishTruncated(tail, readErr)
			}
			grown := make([]byte, len(w.buf)*2)
			copy(grown, w.buf[:w.filled])
			w.buf = grown
			continue
		}
		if eof {
			return w.finishTail(tail, readErr)
		}
		// Hand this batch off once the buffer is full, or once complete values
		// are ready and the source drained for now (a short read). Filling the
		// buffer before handing off keeps batches large, so the two-slot free
		// list suffices and steady state allocates nothing; a short read means
		// no more bytes are waiting, so decoding what we have overlaps the next
		// read rather than idling until the buffer is full.
		if w.filled == len(w.buf) || (short && len(w.vals) > 0) {
			return tail, nil
		}
		// No complete value yet, or the source may have more waiting and there
		// is room: read again. A full buffer with no value took the grow path,
		// so we always make progress.
	}
}

// frame walks the unframed region of w.buf, appending an extent for every
// complete top-level value and leaving the frame mid-value when the buffer edge
// splits one. It returns the tail offset (just past the last complete value)
// and grow=true when a single value has filled the buffer without completing.
//
// It mirrors Reader.Next exactly: validValueFast is the authority on a value's
// extent, and the resumable frame is only a "have we buffered enough" gate.
// This matters at concatenated scalars — "00" is two values 0 and 0, because a
// top-level number ends at its first complete token — where the framer's number
// scan would greedily run both digits together. Deferring the extent to the
// validator keeps the pipelined and sequential framings identical.
func (w *pipelineWorker) frame(eof bool) (tail int, grow bool) {
	for {
		i := skipSpace(w.buf[:w.filled], w.framed)
		if i >= w.filled {
			w.framed = i
			return i, false
		}
		if !w.framing {
			w.fr = valueFrame{}
			w.fr.init(w.buf[i])
			w.frameStart = i
			w.framing = true
		}
		done := w.fr.scan(w.buf, w.frameStart, w.filled)
		// Validate once the value is fully buffered (framer done) or the input
		// ended (a trailing scalar at the edge is then complete). Until then the
		// value may continue in unread bytes, so leave it for the next read.
		if !done && !eof {
			if w.filled == len(w.buf) && i == 0 {
				return i, true // this value alone fills the batch: grow.
			}
			w.framed = i
			return i, false
		}
		window := w.buf[:w.filled]
		base := unsafe.Pointer(unsafe.SliceData(window))
		vend, vok := validValueFast(window, base, w.filled, i, window[i], 0)
		if !vok {
			// Fully buffered (framer done) or EOF, yet invalid: a hard error.
			// Diagnose the framed extent so the reason is stable regardless of
			// trailing input, matching Reader.Next.
			verr := Validate(w.buf[i : w.frameStart+w.fr.framed])
			if verr == nil {
				verr = io.ErrUnexpectedEOF
			}
			w.batchErr = fmt.Errorf("simdjson: invalid value at input offset %d: %w",
				w.baseOff+int64(i), verr)
			return i, false
		}
		if vend == w.filled && !eof {
			// A number or literal ending exactly at the buffer edge may continue
			// in unread input; wait for the confirming byte rather than split it.
			// If it alone fills the batch, grow so the confirming byte has room
			// (fill hands the batch off first when earlier values are present).
			if w.filled == len(w.buf) && len(w.vals) == 0 {
				return i, true
			}
			w.framed = i
			return i, false
		}
		if w.maxValue > 0 && vend-i > w.maxValue {
			w.batchErr = fmt.Errorf("simdjson: value at input offset %d exceeds the %d byte limit",
				w.baseOff+int64(i), w.maxValue)
			return i, false
		}
		w.vals = append(w.vals, extent{start: i, end: vend})
		w.framed = vend
		w.framing = false
	}
}

// finishTail is fill's clean end-of-input case: everything complete has been
// framed, so classify the terminal error to travel with the batch.
func (w *pipelineWorker) finishTail(tail int, readErr error) (int, error) {
	if w.batchErr != nil {
		return tail, w.batchErr
	}
	// Any bytes past the last value must be whitespace; a nonspace tail at EOF
	// is a value the framer could not complete.
	rest := skipSpace(w.buf[:w.filled], tail)
	if rest < w.filled {
		return w.finishTruncated(rest, readErr)
	}
	if readErr != nil {
		w.batchErr = readErr
	} else {
		w.batchErr = errCleanEOF
	}
	return w.filled, w.batchErr
}

// finishTruncated reports a value cut off by end of input, matching the
// sequential Reader's diagnosis of the framed extent.
func (w *pipelineWorker) finishTruncated(at int, readErr error) (int, error) {
	verr := Validate(w.buf[at:w.filled])
	if verr == nil {
		verr = io.ErrUnexpectedEOF
	}
	if readErr != nil {
		verr = readErr
	}
	w.batchErr = fmt.Errorf("simdjson: invalid value at input offset %d: %w",
		w.baseOff+int64(at), verr)
	return at, w.batchErr
}

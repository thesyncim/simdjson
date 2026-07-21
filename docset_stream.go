package simdjson

// This file implements bulk stream ingestion: ReadFrom fuses the streaming
// Reader's document framing with the DocSet's arena-resident indexing. Bytes
// are read in large blocks straight into the source arena's spare capacity,
// each document is framed and indexed where it landed, and committing a
// document only extends the arena over storage already written in place — no
// intermediate buffer, no per-document copy, and on the hot path a single
// pass that validates, indexes, and locates the document's end together.
//
// The current source chunk plays both roles at once — committed arena on the
// left, read buffer on the right:
//
//	0             len(srcChunk)    pos              bufEnd            cap
//	|  committed documents  | ws  |  read-ahead      |  unfilled       |
//	                               ^ next document starts here
//
// The committed length only ever grows over bytes already framed and
// indexed, so a failed document needs no rollback: its bytes sit in the
// uncommitted read-ahead and are simply never committed. When a document
// straddles the chunk's end, only its uncommitted prefix is copied into the
// next chunk (fill); committed documents never move.

import (
	"fmt"
	"io"
	"unsafe"
)

// docSetStream carries ReadFrom's progress. Read-ahead lives in the current
// source chunk's spare capacity, between the committed length and bufEnd, so
// a failed document needs no rollback: its bytes were never committed.
type docSetStream struct {
	s      *DocSet
	r      io.Reader
	bufEnd int   // filled bytes within the current chunk's capacity
	total  int64 // bytes read from r so far
	eof    bool  // no more input will arrive
	// readErr is a non-EOF read error, surfaced once the bytes that arrived
	// with or before it have been consumed; io.Reader delivers data and
	// error together and the data comes first.
	readErr error

	// Commit statistics for this call steer the two adaptive policies below:
	// srcChunkMax sizes chunk rolls from the mean committed document, and
	// extendWalk judges entry headroom from the mean entry count.
	docs       int64 // documents committed by this call
	docBytes   int64 // their total source bytes
	docEntries int64 // their total index entries
}

// record notes one committed document in the call's running statistics.
func (d *docSetStream) record(bytes, entries int) {
	d.docs++
	d.docBytes += int64(bytes)
	d.docEntries += int64(entries)
}

// offset translates a chunk position into this call's input offset.
func (d *docSetStream) offset(p int) int64 {
	return d.total - int64(d.bufEnd-p)
}

// fill reads more input into the current chunk's spare capacity, first
// rolling to a fresh chunk when none remains. A roll moves only the
// uncommitted bytes at or after *keep — at most one partial document — so
// committed documents never move; *keep is rewritten to the bytes' new
// position. The partial's length doubles into the new capacity, so a
// document larger than the chunk bound still ingests with amortized-linear
// copying; the bound itself adapts to the stream's document sizes
// (srcChunkMax). fill reports false when no new bytes will ever arrive.
func (d *docSetStream) fill(keep *int) bool {
	if d.eof {
		return false
	}
	s := d.s
	if d.bufEnd == cap(s.srcChunk) {
		partial := d.bufEnd - *keep
		chunk := make([]byte, 0, docSetChunkCap(cap(s.srcChunk), 2*partial, docSetMinSrcChunk, d.srcChunkMax()))
		chunk = append(chunk, s.srcChunk[*keep:d.bufEnd]...)
		s.srcChunk = chunk[:0]
		d.bufEnd = partial
		*keep = 0
	}
	for {
		n, err := d.r.Read(s.srcChunk[d.bufEnd:cap(s.srcChunk)])
		d.bufEnd += n
		d.total += int64(n)
		switch {
		case err == io.EOF:
			d.eof = true
			return n > 0
		case err != nil:
			d.eof = true
			d.readErr = err
			return n > 0
		case n > 0:
			return true
		}
	}
}

// ReadFrom appends every JSON document in r to the set, in order, until the
// input ends. Documents are framed exactly like the streaming Reader's
// values: NDJSON, other whitespace separation, and direct concatenation all
// work. Bytes are read in large blocks straight into the set's source arena
// and every document is validated and indexed in place under the set's
// Options, exactly as Append would, with no intermediate buffering: a
// document straddling an arena chunk boundary costs one copy of its partial
// prefix, and everything else lands in place. ReadFrom implements
// io.ReaderFrom: it returns the number of bytes read from r; the number of
// documents appended is the change in Len.
//
// A failure — an invalid document, a read error, or input ending
// mid-document — keeps every fully ingested document, discards the failed
// one, and leaves the set valid for further Append or ReadFrom calls; bytes
// read past the failure are discarded with it. The error carries the
// document's byte offset within this call's input. Like the Reader, a read
// error surfaces only after the documents that arrived before it.
func (s *DocSet) ReadFrom(r io.Reader) (int64, error) {
	d := docSetStream{s: s, r: r, bufEnd: len(s.srcChunk)}
	pos := len(s.srcChunk)
	for {
		// Skip inter-document whitespace, refilling as needed, to the next
		// document's first significant byte.
		pos = skipSpace(s.srcChunk[:d.bufEnd], pos)
		if pos == d.bufEnd {
			if !d.fill(&pos) {
				return d.total, d.readErr
			}
			continue
		}
		if err := s.readDoc(&d, &pos); err != nil {
			return d.total, err
		}
	}
}

// docSetPrefixWindow bounds the one-pass walk while more input can still
// land in the current chunk. A document that outgrows the window has an
// unknown extent: walking on risks doing full-document work only to find
// truncation at the buffered edge, so the walk stops and the document
// declines to the framing slow path, whose exact-extent buildDoc routes
// through the stage-1 bitmap engine Append uses for large documents. Once
// the buffer cannot grow in place, that risk is bounded and the walk runs
// over every buffered byte instead (extendWalk), keeping large fully
// buffered documents on the one-pass hot path.
const docSetPrefixWindow = validBitmapMinBytes

// docSetMaxStreamSrcChunk caps srcChunkMax's adaptive raise. It bounds both
// the retention granularity of a stream's source arena and the copying a
// single roll can perform, while keeping the roll's abandoned tail — at most
// one mean-sized document per chunk — a small fraction of the whole.
const docSetMaxStreamSrcChunk = 8 << 20

// srcChunkMax returns the size bound for the next source-chunk roll. The
// static bound serves mixed and small-document streams unchanged; a stream
// of large documents raises it toward eight times its mean committed
// document, so one chunk holds several documents and each roll abandons at
// most one document's worth of tail instead of nearly a whole chunk.
func (d *docSetStream) srcChunkMax() int {
	bound := int64(docSetMaxSrcChunk)
	if d.docs > 0 {
		if t := 8 * (d.docBytes / d.docs); t > bound {
			bound = min(t, docSetMaxStreamSrcChunk)
		}
	}
	return int(bound)
}

// extendWalk reports whether readDoc's one-pass walk may run past
// docSetPrefixWindow, over every buffered byte. Extension pays off exactly
// when the buffered bytes plausibly hold the whole document: once the chunk
// is full or the input has ended, no more bytes can land in place, so a
// document that will ever complete in this buffer already has, and walking
// it in one pass saves the slow lane's separate framing scan. While the
// buffer can still grow, the window stays capped — the walk would routinely
// reach the buffered edge mid-document and the work to that edge is thrown
// away. The entry-headroom test keeps streams of entry-dense documents off
// the extension for the same reason, using twice the stream's mean entry
// count as the bar for the entry chunk's free tail.
func (d *docSetStream) extendWalk() bool {
	if !d.eof && d.bufEnd < cap(d.s.srcChunk) {
		return false
	}
	if d.docs == 0 {
		return true
	}
	free := cap(d.s.entryChunk) - len(d.s.entryChunk)
	return int64(free) >= 2*(d.docEntries/d.docs)
}

// readDoc ingests the document whose first significant byte is at *pos,
// advancing *pos past it on success. The hot path is one pass: the fast tape
// walker validates, indexes, and locates the document's end directly in the
// buffered arena bytes, and the commit extends the source and entry chunks
// over that storage. The walker declining — truncation, a syntax error,
// exhausted entry storage, deep nesting, or a document past the walk window
// (docSetPrefixWindow, extendWalk) — falls to readDocSlow, which settles
// every such case.
func (s *DocSet) readDoc(d *docSetStream, pos *int) error {
	start := *pos
	for {
		windowEnd := d.bufEnd
		if windowEnd-start > docSetPrefixWindow && !d.extendWalk() {
			windowEnd = start + docSetPrefixWindow
		}
		index, end, ok := s.buildDocPrefix(start, windowEnd)
		if !ok {
			return s.readDocSlow(d, start, pos)
		}
		// A root number ending exactly at the filled edge may continue in
		// unread input, so it commits only once a byte follows it or the
		// input ends. Every other root ends on its closing or final byte.
		// start sits past the committed length, so the byte is read through
		// the filled extent.
		if c := s.srcChunk[start:d.bufEnd][0]; c == '-' || isDigit(c) {
			if end == d.bufEnd && !d.eof {
				// Read the confirming byte and re-walk: a fill can roll the
				// chunk, so the built index must not outlive it. A failed
				// fill sets eof and the re-walk commits the same extent.
				d.fill(&start)
				continue
			}
			if end == windowEnd && windowEnd < d.bufEnd {
				// The window cap cut the number short of buffered bytes.
				return s.readDocSlow(d, start, pos)
			}
		}
		s.entryChunk = s.entryChunk[:len(s.entryChunk)+len(index.entries)]
		s.srcChunk = s.srcChunk[:end]
		s.docs = append(s.docs, index)
		d.record(end-start, len(index.entries))
		*pos = end
		return nil
	}
}

// readDocSlow ingests one document through the resumable structural framer:
// it locates the document's exact extent across refills, then hands that
// extent to buildDoc, which behaves exactly as it does under Append — same
// builder routing (including the stage-1 bitmap engine for large documents),
// same diagnostic errors, same spill handling, same atomicity. Framing is
// structure-only and skips string interiors with the vector scanner, so a
// document spanning K refills is scanned once, not K times.
func (s *DocSet) readDocSlow(d *docSetStream, start int, pos *int) error {
	var fr valueFrame
	fr.init(s.srcChunk[start:d.bufEnd][0])
	framed := false
	for {
		framed = fr.scan(s.srcChunk[:d.bufEnd], start, d.bufEnd)
		if framed || !d.fill(&start) {
			break
		}
	}
	// When the input ends mid-document the framer has consumed every
	// buffered byte: a root number legitimately ends there, and anything
	// else is truncation, which buildDoc rejects with its exact diagnosis.
	end := start + fr.framed
	index, err := s.buildDoc(s.srcChunk[start:end:end])
	if err != nil {
		if !framed && d.readErr != nil {
			// The stream broke mid-document; the read failure, not the
			// truncated bytes it left behind, is the cause worth reporting.
			err = d.readErr
		}
		return fmt.Errorf("simdjson: invalid document at input offset %d: %w", d.offset(start), err)
	}
	s.srcChunk = s.srcChunk[:end]
	s.docs = append(s.docs, index)
	d.record(end-start, len(index.entries))
	*pos = end
	return nil
}

// buildDocPrefix walks one document beginning at start against the buffered
// window ending at windowEnd, building its index into the entry chunk's free
// tail without committing anything, so a discarded result leaves no trace.
// On success it returns the built index and the document's exclusive end in
// chunk coordinates. It reports !ok whenever the fast walker declines;
// decline carries no cause because the slow path settles every case itself.
// The walker is the same one BuildIndexOptions runs first on documents this
// size, stopped at the root value's end instead of demanding end of input,
// so accepted documents index byte-identically to Append.
func (s *DocSet) buildDocPrefix(start, windowEnd int) (Index, int, bool) {
	if uint64(windowEnd-start) > uint64(^uint32(0)) {
		// Entry offsets are uint32, exactly as buildIndexOptions enforces; a
		// window past their reach cannot be walked. The slow lane frames the
		// document's true extent and reports its own error.
		return Index{}, 0, false
	}
	if cap(s.entryChunk) == 0 {
		s.entryChunk = make([]IndexEntry, 0, docSetMinEntryChunk)
	}
	used := len(s.entryChunk)
	free := s.entryChunk[used:]
	window := s.srcChunk[start:windowEnd:windowEnd]
	maxDepth := s.Options.MaxDepth
	if maxDepth <= 0 {
		maxDepth = defaultMaxDepth
	}
	b := tapeBuilder{
		src:      window,
		base:     byteSourceOf(window).pointerAt(0),
		entries:  free[:0],
		parent:   noTapeParent,
		maxDepth: maxDepth,
	}
	if b.walkFast() != tapeParseOK {
		return Index{}, 0, false
	}
	n := len(b.entries)
	if n == 0 || unsafe.SliceData(b.entries) != unsafe.SliceData(free) {
		// The walker builds by reslicing the storage it was handed, and a
		// complete document always emits at least one entry. If either
		// invariant ever broke, committing would expose garbage, so fail
		// closed into the slow path, which owns its storage end to end.
		return Index{}, 0, false
	}
	end := b.i
	index := Index{src: window[:end:end], entries: free[:n:n]}
	if s.Options.HashKeys {
		enrichKeyHashes(&index)
	}
	return index, start + end, true
}

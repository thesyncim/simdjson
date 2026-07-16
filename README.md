# simdjson

[![ci](https://github.com/thesyncim/simdjson/actions/workflows/ci.yml/badge.svg)](https://github.com/thesyncim/simdjson/actions/workflows/ci.yml)

Strict, high-performance JSON for Go, written entirely in Go. `Unmarshal` and
`Marshal` are drop-in replacements for their `encoding/json` counterparts;
compiled per-type codecs, structural indexes, and vector kernels built on Go's
experimental `simd/archsimd` package supply the speed. The root module has no
third-party dependencies, generated codecs, assembly, C, `go:linkname`, or
runtime map-layout assumptions.

> [!IMPORTANT]
> **Go tip is required.** simdjson does not currently build with a stable Go
> release. Any current Go tip toolchain can be used; the exact Go commit shown
> in the benchmark section is only a reproducibility pin. Set
> `GOEXPERIMENT=simd` to enable the Go-native SIMD kernels. The same Go tip
> compiler builds portable fallbacks when the experiment is omitted.

[Install](#install) | [Quick start](#quick-start) | [Usage](#usage) | [Performance](#performance) | [Contracts](#compatibility-and-contracts) | [SIMD package](#simd-package) | [Reproduce](#reproduce-benchmarks)

## Install

Install any current Go tip toolchain. `gotip` is the simplest option:

```sh
go install golang.org/dl/gotip@latest
gotip download
```

Then, from your module:

```sh
gotip get github.com/thesyncim/simdjson@latest
```

Enable the SIMD kernels when building or testing:

```sh
GOEXPERIMENT=simd gotip build ./...
```

Without `GOEXPERIMENT=simd`, simdjson keeps the same API and behavior while
using its portable Go implementations. To build the exact pinned compiler used
for published benchmarks, run `./scripts/bootstrap-gotip.sh "$HOME/sdk/simdjson-gotip"`.

## Quick start

```go
import "github.com/thesyncim/simdjson"

type Event struct {
	ID   int      `json:"id"`
	Name string   `json:"name"`
	Tags []string `json:"tags"`
}

var event Event
if err := simdjson.Unmarshal(data, &event); err != nil {
	return err
}

out, err := simdjson.Marshal(&event)
```

Both functions behave like `encoding/json` — same tags, same merge semantics,
same output bytes — and cache one compiled plan per type for the life of the
process. Everything below is opt-in.

## Usage

Expand the task you care about. Every snippet compiles against the current API.

<details>
<summary><b>Decode into structs, compiled once</b></summary>

Hot paths should compile the decoder once and reuse it; a `Decoder` is
immutable and safe for concurrent use. Options select strictness, ownership,
and merge-versus-replace semantics.

```go
decoder, err := simdjson.CompileDecoder[Event](simdjson.DecoderOptions{
	DisallowUnknownFields: true, // reject keys the struct does not declare
	CaseSensitive:         true, // skip the case-insensitive field fallback
})
if err != nil {
	return err
}

var event Event
if err := decoder.Decode(data, &event); err != nil {
	var decodeErr *simdjson.DecodeError
	if errors.As(err, &decodeErr) {
		fmt.Println(decodeErr.Path, decodeErr.Offset) // e.g. "items[3].id" 42
	}
	return err
}
```

`DecodeError` carries the byte offset and a typed path such as
`items[3].scores[1]`; the path is built only when an error unwinds, so
successful decodes pay nothing for it. `DecoderOptions.Replace` resets
destination state the document does not mention — the right mode for
destinations reused across decodes; the default merges like `encoding/json`.
`Decoder.DecodeArray` decodes a top-level array while reusing caller-provided
slice capacity, and `Decoder.DecodePrefix` decodes one value from the front of
a larger buffer.

</details>

<details>
<summary><b>Zero-copy decode</b></summary>

`ZeroCopy` aliases unescaped strings directly into the source buffer instead
of copying them.

```go
decoder, err := simdjson.CompileDecoder[Event](simdjson.DecoderOptions{
	ZeroCopy: true,
})
if err != nil {
	return err
}

var event Event
if err := decoder.Decode(src, &event); err != nil {
	return err
}
// event's strings alias src: keep src alive and unmodified
// for as long as event is in use.
```

Without `ZeroCopy`, results are independent of `src`: decoded strings alias at
most one private copy of the input, so retaining any decoded string retains
that copy. `Options.ZeroCopy` offers the same choice for the ordered `Parse`
tree.

</details>

<details>
<summary><b>Dynamic values: <code>any</code> trees and ordered trees</b></summary>

`Unmarshal` into a `*any` produces the standard Go JSON shapes through a
dedicated one-pass builder, with no intermediate tree; `Parse` produces an
ordered, on-demand `Value` when member order matters.
Parsing builds only the structural index — scalars decode and containers
materialize when they are actually read, so a document consumed in part never
pays for the whole. A `Value` keeps its backing storage alive, so it stays
usable after the source slice is gone.

```go
var value any
if err := simdjson.Unmarshal(src, &value); err != nil {
	return err
}
object := value.(map[string]any)
fmt.Println(object["scores"].([]any)[1]) // 2.5

// json.Number instead of float64:
decoder, err := simdjson.CompileDecoder[any](simdjson.DecoderOptions{UseNumber: true})
if err != nil {
	return err
}
if err := decoder.Decode(src, &value); err != nil {
	return err
}

// Ordered on-demand value that preserves member order:
root, err := simdjson.Parse(src)
if err != nil {
	return err
}
user, _ := root.Get("user")
name, _ := user.Get("name")
text, _ := name.Text()
fmt.Println(text) // ada
```

</details>

<details>
<summary><b>Stream NDJSON in both directions</b></summary>

`Writer` streams NDJSON or concatenated values through one reused buffer.
`Reader` iterates validated top-level values from any `io.Reader`;
`DecodeNext` fuses iteration and typed decoding into one pass — the decoder
itself finds the value boundary — and is the fastest way through a typed
stream. Steady-state streaming in both directions performs no per-value
allocations.

```go
codec, err := simdjson.CompileCodec[Event](simdjson.CodecOptions{})
if err != nil {
	return err
}

w := simdjson.NewWriter(out)
for i := range events {
	codec.EncodeTo(w, &events[i])
	w.Newline()
}
if err := w.Close(); err != nil {
	return err
}

r := simdjson.NewReader(in)
var event Event
for simdjson.DecodeNext(r, codec.Decoder(), &event) {
	// use event
}
return r.Err()
```

`CompileCodec` bundles both directions for one type plus a per-codec output
size hint. For hand-built documents, `Writer` exposes token methods that track
container state and refuse to emit malformed JSON:

```go
w := simdjson.NewWriter(out)
w.BeginObject()
w.Key("id")
w.Int(7)
w.Key("ok")
w.Bool(true)
w.EndObject()
return w.Close()
```

For dynamic single-pass consumption, `Reader.Cursor` walks the current value
forward without building any tree or index: scalars parse on demand through
the same kernels the compiled decoder uses, `Skip` hops unwanted subtrees
structurally, and a full stream costs two allocations total rather than
allocations per value. The contract is forward-only and explicit —
`BeginObject`/`NextField`, `BeginArray`/`NextElement`, `Skip`, `Finish`:

```go
r := simdjson.NewReader(in)
for r.Next() {
	c := r.Cursor()
	if err := c.BeginObject(); err != nil {
		return err
	}
	for {
		key, ok, err := c.NextField()
		if err != nil || !ok {
			break
		}
		if key == "id" {
			id, _ := c.Int64()
			// use id
		} else {
			c.Skip()
		}
	}
}
return r.Err()
```

`Reader.Bytes`, zero-copy decodes, and cursor strings alias the rolling
buffer and stay valid only until the next `Next`; `SetMaxValueBytes` bounds
buffer growth on untrusted input.

</details>

<details>
<summary><b>Encode into a reused buffer</b></summary>

`AppendJSON` appends to a caller-owned buffer, so steady-state encoding does
not allocate. An `Encoder` is immutable and safe for concurrent use.

```go
encoder, err := simdjson.CompileEncoder[Event](simdjson.EncoderOptions{})
if err != nil {
	return err
}

buf := make([]byte, 0, 4096)
for i := range events {
	buf, err = encoder.AppendJSON(buf[:0], &events[i])
	if err != nil {
		return err
	}
	fmt.Println(string(buf)) // use buf before the next iteration reuses it
}
```

Output matches `encoding/json` byte for byte: compact, HTML-escaped (opt out
with `DisableHTMLEscaping`), U+2028/U+2029 escaped, invalid UTF-8 replaced.
`omitempty` and `string` tag options, `json.Marshaler`, `encoding.TextMarshaler`,
and `time.Time` are supported. Unrepresentable values (NaN, infinities,
malformed `json.Number`) return an `EncodeError` with a typed path.

</details>

<details>
<summary><b>Hand-written hooks for the last few percent</b></summary>

The compiled decoder and encoder already outrun generated code and JIT
libraries, but a hot type can shed the remaining per-field dispatch by
implementing `UnmarshalSimdJSON(*DecodeCursor) error` or `MarshalSimdJSON(Appender)
Appender` — the simdjson-native counterparts of `json.Unmarshaler` and
`json.Marshaler`. A `DecodeCursor` exposes the same SIMD scalar kernels and object
and array framing the decoder itself uses; an `Appender` is a by-value builder
whose output buffer stays in registers. These read ~12–15% and write ~17–27%
faster than the (already fastest-in-Go) compiled path, at zero allocation, and
are entirely opt-in: a type without the methods is unaffected.

```go
func (e Event) MarshalSimdJSON(w simdjson.Appender) simdjson.Appender {
	w = w.RawByte('{').Raw(`"id":`).Int(int64(e.ID))
	w = w.Raw(`,"name":`).String(e.Name)
	return w.Raw(`,"active":`).Bool(e.Active).RawByte('}')
}
```

Decode bodies read fields through the `DecodeCursor` (`BeginObject`, `Field`/`NextField`,
`Int64`/`String`/`Bool`/…, `Skip`); see the `DecodeCursor` and `Appender` type
documentation. A hook body must not retain the `DecodeCursor` or `Appender` past the
call, and an encode hook must emit valid compact JSON, since its bytes are
spliced in without re-validation. Build with `-race` or the `simdjson_safehooks`
tag during development: it runs each body against a heap-copied cursor and turns
a retained handle into an immediate panic instead of latent corruption. Output
and decoded values stay byte-identical to `encoding/json`.

</details>

<details>
<summary><b>Capture unknown members with an <code>,inline</code> catch-all</b></summary>

An opt-in extension routes object members that match no declared field into a
`map[string]T` tagged `json:",inline"`, and re-emits them at the object's own
level. The tag is inert unless you enable it, so types that do not use it
compile to the identical plan and pay nothing.

```go
type Event struct {
	ID    int                        `json:"id"`
	Extra map[string]json.RawMessage `json:",inline"` // unknown members land here
}

decoder, _ := simdjson.CompileDecoder[Event](simdjson.DecoderOptions{InlineFields: true})
encoder, _ := simdjson.CompileEncoder[Event](simdjson.EncoderOptions{InlineFields: true})
```

The catch-all consumes members that `DisallowUnknownFields` would otherwise
reject. On encode the map's members follow the declared fields, sorted by name
for deterministic output (`EncoderOptions.UnsortedInlineFields` emits them in
map order instead); a populated catch-all encodes without allocating once the
encoder's pooled scratch has warmed. With `DecoderOptions.Replace`, a reused
destination's map is cleared before decoding, like any other field; the default
merges.

</details>

<details>
<summary><b>Validate without decoding</b></summary>

`Valid` and `Validate` check strict JSON syntax and full UTF-8 validity
without building any representation.

```go
fmt.Println(simdjson.Valid([]byte(`{"strict":true}`))) // true

err := simdjson.Validate([]byte(`{"trailing":1,}`))
fmt.Println(err)
// json syntax error at byte 14, line 1, column 15: expected object key string
```

`ValidNumber`, `ValidString`, and their `Validate` forms check single JSON
number and string literals.

</details>

<details>
<summary><b>Extract one value with a JSON Pointer</b></summary>

`GetRaw` resolves an RFC 6901 pointer to a raw source slice while validating
the whole document. `ScanRaw` validates only up to and including the target
and stops — the fast choice for plucking one field from a large document.

```go
price, ok, err := simdjson.GetRaw(src, "/items/0/price")
if err != nil || !ok {
	return err
}
v, _ := price.Float64()
fmt.Println(v) // 9.99

// ScanRaw stops as soon as the target has been validated; compile
// the pointer once on hot paths.
pointer := simdjson.MustCompilePointer("/items/0/sku")
sku, ok, err := pointer.ScanRaw(src)
if err != nil || !ok {
	return err
}
text, _, _ := sku.Text()
fmt.Println(text) // a-1
```

For repeated traversal of one document, `BuildIndex` validates the input once
and lays out a navigable structural index in caller-provided storage, which
`Node` and the iterators walk without allocating (`RequiredIndexEntries`
reports the exact storage needed):

```go
var storage [16]simdjson.IndexEntry
index, err := simdjson.BuildIndex(src, storage[:])
if err != nil {
	return err
}

items, ok, err := index.Pointer("/items")
if err != nil || !ok {
	return err
}
iter, _ := items.ArrayIter()
for item, ok := iter.Next(); ok; item, ok = iter.Next() {
	sku, _ := item.Get("sku")
	raw, _ := sku.StringBytes() // aliases src; nothing allocates
	fmt.Println(string(raw))
}
```

</details>

<details>
<summary><b>Transform documents: compact, indent, canonicalize</b></summary>

All three validate their input and append to caller-owned buffers;
`Compact`, `Indent`, and `Canonicalize` are the allocating conveniences.

```go
compact, err := simdjson.AppendCompact(nil, src)
if err != nil {
	return err
}
fmt.Println(string(compact)) // {"b":1,"a":[1,2]}

canonical, err := simdjson.AppendCanonicalize(nil, src)
if err != nil {
	return err
}
fmt.Println(string(canonical)) // members sorted: {"a":[1,2],"b":1}

pretty, err := simdjson.AppendIndent(nil, src, "", "  ")
if err != nil {
	return err
}
fmt.Println(string(pretty)) // reindented, like json.Indent
```

</details>

<details>
<summary><b>Use the SIMD kernels directly</b></summary>

`github.com/thesyncim/simdjson/simd` is independently importable and adds no
module dependencies. The fixed-array digit API makes load and store widths
explicit:

```go
import "github.com/thesyncim/simdjson/simd"

if len(src) >= 16 {
	digits := (*[16]byte)(src)
	if simd.All16Digits(digits) {
		value := simd.Parse16Digits(digits)
		_ = value
	}
}

info := simd.Current()
_ = info.Enabled // true when built with GOEXPERIMENT=simd on a supported CPU
```

See the [SIMD package](#simd-package) section for the full kernel inventory
and runtime dispatch.

</details>

## Performance

Apple M4 Max, one CPU, six 300 ms samples, exact 6.33 MiB Go
`encoding/json` corpus. Lower time is better; the table reports geometric-mean
speedup across all seven payloads.

| Operation | Contract | vs stdlib | vs fastest rival | vs native Sonic | SIMD vs pure Go |
|---|---|---:|---:|---:|---:|
| Validate | Strict JSON + UTF-8 | **2.51x** | **2.28x** | **1.06x** | **1.497x** |
| Typed decode | Owned strings | **5.10x** | **2.06x** | **2.08x** | **1.159x** |
| Dynamic decode | Owned `any` tree | **4.48x** | **2.01x** | **1.40x** | **1.121x** |
| Encode | Owned output | **3.39x** | **2.06x** | **3.91x** | **1.720x** |
| Encode | Reused output buffer | **3.76x** | **2.29x** | — | **1.791x** |

Every stdlib row and every rival row wins all seven payloads, owned encode
included. Comparisons use the same Go tip compiler and do
not mix owned and source-backed results. The Sonic column compares against
native Sonic v1.15.2 compiled with the previous stable Go (1.26.4) in an
isolated module, because Sonic falls back to `encoding/json` on Go tip; it is
excluded from the fastest-rival column, its `Valid` is syntax-only where ours
also enforces UTF-8, and it has no reused-buffer `Marshal` counterpart. The
SIMD column compares the same code, compiler, and corpus with and without
`GOEXPERIMENT=simd`.

The upcoming `encoding/json/v2` (`GOEXPERIMENT=jsonv2`) trails on the same
corpus by 3.9x on typed decode, 2.5x on dynamic decode, and 3.3x on owned
`Marshal` — see [the v2 table](benchmarks/README.md#encodingjsonv2).

The Validate row was refreshed on 2026-07-15 on the current build; the decode
and encode rows, the native-Sonic column, and the SIMD-versus-pure column for
those rows are the pinned 2026-07-14 snapshot. The 2026-07-15 regeneration ran
under heavy background machine load: the zero-allocation paths (validation and
the reused-buffer encoder) reproduced or beat their published times, but every
path that allocates a fresh owned tree or output buffer inflated in lockstep
for simdjson and the competitors alike — allocator and memory-bandwidth
contention rather than a code change, since same-process leads held steady
through it. The published owned-allocation absolutes are therefore kept as the
honest idle-machine figures for this same build, and will be re-pinned from an
idle window. See the
[per-corpus tables](benchmarks/README.md#published-corpus-snapshot) for the
refreshed validation and new Parse/DOM rows.

[Full per-corpus results, allocations, SIMD uplift, versions, and exact commands](benchmarks/README.md#published-corpus-snapshot).
For context beyond Go — C++ simdjson and Rust serde_json/simd-json on the
same corpus and machine — see the
[cross-language benchmarks](benchmarks/README.md#cross-language-context).

## Compatibility and contracts

**Strictness.** Parsing, decoding, validation, and transforms enforce RFC
8259 syntax, full UTF-8 validity, and correct `\uXXXX` escapes including
surrogate pairing, and reject trailing data after the top-level value
(`ScanRaw` deliberately stops validating once its target is found; `Reader`
frames multiple top-level values by design). Where `encoding/json` silently
replaces invalid UTF-8 during decode, simdjson rejects the document with a
positioned error. Depth is limited (default 10000, configurable through the
`MaxDepth` options).

**encoding/json parity.** Encoded output matches `encoding/json` byte for
byte, with one deliberate exception: a custom `json.Marshaler` or
`json.RawMessage` whose bytes contain a lone `\uXXXX` surrogate or invalid
UTF-8 is rejected rather than passed through. simdjson rejects those same
bytes on decode, so accepting them here would emit JSON it could not read
back; the strict-UTF-8 guarantee is symmetric across encode and decode.
Decoding follows stdlib semantics: struct tags, case-insensitive field
fallback (disable with `CaseSensitive`), merge-into-existing destinations
(switch with `Replace`), `json.Unmarshaler`/`json.Marshaler`,
`encoding.TextUnmarshaler`/`encoding.TextMarshaler`, and `time.Time`. Typed
and dynamic differential tests run against `encoding/json`.

**Ownership.** Default decodes return owned results that are independent of
the source. `ZeroCopy` decodes alias the source: keep it alive and unmodified
while results are in use. A `Value` from `Parse` owns a private copy of the
source and its structural index (with `Options.ZeroCopy` it aliases the
caller's bytes instead), and keeps both alive for as long as any `Value`
derived from it is reachable. `RawValue`, `Index`, and `Node` always alias the
source; `Index` and `Node` also alias the caller-provided `IndexEntry`
workspace. `AppendJSON` requires destination storage disjoint from storage
reachable through the source value; on error it returns the original
destination length, but unused capacity may contain partial output.

**Concurrency.** Compiled `Decoder`, `Encoder`, `Codec`, and
`CompiledPointer` values are immutable and safe for concurrent use, as are
the package-level functions backed by cached plans. A `Reader` or `Writer`
belongs to one goroutine.

## SIMD package

`github.com/thesyncim/simdjson/simd` owns every architecture-specific kernel,
runtime feature probe, and dispatch decision. It provides fixed-width decimal
parsing and formatting, `encoding/json`-compatible float formatting, quoted
RFC3339Nano time formatting, JSON and HTML-safe string scans with fused
prefix copies, strict UTF-8 / U+2028-U+2029 / contiguous `\uXXXX` kernels,
safe public scanners plus an explicit precondition-based `simd.Unchecked`
surface, and `Current`, which reports selected backends, vector widths, and
CPU features.

All APIs have portable fallbacks; every vector load is length-guarded.
Runtime capabilities are read once and implementation choices are fixed
during package initialization:

| Runtime | String scanning | Decimal parse | Decimal and time format |
|---|---|---|---|
| arm64 | NEON on sustained runs; overlap-vector tails | NEON 16-digit reduction | NEON 16-digit and RFC3339 formatting |
| amd64 with AVX-512 | 64-byte AVX-512 | AVX 16-digit reduction | Scalar SWAR |
| amd64 with AVX2 | 32-byte AVX2 | AVX 16-digit reduction | Scalar SWAR |
| Other build or CPU | Scalar Go | Scalar Go | Scalar SWAR |

[Kernel benchmark results](benchmarks/README.md#simd-kernel-snapshot) use
guarded public APIs and report zero allocations.

## Correctness and safety

Unsafe code is restricted to measured internal paths: complete guarded blocks
around vector loads and stores, clamped public scanners with a documented
`simd.Unchecked` precondition surface, size-proven float and integer stores,
and typed offsets taken from public `reflect` metadata — never runtime layout
assumptions. Source-backed APIs require immutable input, and pointer-receiver
custom methods use GC-safe heap-backed shadows.

The test suite covers all 318 JSONTestSuite parsing cases, the seven pinned
Go tip payloads with exact concrete models, typed and dynamic differentials
against `encoding/json`, 500,000 randomized float spellings, 700,000
randomized or boundary timestamps, randomized scalar/SIMD differentials
across lengths and alignments, and fuzzers for validation, transforms, typed
decode, encode, numbers, and the SIMD kernels.

Correctness fixes that touch a hot path require before-and-after benchmarks.
A regression is optimized back before merge; correctness is never traded away
to recover it.

<details>
<summary><b>Local release gate</b></summary>

The gate uses the pinned compiler only so results are exactly reproducible:

```sh
./scripts/bootstrap-gotip.sh "$HOME/sdk/simdjson-gotip"
TIP_GO="$HOME/sdk/simdjson-gotip/bin/go"

"$TIP_GO" test ./...
GOEXPERIMENT=simd "$TIP_GO" test ./...
"$TIP_GO" vet -unsafeptr=false ./...
GOEXPERIMENT=simd "$TIP_GO" test -race \
  -skip 'Allocs|StaysOnStack|TestParseFloat64' ./...
GOEXPERIMENT=simd "$TIP_GO" test -gcflags='all=-d=checkptr=2' \
  -skip 'Allocs|StaysOnStack|TestParseFloat64' ./...
./scripts/check-stdlib-corpus.sh "$TIP_GO"
```

Vet's `unsafeptr` analyzer is disabled only for the documented runtime-style
pointer-hiding helper in `noescape.go`; all other vet analyzers remain
enabled.

</details>

## Reproduce benchmarks

Comparison libraries live in nested modules and never enter the root module.
The complete suite builds the pinned Go tip compiler, verifies the copied
stdlib corpus, and runs pure Go, SIMD, jsonv2, and native Sonic controls:

```sh
./scripts/bootstrap-gotip.sh "$HOME/sdk/simdjson-gotip"
TIP_GO="$HOME/sdk/simdjson-gotip/bin/go" ./benchmarks/run-comparison.sh
```

[Benchmark methodology and individual commands](benchmarks/README.md)

## Status

simdjson is pre-release. The API may change until the first tagged release.

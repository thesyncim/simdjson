package pgbaseline

// The acceptance table from ADR 0002, as code. Every later phase
// regenerates the phase-0 report against exactly these rows; the targets
// bind the design, and misses are reported as measured, never edited away.

// TargetKind identifies which measured quantity a target constrains.
type TargetKind int

const (
	// TargetSpace compares our retained bytes (DocSet arenas and headers,
	// HashKeys on) against PostgreSQL's table plus gin jsonb_path_ops
	// index, both measured at rest. Ratio is ours/PG; lower is better.
	TargetSpace TargetKind = iota
	// TargetIngest compares single-core ingest throughput over minified
	// source bytes: our ReadFrom against PostgreSQL's COPY plus the gin
	// jsonb_path_ops build. Ratio is ours/PG; higher is better.
	TargetIngest
	// TargetExtract compares per-document point extraction: our best
	// public full-scan path against PostgreSQL's ->> full-scan per-row
	// cost. Ratio is PG/ours; higher is better.
	TargetExtract
	// TargetExist compares whole-corpus key-existence counting: our column
	// scan against PostgreSQL's best of sequential and gin jsonb_ops
	// plans. Ratio is PG/ours; higher is better.
	TargetExist
	// TargetContain compares whole-corpus containment counting: our column
	// scan against PostgreSQL's best of sequential, gin jsonb_ops, and gin
	// jsonb_path_ops plans. Ratio is PG/ours; higher is better.
	TargetContain
)

// Target is one acceptance row: the constraint a corpus class must meet.
type Target struct {
	ID    string
	Kind  TargetKind
	Class string // corpus class the row applies to; "" means every corpus

	// Bound is the required ratio: an upper bound for TargetSpace (ours/PG
	// must be <= Bound), a lower bound otherwise (must be >= Bound).
	Bound float64

	// Phase is the ADR phase whose machinery the target assumes; rows
	// whose phase has not landed are still measured and reported, marked
	// with the pending phase, because the starting point is the point.
	Phase int
	Note  string
}

// Targets is ADR 0002's acceptance table.
var Targets = []Target{
	{
		ID: "space-clustered", Kind: TargetSpace, Class: "clustered", Bound: 0.6, Phase: 1,
		Note: "documents + index structures vs PG table + gin jsonb_path_ops; assumes shape-deduplicated tapes (phase 1) and dual-width tapes (phase 2)",
	},
	{
		ID: "space-real", Kind: TargetSpace, Class: "real", Bound: 0.6, Phase: 1,
		Note: "real corpora cluster into shapes and carry the clustered bound",
	},
	{
		ID: "space-heterogeneous", Kind: TargetSpace, Class: "heterogeneous", Bound: 1.0, Phase: 1,
		Note: "adversarial: every document a distinct shape, no shape reuse to harvest",
	},
	{
		ID: "space-stretch-columnar", Kind: TargetSpace, Class: "clustered", Bound: 0.4, Phase: 3,
		Note: "stretch goal: cold columnar mode (phase 3), keys stored only in shapes",
	},
	{
		ID: "ingest", Kind: TargetIngest, Class: "", Bound: 10, Phase: 0,
		Note: "single-core ReadFrom vs COPY + gin jsonb_path_ops build; our side builds no key postings yet (phase 4 adds them)",
	},
	{
		ID: "extract", Kind: TargetExtract, Class: "", Bound: 50, Phase: 0,
		Note: "per-document ->>-equivalent, full scan on both sides",
	},
	{
		ID: "exist", Kind: TargetExist, Class: "", Bound: 10, Phase: 4,
		Note: "ours is a full column scan until the inverted layer (phase 4); PG may use gin jsonb_ops",
	},
	{
		ID: "contain", Kind: TargetContain, Class: "", Bound: 5, Phase: 4,
		Note: "ours is a column scan with scalar equality until RawContains + pruning (phase 4); PG may use either gin index",
	},
}

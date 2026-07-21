package redisbench

// The ADR 0003 acceptance table, as code. The query subpackage regenerates
// this scoreboard as it lands; the targets bind the surface, and misses are
// reported as measured, never edited away. The framing is single-core parity:
// our reads are far more expressive than RedisJSON's (containment has no
// competitor operator at all), so the speed bar is to stay at or above parity
// on every scenario RediSearch can express.

// TargetKind identifies which measured quantity a target constrains.
type TargetKind int

const (
	// TargetSpace compares our retained bytes (DocSet arenas and headers)
	// against RedisJSON's keyspace plus the RediSearch index, both at rest.
	// Ratio is ours/Redis; lower is better.
	TargetSpace TargetKind = iota
	// TargetProject compares single-core point projection: our best full-scan
	// or shape-column per-document cost against RedisJSON's JSON.GET. Ratio is
	// Redis/ours; higher is better.
	TargetProject
	// TargetFilter compares the filtered scan: our column scan with a scalar
	// equality against RediSearch FT.SEARCH on a TAG field. Ratio is
	// Redis/ours; higher is better.
	TargetFilter
	// TargetSum compares the scalar aggregate: our AppendFieldInt64 reduce
	// against RediSearch FT.AGGREGATE REDUCE SUM. Ratio is Redis/ours; higher
	// is better.
	TargetSum
	// TargetGroup compares the grouped aggregate: our KeyInterner group-by
	// against RediSearch FT.AGGREGATE GROUPBY. Ratio is Redis/ours; higher is
	// better.
	TargetGroup
)

// Target is one acceptance row: the constraint a corpus class must meet.
type Target struct {
	ID    string
	Kind  TargetKind
	Class string // corpus class the row applies to; "" means every corpus

	// Bound is the required ratio: an upper bound for TargetSpace (ours/Redis
	// must be <= Bound), a lower bound otherwise (must be >= Bound).
	Bound float64

	Note string
}

// Targets is ADR 0003's acceptance table: single-core parity on every
// expressible scenario, and a space bound against RedisJSON's uncompressed
// keyspace plus the RediSearch index.
var Targets = []Target{
	{
		ID: "space-clustered", Kind: TargetSpace, Class: "clustered", Bound: 1.0,
		Note: "DocSet (shape-deduplicated) vs RedisJSON keyspace + RediSearch index; RedisJSON stores an uncompressed re-encoded object per key and RediSearch a separate inverted index",
	},
	{
		ID: "space-real", Kind: TargetSpace, Class: "real", Bound: 1.0,
		Note: "real corpora cluster into shapes and carry the clustered space bound",
	},
	{
		ID: "space-heterogeneous", Kind: TargetSpace, Class: "heterogeneous", Bound: 1.5,
		Note: "adversarial: every document a distinct shape, no shape reuse to harvest",
	},
	{
		ID: "project", Kind: TargetProject, Class: "", Bound: 1.0,
		Note: "point projection: our Doc+PointerCompiled vs JSON.GET; our whole-corpus AppendField has no single JSON.GET analogue",
	},
	{
		ID: "filter", Kind: TargetFilter, Class: "", Bound: 1.0,
		Note: "filtered scan: our column scan + scalar equality (no pre-declared schema) vs FT.SEARCH on a TAG field that FT.CREATE had to declare up front",
	},
	{
		ID: "sum", Kind: TargetSum, Class: "", Bound: 1.0,
		Note: "scalar aggregate: our AppendFieldInt64 reduce vs FT.AGGREGATE REDUCE SUM",
	},
	{
		ID: "group", Kind: TargetGroup, Class: "", Bound: 1.0,
		Note: "grouped aggregate: our KeyInterner group-by vs FT.AGGREGATE GROUPBY",
	},
}

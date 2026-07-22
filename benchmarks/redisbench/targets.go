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
	// TargetSpace compares the keyed Store's retained heap, including its exact
	// index, against RedisJSON's keyspace plus the RediSearch index at rest.
	// Ratio is ours/Redis; lower is better.
	TargetSpace TargetKind = iota
	// TargetProject compares Store Snapshot.Get plus a compiled pointer against
	// RedisJSON's JSON.GET. Ratio is Redis/ours; higher is better.
	TargetProject
	// TargetFilter compares a Store query using the declared exact index and an
	// exact scalar recheck against RediSearch FT.SEARCH on a TAG field.
	TargetFilter
	// TargetSum compares the scalar aggregate: our RunSnapshotInto reduce
	// against RediSearch FT.AGGREGATE REDUCE SUM. Ratio is Redis/ours; higher
	// is better.
	TargetSum
	// TargetGroup compares the grouped aggregate: our RunSnapshotInto group-by
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
		Note: "keyed Store plus the matching exact index vs RedisJSON keyspace plus RediSearch index; the DocSet shape row is diagnostic only",
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
		Note: "point projection: Snapshot.Get plus PointerCompiled vs JSON.GET; the schema-free DocSet corpus projection has no single JSON.GET analogue",
	},
	{
		ID: "filter", Kind: TargetFilter, Class: "", Bound: 1.0,
		Note: "our declared nested-capable exact index plus mandatory scalar recheck vs the same field declared as a RediSearch TAG",
	},
	{
		ID: "sum", Kind: TargetSum, Class: "", Bound: 1.0,
		Note: "scalar aggregate: Store RunSnapshotInto SUM vs FT.AGGREGATE REDUCE SUM",
	},
	{
		ID: "group", Kind: TargetGroup, Class: "", Bound: 1.0,
		Note: "grouped aggregate: Store RunSnapshotInto group-by vs FT.AGGREGATE GROUPBY",
	},
}

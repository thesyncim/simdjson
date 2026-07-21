package query

import (
	"sort"

	"github.com/thesyncim/simdjson"
)

// The executor. run extracts each needed path as a column off the DocSet's
// tape, filters rows with a full columnar scan of the compiled predicate,
// reduces aggregates over the typed columns, groups by interning group keys,
// and sorts and truncates the small result. Every step is one pass; the result
// materializes row-oriented and transposes to the column-oriented Result once,
// because result sets are small next to the corpus.

// execCtx holds one Run's transient state: the extracted columns and a
// per-Run ShapeCache (the cache is single-consumer, so a fresh one keeps
// concurrent Runs of one compiled query independent).
type execCtx struct {
	s      *simdjson.DocSet
	cache  simdjson.ShapeCache
	rows   int
	values [][]scalar  // one classified column per plan.valuePaths entry
	nums   []numColumn // one numeric column per plan.numPaths entry
}

// A numColumn is an aggregate argument extracted as dense float64 cells with a
// validity mask: valid[i] is false for an absent, null, non-numeric, or
// out-of-range value, exactly the cells SUM/AVG/MIN/MAX skip.
type numColumn struct {
	vals  []float64
	valid []bool
}

// run executes the compiled plan over s.
func (p *plan) run(s *simdjson.DocSet) (Result, error) {
	ctx := &execCtx{s: s, rows: s.Len()}
	if err := ctx.extract(p); err != nil {
		return Result{}, err
	}
	selected := p.selectRows(ctx)
	switch {
	case p.grouped:
		return p.runGrouped(ctx, selected), nil
	case p.singleRow:
		return p.runAggregate(ctx, selected), nil
	default:
		return p.runProjection(ctx, selected), nil
	}
}

// extract materializes every value and numeric column the plan reads.
func (ctx *execCtx) extract(p *plan) error {
	ctx.values = make([][]scalar, len(p.valuePaths))
	for i, cp := range p.valuePaths {
		raws, err := ctx.rawColumn(cp)
		if err != nil {
			return err
		}
		col := make([]scalar, len(raws))
		for j, r := range raws {
			col[j] = classifyRaw(r)
		}
		ctx.values[i] = col
	}
	ctx.nums = make([]numColumn, len(p.numPaths))
	for i, cp := range p.numPaths {
		nc, err := ctx.numericColumn(cp)
		if err != nil {
			return err
		}
		ctx.nums[i] = nc
	}
	return nil
}

// rawColumn extracts a path as raw values: the fused field scan for a single
// top-level field, the compiled pointer otherwise.
func (ctx *execCtx) rawColumn(cp compiledPath) ([]simdjson.RawValue, error) {
	if cp.single {
		return ctx.cache.AppendField(nil, ctx.s, cp.name), nil
	}
	return ctx.s.AppendPointer(nil, cp.pointer)
}

// numericColumn extracts an aggregate argument as float64 cells with a validity
// mask. A single top-level field takes the typed fused scan
// (AppendFieldFloat64); a nested path resolves through the compiled pointer and
// parses each cell with the same Float64 verdict.
func (ctx *execCtx) numericColumn(cp compiledPath) (numColumn, error) {
	if cp.single {
		vals, valid := ctx.cache.AppendFieldFloat64(nil, nil, ctx.s, cp.name)
		return numColumn{vals: vals, valid: valid}, nil
	}
	raws, err := ctx.s.AppendPointer(nil, cp.pointer)
	if err != nil {
		return numColumn{}, err
	}
	vals := make([]float64, len(raws))
	valid := make([]bool, len(raws))
	for i, r := range raws {
		if f, ok := r.Float64(); ok {
			vals[i], valid[i] = f, true
		}
	}
	return numColumn{vals: vals, valid: valid}, nil
}

// selectRows returns the row ordinals the WHERE predicate accepts, in
// ascending order. It tests each candidate the seam yields with the compiled
// predicate; candidateRows decides which rows are candidates.
func (p *plan) selectRows(ctx *execCtx) []int {
	candidates := p.candidateRows(ctx)
	selected := make([]int, 0, ctx.rows)
	if candidates == nil {
		for row := 0; row < ctx.rows; row++ {
			if p.where == nil || p.where.eval(ctx.values, row) {
				selected = append(selected, row)
			}
		}
		return selected
	}
	for _, row := range candidates {
		if p.where == nil || p.where.eval(ctx.values, row) {
			selected = append(selected, row)
		}
	}
	return selected
}

// candidateRows is the selective seam. It returns the rows the WHERE predicate
// must be tested against, or nil meaning "every row" — the full columnar scan.
//
// When the DocSet opted into the inverted posting layer (DocSet.Postings) and
// the predicate has a leaf the postings can answer, candidateRows returns a
// narrowed candidate slice built from the predicate's postable leaves (see
// candidates.go), and selectRows verifies each candidate with the same per-row
// eval — so the accepted-rows contract and everything downstream are unchanged,
// only the candidate enumeration gets cheaper. Without postings, or for a
// predicate no leaf can prune, it returns nil and the full scan stands.
func (p *plan) candidateRows(ctx *execCtx) []int {
	if p.where == nil || !ctx.s.Postings {
		return nil
	}
	rows, ok := p.where.candidates(ctx.s)
	if !ok {
		return nil // no postable leaf bounds the predicate: full scan
	}
	if rows == nil {
		// A postable predicate that matches no row: an empty candidate set, not
		// "every row". Hand back a non-nil empty slice so selectRows selects
		// nothing rather than falling into the full scan.
		return []int{}
	}
	return rows
}

// runProjection builds one result row per selected document. A projection can
// be as large as the corpus, so the unordered case — the common one — fills the
// result columns directly, without a per-row intermediate; only ORDER BY needs
// the row materialization the sort works over.
func (p *plan) runProjection(ctx *execCtx, selected []int) Result {
	if len(p.order) == 0 {
		if p.hasLimit && len(selected) > p.limit {
			selected = selected[:p.limit]
		}
		columns := make([]ResultColumn, len(p.columns))
		for c := range p.columns {
			columns[c] = ResultColumn{Header: p.columns[c].header, Cells: make([]Cell, len(selected))}
		}
		for r, row := range selected {
			for c, col := range p.columns {
				columns[c].Cells[r] = cellFromScalar(ctx.values[col.value][row])
			}
		}
		return Result{Columns: columns, RowCount: len(selected)}
	}
	rows := make([]resultRow, 0, len(selected))
	for _, row := range selected {
		r := resultRow{cells: make([]Cell, len(p.columns))}
		for c, col := range p.columns {
			r.cells[c] = cellFromScalar(ctx.values[col.value][row])
		}
		r.order = p.orderKeysPerRow(ctx, row)
		rows = append(rows, r)
	}
	return p.finish(rows)
}

// runAggregate builds the single result row of an aggregate query with no
// GROUP BY.
func (p *plan) runAggregate(ctx *execCtx, selected []int) Result {
	accs := make([]aggAcc, len(p.columns))
	for _, row := range selected {
		p.accumulate(accs, ctx, row)
	}
	r := resultRow{cells: p.aggregateCells(accs, nil)}
	return p.finish([]resultRow{r})
}

// runGrouped builds one result row per group, interning group keys to route
// each selected row to its accumulators in a single pass.
func (p *plan) runGrouped(ctx *execCtx, selected []int) Result {
	var interner simdjson.KeyInterner
	var groups []*group
	var key []byte
	for _, row := range selected {
		key = p.groupKey(key[:0], ctx, row)
		id := interner.Intern(key)
		if int(id) == len(groups) {
			groups = append(groups, p.newGroup(ctx, row))
		}
		p.accumulate(groups[id].accs, ctx, row)
	}
	rows := make([]resultRow, 0, len(groups))
	for _, g := range groups {
		rows = append(rows, resultRow{
			cells: p.aggregateCells(g.accs, g),
			order: p.orderKeysPerGroup(g),
		})
	}
	return p.finish(rows)
}

// groupKey encodes a row's group-by values into a single interner key.
func (p *plan) groupKey(dst []byte, ctx *execCtx, row int) []byte {
	for _, gc := range p.groupCols {
		dst = appendGroupKey(dst, ctx.values[gc][row])
	}
	return dst
}

// newGroup captures a fresh group's grouped-path values at first sighting; all
// later rows of the group share them by construction.
func (p *plan) newGroup(ctx *execCtx, row int) *group {
	g := &group{
		scalars: make([]scalar, len(p.groupCols)),
		accs:    make([]aggAcc, len(p.columns)),
	}
	for i, gc := range p.groupCols {
		g.scalars[i] = ctx.values[gc][row]
	}
	return g
}

// A group is one GROUP BY partition: its grouped-path values and its per-column
// accumulators.
type group struct {
	scalars []scalar
	accs    []aggAcc
}

// An aggAcc accumulates one aggregate column over the rows routed to it.
type aggAcc struct {
	count int     // COUNT contributions (rows, or present values)
	n     int     // numeric contributions to SUM/AVG/MIN/MAX
	sum   float64 // running total
	min   float64
	max   float64
}

// accumulate folds one row into a column's accumulators.
func (p *plan) accumulate(accs []aggAcc, ctx *execCtx, row int) {
	for c, col := range p.columns {
		switch col.agg {
		case aggCount:
			if col.value < 0 || present(ctx.values[col.value][row]) {
				accs[c].count++
			}
		case aggSum, aggAvg, aggMin, aggMax:
			nc := ctx.nums[col.num]
			if !nc.valid[row] {
				continue
			}
			v := nc.vals[row]
			a := &accs[c]
			if a.n == 0 {
				a.min, a.max = v, v
			} else {
				if v < a.min {
					a.min = v
				}
				if v > a.max {
					a.max = v
				}
			}
			a.sum += v
			a.n++
		}
	}
}

// aggregateCells materializes one output row from accumulators; g supplies the
// grouped-path values for projection columns and is nil for a single-row
// aggregate.
func (p *plan) aggregateCells(accs []aggAcc, g *group) []Cell {
	cells := make([]Cell, len(p.columns))
	for c, col := range p.columns {
		switch col.agg {
		case aggNone:
			cells[c] = cellFromScalar(g.scalars[col.slot])
		case aggCount:
			cells[c] = countCell(accs[c].count)
		case aggSum:
			cells[c] = numericOrNull(accs[c].n, accs[c].sum)
		case aggAvg:
			if accs[c].n == 0 {
				cells[c] = nullCell()
			} else {
				cells[c] = floatCell(accs[c].sum / float64(accs[c].n))
			}
		case aggMin:
			cells[c] = numericOrNull(accs[c].n, accs[c].min)
		case aggMax:
			cells[c] = numericOrNull(accs[c].n, accs[c].max)
		}
	}
	return cells
}

// numericOrNull returns a numeric cell, or null when no row contributed.
func numericOrNull(n int, v float64) Cell {
	if n == 0 {
		return nullCell()
	}
	return floatCell(v)
}

// orderKeysPerRow captures a projected row's ORDER BY keys.
func (p *plan) orderKeysPerRow(ctx *execCtx, row int) []scalar {
	if len(p.order) == 0 {
		return nil
	}
	keys := make([]scalar, len(p.order))
	for i, o := range p.order {
		keys[i] = ctx.values[o.value][row]
	}
	return keys
}

// orderKeysPerGroup captures a group's ORDER BY keys from its grouped values.
func (p *plan) orderKeysPerGroup(g *group) []scalar {
	if len(p.order) == 0 {
		return nil
	}
	keys := make([]scalar, len(p.order))
	for i, o := range p.order {
		keys[i] = g.scalars[o.slot]
	}
	return keys
}

// A resultRow is one materialized output row before transposition, carrying its
// cells and its ORDER BY keys.
type resultRow struct {
	cells []Cell
	order []scalar
}

// finish sorts, limits, and transposes the materialized rows into the
// column-oriented Result.
func (p *plan) finish(rows []resultRow) Result {
	p.sortRows(rows)
	if p.hasLimit && len(rows) > p.limit {
		rows = rows[:p.limit]
	}
	columns := make([]ResultColumn, len(p.columns))
	for c := range p.columns {
		columns[c] = ResultColumn{Header: p.columns[c].header, Cells: make([]Cell, len(rows))}
	}
	for r := range rows {
		for c := range p.columns {
			columns[c].Cells[r] = rows[r].cells[c]
		}
	}
	return Result{Columns: columns, RowCount: len(rows)}
}

// sortRows applies the ORDER BY keys with a stable sort, so rows or groups that
// tie keep their scan (first-appearance) order.
func (p *plan) sortRows(rows []resultRow) {
	if len(p.order) == 0 {
		return
	}
	sort.SliceStable(rows, func(i, j int) bool {
		for k, o := range p.order {
			c := compareScalar(rows[i].order[k], rows[j].order[k])
			if o.dir == Desc {
				c = -c
			}
			if c != 0 {
				return c < 0
			}
		}
		return false
	})
}

package query

import "github.com/thesyncim/simdjson"

type fileIndexSnapshot struct {
	snapshot  *simdjson.FileSnapshot
	workspace *simdjson.FileIndexWorkspace
}

func (s fileIndexSnapshot) AppendIndexes(dst []simdjson.StoreIndexInfo) []simdjson.StoreIndexInfo {
	return s.snapshot.AppendIndexes(dst)
}

func (s fileIndexSnapshot) AppendIndexMasks(dst []simdjson.StoreMask, name string, values ...simdjson.Index) ([]simdjson.StoreMask, error) {
	return s.snapshot.AppendIndexCandidateMasksInto(dst, s.workspace, name, values...)
}

func (p *plan) fileCandidateMasks(snapshot *simdjson.FileSnapshot, index *simdjson.FileIndexWorkspace, w *Workspace) ([]simdjson.StoreMask, error) {
	return fileCandidateMasksFor(p, fileIndexSnapshot{snapshot: snapshot, workspace: index}, w)
}

func fileCandidateMasksFor(p *plan, snapshot fileIndexSnapshot, w *Workspace) ([]simdjson.StoreMask, error) {
	if p.where == nil {
		return nil, nil
	}
	w.storeMaskUsed = 0
	w.storeIndexProbes = 0
	w.storeIndexes = snapshot.AppendIndexes(w.storeIndexes[:0])
	if !p.where.fileCanBound(p.valuePaths, w.storeIndexes) {
		return nil, nil
	}
	masks, bounded, _, err := fileCandidatesFor(p.where, snapshot, p.valuePaths, w.storeIndexes, w)
	if err != nil {
		return nil, err
	}
	if !bounded {
		return nil, nil
	}
	if masks == nil {
		return w.emptyStoreMask[:0], nil
	}
	return masks, nil
}

func fileCandidatesFor(p *compiledPredicate, snapshot fileIndexSnapshot, paths []compiledPath, indexes []simdjson.StoreIndexInfo, w *Workspace) ([]simdjson.StoreMask, bool, bool, error) {
	switch p.kind {
	case predCmp:
		if p.op != Eq {
			return nil, false, false, nil
		}
		path := paths[p.col].indexPath()
		for _, index := range indexes {
			if index.Kind != simdjson.StoreIndexExact || index.State != simdjson.StoreIndexReady || index.ColumnCount != 1 || index.Columns[0] != path {
				continue
			}
			out := w.nextStoreMasks()
			out, err := snapshot.AppendIndexMasks(out, index.Name, p.needle)
			if err != nil {
				return nil, false, false, err
			}
			w.storeIndexProbes++
			w.keepStoreMasks(out)
			return out, true, false, nil
		}
		return nil, false, false, nil
	case predContains:
		if p.containIndexPath == "" {
			return nil, false, false, nil
		}
		for _, index := range indexes {
			if index.Kind != simdjson.StoreIndexExact || index.State != simdjson.StoreIndexReady || index.ColumnCount != 1 || index.Columns[0] != p.containIndexPath {
				continue
			}
			out := w.nextStoreMasks()
			out, err := snapshot.AppendIndexMasks(out, index.Name, p.containIndexNeedle)
			if err != nil {
				return nil, false, false, err
			}
			w.storeIndexProbes++
			w.keepStoreMasks(out)
			return out, true, false, nil
		}
		return nil, false, false, nil
	case predAnd:
		return fileAndCandidatesFor(p, snapshot, paths, indexes, w)
	case predOr:
		for _, kid := range p.kids {
			if !kid.fileCanBound(paths, indexes) {
				return nil, false, false, nil
			}
		}
		return fileOrCandidatesFor(p, snapshot, paths, indexes, w)
	default:
		// Durable NOT deliberately stays unbounded. Constructing its exact live
		// complement requires fallible page I/O and is not a zero-cost metadata
		// operation like heap Snapshot.AppendLiveMasks.
		return nil, false, false, nil
	}
}

// fileCanBound is the no-I/O planner pass. In particular, OR must prove every
// branch usable before the first persistent probe; otherwise a full scan would
// pay index I/O that cannot restrict its universe.
func (p *compiledPredicate) fileCanBound(paths []compiledPath, indexes []simdjson.StoreIndexInfo) bool {
	switch p.kind {
	case predCmp:
		if p.op != Eq {
			return false
		}
		path := paths[p.col].indexPath()
		for _, index := range indexes {
			if index.Kind == simdjson.StoreIndexExact && index.State == simdjson.StoreIndexReady &&
				index.ColumnCount == 1 && index.Columns[0] == path {
				return true
			}
		}
		return false
	case predContains:
		if p.containIndexPath == "" {
			return false
		}
		for _, index := range indexes {
			if index.Kind == simdjson.StoreIndexExact && index.State == simdjson.StoreIndexReady &&
				index.ColumnCount == 1 && index.Columns[0] == p.containIndexPath {
				return true
			}
		}
		return false
	case predAnd:
		if _, _, ok := p.bestCompoundIndex(paths, indexes); ok {
			return true
		}
		for _, kid := range p.kids {
			if kid.fileCanBound(paths, indexes) {
				return true
			}
		}
		return false
	case predOr:
		if len(p.kids) == 0 {
			return false
		}
		for _, kid := range p.kids {
			if !kid.fileCanBound(paths, indexes) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func fileAndCandidatesFor(p *compiledPredicate, snapshot fileIndexSnapshot, paths []compiledPath, indexes []simdjson.StoreIndexInfo, w *Workspace) ([]simdjson.StoreMask, bool, bool, error) {
	var acc []simdjson.StoreMask
	have := false
	allExact := true
	var compound simdjson.StoreIndexInfo
	if index, values, ok := p.bestCompoundIndex(paths, indexes); ok {
		compound = index
		acc = w.nextStoreMasks()
		var err error
		acc, err = snapshot.AppendIndexMasks(acc, index.Name, values[:index.ColumnCount]...)
		if err != nil {
			return nil, false, false, err
		}
		w.storeIndexProbes++
		w.keepStoreMasks(acc)
		have = true
		allExact = false
	}
	for _, kid := range p.kids {
		if kid.coveredEquality(paths, compound) {
			continue
		}
		rows, bounded, exact, err := fileCandidatesFor(kid, snapshot, paths, indexes, w)
		if err != nil {
			return nil, false, false, err
		}
		if !bounded {
			allExact = false
			continue
		}
		allExact = allExact && exact
		if !have {
			acc, have = rows, true
			continue
		}
		acc = intersectStoreMasks(w.nextStoreMasks(), acc, rows)
		w.keepStoreMasks(acc)
	}
	if !have {
		return nil, false, false, nil
	}
	return acc, true, allExact, nil
}

func fileOrCandidatesFor(p *compiledPredicate, snapshot fileIndexSnapshot, paths []compiledPath, indexes []simdjson.StoreIndexInfo, w *Workspace) ([]simdjson.StoreMask, bool, bool, error) {
	var acc []simdjson.StoreMask
	allExact := true
	for i, kid := range p.kids {
		rows, bounded, exact, err := fileCandidatesFor(kid, snapshot, paths, indexes, w)
		if err != nil {
			return nil, false, false, err
		}
		if !bounded {
			return nil, false, false, nil
		}
		allExact = allExact && exact
		if i == 0 {
			acc = rows
			continue
		}
		acc = unionStoreMasks(w.nextStoreMasks(), acc, rows)
		w.keepStoreMasks(acc)
	}
	return acc, true, allExact, nil
}

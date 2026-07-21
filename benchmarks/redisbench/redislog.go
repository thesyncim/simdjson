package redisbench

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// This file parses the session logs produced by run-redis.sh. The logs are
// the raw RedisJSON/RediSearch artifact: the script extracts each measurement
// with redis-cli in raw mode and writes one self-describing line per fact, so
// the log needs no aligned-table parsing and the script does no arithmetic —
// every per-operation cost and ratio is recovered here, testably.
//
// Line grammar (one fact per line, space separated):
//
//	IMAGE <image>                 the pinned docker image
//	VERSION <redis version ...>   redis_version from INFO server
//	MODULE <name> <version>       one loaded module (ReJSON, search)
//	DOCS <n>                      documents the manifest declared
//	LOAD_NS <n>                   wall time of redis-cli --pipe mass load
//	LOAD_REPLIES <n>              replies --pipe acknowledged (== DOCS)
//	USED_MEMORY_BASE <bytes>      INFO memory used_memory before load
//	USED_MEMORY <bytes>           INFO memory used_memory after load (keyspace)
//	USED_MEMORY_INDEXED <bytes>   INFO memory used_memory after FT.CREATE
//	INDEX_NS <n>                  wall time of FT.CREATE + indexing drain
//	INDEX_NUM_DOCS <n>            FT.INFO num_docs (index cross-check)
//	SAMPLE <label> <ns>           one timed repetition of a scenario command
//	RESULT <label> <int>          the value that scenario command returned
//
// SAMPLE lines repeat once per outer repetition; QueryNS discards the first
// (warm-up) and takes the minimum of the rest, matching the ours-side timing
// discipline. Scenario labels: projection, filter, sum, groupby.

// RedisLog is one corpus's parsed RedisJSON/RediSearch session.
type RedisLog struct {
	Image   string
	Version string
	Modules map[string]string

	// Samples collects every SAMPLE nanosecond reading per scenario label, in
	// order; repeated scenario commands carry one sample per repetition.
	Samples map[string][]float64

	// Results holds the value each scenario command returned (filter count,
	// aggregate sum, group cardinality), for cross-checking both engines
	// against the manifest.
	Results map[string]int64

	// Values holds the single-valued facts: DOCS, LOAD_NS, LOAD_REPLIES, the
	// memory readings, and the index build time and sizes.
	Values map[string]int64
}

// present reports whether the log carried any content beyond an empty shell.
func (l RedisLog) present() bool { return l.Version != "" || len(l.Values) != 0 }

// ParseRedisLog parses one run-redis.sh session log.
func ParseRedisLog(path string) (RedisLog, error) {
	f, err := os.Open(path)
	if err != nil {
		return RedisLog{}, err
	}
	defer f.Close()

	log := RedisLog{
		Modules: map[string]string{},
		Samples: map[string][]float64{},
		Results: map[string]int64{},
		Values:  map[string]int64{},
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "IMAGE":
			if len(fields) >= 2 {
				log.Image = fields[1]
			}
		case "VERSION":
			if len(fields) >= 2 {
				log.Version = strings.Join(fields[1:], " ")
			}
		case "MODULE":
			if len(fields) >= 3 {
				log.Modules[fields[1]] = fields[2]
			}
		case "SAMPLE":
			if len(fields) >= 3 {
				if ns, err := strconv.ParseFloat(fields[2], 64); err == nil {
					log.Samples[fields[1]] = append(log.Samples[fields[1]], ns)
				}
			}
		case "RESULT":
			if len(fields) >= 3 {
				if n, err := strconv.ParseInt(fields[2], 10, 64); err == nil {
					log.Results[fields[1]] = n
				}
			}
		default:
			// Every remaining verb is a single scalar value keyed by its
			// lowercased verb (docs, load_ns, used_memory, index_sz, ...).
			if len(fields) >= 2 {
				if n, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
					log.Values[strings.ToLower(fields[0])] = n
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		return RedisLog{}, err
	}
	if !log.present() {
		return RedisLog{}, fmt.Errorf("%s: no redis facts found; incomplete log", path)
	}
	return log, nil
}

// QueryNS returns the representative per-operation nanoseconds for a scenario:
// the minimum over samples after discarding the first (warm-up) repetition
// when more than one sample exists. ok is false when the label is absent.
func (l RedisLog) QueryNS(label string) (float64, bool) {
	ns := l.Samples[label]
	if len(ns) == 0 {
		return 0, false
	}
	if len(ns) > 1 {
		ns = ns[1:]
	}
	best := ns[0]
	for _, t := range ns[1:] {
		if t < best {
			best = t
		}
	}
	return best, true
}

// keyspaceBytes is the RedisJSON keyspace cost: used_memory after the load
// minus the pre-load baseline, so the empty-server overhead is not charged.
func (l RedisLog) keyspaceBytes() int64 {
	d := l.Values["used_memory"] - l.Values["used_memory_base"]
	if d < 0 {
		return 0
	}
	return d
}

// indexBytes is the RediSearch index cost: the used_memory the server grew by
// building the FT.CREATE index over the already-loaded keyspace.
func (l RedisLog) indexBytes() int64 {
	d := l.Values["used_memory_indexed"] - l.Values["used_memory"]
	if d < 0 {
		return 0
	}
	return d
}

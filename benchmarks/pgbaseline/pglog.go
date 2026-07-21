package pgbaseline

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// This file parses the psql session logs produced by run-pg.sh. The logs
// are the raw PostgreSQL artifact: labeled steps (\echo STEP name), psql
// \timing lines, and query output in psql's aligned format. The script does
// no arithmetic; everything numeric is recovered here, testably.

// PGLog is one corpus's parsed PostgreSQL session.
type PGLog struct {
	Version  string
	Settings map[string]string

	// Times collects every "Time: N ms" reading per step label, in order.
	// Repeated query steps therefore carry one sample per repetition.
	Times map[string][]float64

	// Values holds the first numeric result row seen under each step:
	// sizes in bytes, rowcounts, and the count(*) results of the query
	// steps (used to cross-check both engines against the manifest).
	Values map[string]int64

	// Explains holds the raw EXPLAIN (ANALYZE, BUFFERS) text per
	// explain_-prefixed step, kept for the record.
	Explains map[string]string
}

var (
	pgTimeRe  = regexp.MustCompile(`^Time: ([0-9]+\.[0-9]+) ms`)
	pgValueRe = regexp.MustCompile(`^\s*([0-9]+)\s*$`)
	pgCopyRe  = regexp.MustCompile(`^COPY ([0-9]+)$`)
)

// ParsePGLog parses one run-pg.sh session log.
func ParsePGLog(path string) (PGLog, error) {
	f, err := os.Open(path)
	if err != nil {
		return PGLog{}, err
	}
	defer f.Close()

	log := PGLog{
		Settings: map[string]string{},
		Times:    map[string][]float64{},
		Values:   map[string]int64{},
		Explains: map[string]string{},
	}
	step := ""
	prevDashes := false
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if rest, ok := strings.CutPrefix(line, "STEP "); ok {
			step = strings.TrimSpace(rest)
			prevDashes = false
			continue
		}
		if m := pgTimeRe.FindStringSubmatch(line); m != nil {
			ms, _ := strconv.ParseFloat(m[1], 64)
			log.Times[step] = append(log.Times[step], ms)
			continue
		}
		switch {
		case step == "version":
			if strings.Contains(line, "PostgreSQL") && log.Version == "" {
				log.Version = strings.TrimSpace(line)
			}
		case strings.HasPrefix(step, "setting_"):
			// Aligned psql output: header, dashes, value, "(1 row)". The
			// value is the line after the dashes.
			name := strings.TrimPrefix(step, "setting_")
			if prevDashes {
				if _, seen := log.Settings[name]; !seen {
					log.Settings[name] = strings.TrimSpace(line)
				}
			}
		case strings.HasPrefix(step, "explain_"):
			log.Explains[step] += line + "\n"
		}
		if _, seen := log.Values[step]; !seen && step != "" {
			if m := pgValueRe.FindStringSubmatch(line); m != nil {
				n, _ := strconv.ParseInt(m[1], 10, 64)
				log.Values[step] = n
			} else if m := pgCopyRe.FindStringSubmatch(line); m != nil {
				n, _ := strconv.ParseInt(m[1], 10, 64)
				log.Values[step] = n
			}
		}
		prevDashes = strings.HasPrefix(strings.TrimSpace(line), "--")
	}
	if err := sc.Err(); err != nil {
		return PGLog{}, err
	}
	if log.Version == "" {
		return PGLog{}, fmt.Errorf("%s: no PostgreSQL version found; incomplete log", path)
	}
	return log, nil
}

// QueryMS returns the representative wall time for a repeated query step:
// the minimum over samples after discarding the first (warm-up) repetition
// when more than one sample exists. ok is false when the step is absent.
func (l PGLog) QueryMS(step string) (float64, bool) {
	ts := l.Times[step]
	if len(ts) == 0 {
		return 0, false
	}
	if len(ts) > 1 {
		ts = ts[1:]
	}
	best := ts[0]
	for _, t := range ts[1:] {
		if t < best {
			best = t
		}
	}
	return best, true
}

// StepMS returns the single wall-time sample of a one-shot step (COPY,
// CREATE INDEX, VACUUM).
func (l PGLog) StepMS(step string) (float64, bool) {
	ts := l.Times[step]
	if len(ts) == 0 {
		return 0, false
	}
	return ts[0], true
}

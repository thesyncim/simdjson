package duckdbbench

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// DuckDBProfile is the stable subset of DuckDB's JSON profiler output used by
// this harness. Durations are seconds in DuckDB's artifact and are converted
// to nanoseconds only at report time.
type DuckDBProfile struct {
	QueryName              string  `json:"query_name"`
	Latency                float64 `json:"latency"`
	CPUTime                float64 `json:"cpu_time"`
	SystemPeakBufferMemory int64   `json:"system_peak_buffer_memory"`
	SystemPeakTempDirSize  int64   `json:"system_peak_temp_dir_size"`
	CumulativeRowsScanned  int64   `json:"cumulative_rows_scanned"`
	TotalBytesRead         int64   `json:"total_bytes_read"`
	TotalBytesWritten      int64   `json:"total_bytes_written"`
}

// DuckDBLog is one corpus's immutable measurement artifact. Profiles are the
// engine's own high-resolution timings; Values and Results come from facts.log
// and remain independently verifiable against the corpus manifest.
type DuckDBLog struct {
	Image        string
	Version      string
	Platform     string
	CorpusSHA256 string
	Threads      int64

	Values   map[string]int64
	Results  map[string]int64
	Profiles map[string][]DuckDBProfile
}

// ParseDuckDBRun loads results/duckdb/<corpus>. facts.log contains only scalar
// metadata; each <scenario>.profiles.jsons file is a whitespace-separated
// stream of unmodified JSON objects emitted by DuckDB's profiler.
func ParseDuckDBRun(dir string) (DuckDBLog, error) {
	log := DuckDBLog{
		Values:   map[string]int64{},
		Results:  map[string]int64{},
		Profiles: map[string][]DuckDBProfile{},
	}
	if err := parseDuckDBFacts(filepath.Join(dir, "facts.log"), &log); err != nil {
		return DuckDBLog{}, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return DuckDBLog{}, err
	}
	for _, entry := range entries {
		const suffix = ".profiles.jsons"
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), suffix) {
			continue
		}
		label := strings.TrimSuffix(entry.Name(), suffix)
		profiles, err := parseProfileStream(filepath.Join(dir, entry.Name()))
		if err != nil {
			return DuckDBLog{}, fmt.Errorf("%s: %w", label, err)
		}
		if len(profiles) == 0 {
			return DuckDBLog{}, fmt.Errorf("%s: empty profile stream", entry.Name())
		}
		log.Profiles[label] = profiles
	}
	if log.Version == "" || log.Values["docs"] <= 0 {
		return DuckDBLog{}, fmt.Errorf("%s: incomplete DuckDB facts", dir)
	}
	return log, nil
}

func parseDuckDBFacts(path string, log *DuckDBLog) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for line := 1; scanner.Scan(); line++ {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		switch fields[0] {
		case "IMAGE":
			if len(fields) != 2 {
				return fmt.Errorf("%s:%d: IMAGE needs one value", path, line)
			}
			log.Image = fields[1]
		case "VERSION":
			if len(fields) < 2 {
				return fmt.Errorf("%s:%d: VERSION needs a value", path, line)
			}
			log.Version = strings.Join(fields[1:], " ")
		case "PLATFORM":
			if len(fields) != 2 {
				return fmt.Errorf("%s:%d: PLATFORM needs one value", path, line)
			}
			log.Platform = fields[1]
		case "CORPUS_SHA256":
			if len(fields) != 2 {
				return fmt.Errorf("%s:%d: CORPUS_SHA256 needs one value", path, line)
			}
			log.CorpusSHA256 = fields[1]
		case "RESULT":
			if len(fields) != 3 {
				return fmt.Errorf("%s:%d: RESULT needs label and integer", path, line)
			}
			n, err := strconv.ParseInt(fields[2], 10, 64)
			if err != nil {
				return fmt.Errorf("%s:%d: %w", path, line, err)
			}
			log.Results[fields[1]] = n
		default:
			if len(fields) != 2 {
				return fmt.Errorf("%s:%d: scalar fact needs one integer", path, line)
			}
			n, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				return fmt.Errorf("%s:%d: %w", path, line, err)
			}
			key := strings.ToLower(fields[0])
			log.Values[key] = n
			if key == "threads" {
				log.Threads = n
			}
		}
	}
	return scanner.Err()
}

func parseProfileStream(path string) ([]DuckDBProfile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	var profiles []DuckDBProfile
	for {
		var p DuckDBProfile
		err := decoder.Decode(&p)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if p.Latency <= 0 || p.QueryName == "" {
			return nil, fmt.Errorf("invalid profile: query=%q latency=%g", p.QueryName, p.Latency)
		}
		profiles = append(profiles, p)
	}
	return profiles, nil
}

// QueryNS returns the minimum warmed end-to-end latency reported by DuckDB.
// Store uses the same minimum-of-repetitions policy. The runner performs
// unprofiled warmups, so no sample is silently discarded here.
func (l DuckDBLog) QueryNS(label string) (float64, bool) {
	profiles := l.Profiles[label]
	if len(profiles) == 0 {
		return 0, false
	}
	best := profiles[0].Latency
	for _, profile := range profiles[1:] {
		if profile.Latency < best {
			best = profile.Latency
		}
	}
	return best * 1e9, true
}

// MeanNS returns total profiled latency divided by operation count. It is used
// for mutation streams where each statement is one independently committed
// key operation, matching Store.Put and Store.Delete publication semantics.
func (l DuckDBLog) MeanNS(label string) (float64, bool) {
	profiles := l.Profiles[label]
	if len(profiles) == 0 {
		return 0, false
	}
	var seconds float64
	for _, profile := range profiles {
		seconds += profile.Latency
	}
	operations := l.Values["mutation_ops"]
	if operations <= 0 {
		operations = int64(len(profiles))
	}
	return seconds * 1e9 / float64(operations), true
}

func (l DuckDBLog) IndexNS() float64 {
	var ns float64
	for _, label := range []string{"key_index", "filter_index"} {
		if value, ok := l.QueryNS(label); ok {
			ns += value
		}
	}
	return ns
}

func (l DuckDBLog) PeakBufferBytes() int64 {
	var peak int64
	for _, profiles := range l.Profiles {
		for _, profile := range profiles {
			if profile.SystemPeakBufferMemory > peak {
				peak = profile.SystemPeakBufferMemory
			}
		}
	}
	return peak
}

func (l DuckDBLog) PeakTempBytes() int64 {
	var peak int64
	for _, profiles := range l.Profiles {
		for _, profile := range profiles {
			if profile.SystemPeakTempDirSize > peak {
				peak = profile.SystemPeakTempDirSize
			}
		}
	}
	return peak
}

// ProfileLabels provides deterministic diagnostics and test output.
func (l DuckDBLog) ProfileLabels() []string {
	labels := make([]string, 0, len(l.Profiles))
	for label := range l.Profiles {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	return labels
}

// Verify cross-checks all materialized query results and mutation cardinality.
func (l DuckDBLog) Verify(m Manifest) []string {
	wants := map[string]int64{
		"docs":          int64(m.Docs),
		"extract_hits":  int64(m.ExtractHits),
		"after_deletes": int64(m.Docs) - l.Values["mutation_ops"],
	}
	if m.ContainKey != "" {
		wants["filter"] = int64(m.ContainExpected)
		wants["group"] = int64(m.GroupExpected)
		wants["contain"] = int64(m.ContainExpected)
	}
	if m.SumField != "" {
		wants["sum"] = m.SumExpected
	}
	var bad []string
	for label, want := range wants {
		got, ok := l.Results[label]
		if !ok {
			bad = append(bad, fmt.Sprintf("%s missing (want %d)", label, want))
		} else if got != want {
			bad = append(bad, fmt.Sprintf("%s %d, want %d", label, got, want))
		}
	}
	sort.Strings(bad)
	if l.Values["source_bytes"] != m.SourceBytes {
		bad = append(bad, fmt.Sprintf("source bytes %d, want %d", l.Values["source_bytes"], m.SourceBytes))
	}
	if l.Values["key_bytes"] != m.KeyBytes {
		bad = append(bad, fmt.Sprintf("key bytes %d, want %d", l.Values["key_bytes"], m.KeyBytes))
	}
	if l.CorpusSHA256 != m.NDJSONSHA256 {
		bad = append(bad, fmt.Sprintf("corpus SHA-256 %q, want %q", l.CorpusSHA256, m.NDJSONSHA256))
	}
	sort.Strings(bad)
	return bad
}

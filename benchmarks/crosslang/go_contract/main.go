// Command go_contract implements the Go half of the equivalent
// cross-language parse-plus-semantic-digest contract.
package main

import (
	"fmt"
	"math"
	"os"
	"runtime"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/thesyncim/simdjson"
)

var digestSink uint64

func hashByte(hash *uint64, value byte) {
	*hash ^= uint64(value)
	*hash *= 1099511628211
}

func hashUint64(hash *uint64, value uint64) {
	for shift := 0; shift < 64; shift += 8 {
		hashByte(hash, byte(value>>shift))
	}
}

func hashBytes(hash *uint64, value []byte) {
	hashUint64(hash, uint64(len(value)))
	for _, b := range value {
		hashByte(hash, b)
	}
}

func hashString(hash *uint64, value string) {
	hashUint64(hash, uint64(len(value)))
	for i := range len(value) {
		hashByte(hash, value[i])
	}
}

func digestNode(node simdjson.Node, hash *uint64, textScratch *[]byte) bool {
	switch node.Kind() {
	case simdjson.Array:
		hashByte(hash, '[')
		iter, ok := node.ArrayIter()
		if !ok {
			return false
		}
		for {
			child, ok := iter.Next()
			if !ok {
				break
			}
			if !digestNode(child, hash, textScratch) {
				return false
			}
		}
		hashByte(hash, ']')
		return true
	case simdjson.Object:
		hashByte(hash, '{')
		iter, ok := node.ObjectIter()
		if !ok {
			return false
		}
		for {
			key, value, ok := iter.Next()
			if !ok {
				break
			}
			hashByte(hash, 'k')
			text, ok := key.AppendText((*textScratch)[:0])
			if !ok {
				return false
			}
			hashBytes(hash, text)
			*textScratch = text
			if !digestNode(value, hash, textScratch) {
				return false
			}
		}
		hashByte(hash, '}')
		return true
	case simdjson.String:
		hashByte(hash, 's')
		text, ok := node.AppendText((*textScratch)[:0])
		if !ok {
			return false
		}
		hashBytes(hash, text)
		*textScratch = text
		return true
	case simdjson.Number:
		text, ok := node.NumberText()
		if !ok {
			return false
		}
		if strings.ContainsAny(text, ".eE") {
			value, err := strconv.ParseFloat(text, 64)
			if err != nil {
				return false
			}
			hashByte(hash, 'd')
			hashUint64(hash, math.Float64bits(value))
			return true
		}
		if value, err := strconv.ParseInt(text, 10, 64); err == nil {
			hashByte(hash, 'i')
			hashUint64(hash, uint64(value))
			return true
		}
		if value, err := strconv.ParseUint(text, 10, 64); err == nil {
			hashByte(hash, 'u')
			hashUint64(hash, value)
			return true
		}
		hashByte(hash, 'g')
		hashString(hash, text)
		return true
	case simdjson.Bool:
		value, ok := node.Bool()
		if !ok {
			return false
		}
		if value {
			hashByte(hash, 't')
		} else {
			hashByte(hash, 'f')
		}
		return true
	case simdjson.Null:
		hashByte(hash, 'n')
		return true
	default:
		return false
	}
}

func semanticDigest(root simdjson.Node, textScratch *[]byte) uint64 {
	hash := uint64(14695981039346656037)
	if !digestNode(root, &hash, textScratch) {
		panic("semantic digest failed")
	}
	return hash
}

func sampleOperation(operation func()) float64 {
	iterations := 1
	for {
		begin := time.Now()
		for range iterations {
			operation()
		}
		seconds := time.Since(begin).Seconds()
		if seconds > 0.25 || iterations > 1<<22 {
			return seconds * 1e9 / float64(iterations)
		}
		if seconds < 0.02 {
			iterations *= 8
		} else {
			iterations = int(float64(iterations)*0.3/seconds) + 1
		}
	}
}

func median6(operation func()) float64 {
	samples := make([]float64, 6)
	for i := range samples {
		samples[i] = sampleOperation(operation)
	}
	slices.Sort(samples)
	return (samples[2] + samples[3]) / 2
}

func main() {
	dir := "."
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	names := []string{
		"canada_geometry", "citm_catalog", "golang_source", "string_escaped",
		"string_unicode", "synthea_fhir", "twitter_status",
	}

	revision, modified := "unknown", "unknown"
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				revision = setting.Value
			case "vcs.modified":
				modified = setting.Value
			}
		}
	}
	fmt.Printf("Go simdjson equivalent contract, toolchain=%s revision=%s modified=%s\n",
		runtime.Version(), revision, modified)
	for _, name := range names {
		src, err := os.ReadFile(dir + "/" + name + ".json")
		if err != nil {
			panic(err)
		}
		entries, err := simdjson.RequiredIndexEntries(src)
		if err != nil {
			panic(err)
		}
		storage := make([]simdjson.IndexEntry, entries)
		index, err := simdjson.BuildIndex(src, storage)
		if err != nil {
			panic(err)
		}
		textScratch := make([]byte, 0, 256)
		referenceDigest := semanticDigest(index.Root(), &textScratch)

		elapsedNS := median6(func() {
			index, err := simdjson.BuildIndex(src, storage)
			if err != nil {
				panic(err)
			}
			digestSink = semanticDigest(index.Root(), &textScratch)
		})
		fmt.Printf("%-16s size=%8d contract=parse+semantic-digest digest=%016x time=%10.0fns (%6.2f GB/s)\n",
			name, len(src), referenceDigest, elapsedNS, float64(len(src))/elapsedNS)
	}
}

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
	"time"

	"github.com/thesyncim/simdjson"
)

var digestSink uint64

func hashByte(hash uint64, value byte) uint64 {
	return (hash ^ uint64(value)) * 1099511628211
}

func hashUint64(hash uint64, value uint64) uint64 {
	for shift := 0; shift < 64; shift += 8 {
		hash = hashByte(hash, byte(value>>shift))
	}
	return hash
}

func hashBytes(hash uint64, value []byte) uint64 {
	hash = hashUint64(hash, uint64(len(value)))
	for _, b := range value {
		hash = hashByte(hash, b)
	}
	return hash
}

func hashString(hash uint64, value string) uint64 {
	hash = hashUint64(hash, uint64(len(value)))
	for i := range len(value) {
		hash = hashByte(hash, value[i])
	}
	return hash
}

func decodedText(node simdjson.Node, scratch *[]byte) ([]byte, bool) {
	if text, ok := node.StringBytes(); ok {
		return text, true
	}
	text, ok := node.AppendText((*scratch)[:0])
	*scratch = text
	return text, ok
}

func digestNode(node simdjson.Node, hash uint64, textScratch *[]byte) (uint64, bool) {
	switch node.Kind() {
	case simdjson.Array:
		hash = hashByte(hash, '[')
		iter, ok := node.ArrayIter()
		if !ok {
			return hash, false
		}
		for {
			child, ok := iter.Next()
			if !ok {
				break
			}
			hash, ok = digestNode(child, hash, textScratch)
			if !ok {
				return hash, false
			}
		}
		return hashByte(hash, ']'), true
	case simdjson.Object:
		hash = hashByte(hash, '{')
		iter, ok := node.ObjectIter()
		if !ok {
			return hash, false
		}
		for {
			key, value, ok := iter.Next()
			if !ok {
				break
			}
			hash = hashByte(hash, 'k')
			text, ok := decodedText(key, textScratch)
			if !ok {
				return hash, false
			}
			hash = hashBytes(hash, text)
			hash, ok = digestNode(value, hash, textScratch)
			if !ok {
				return hash, false
			}
		}
		return hashByte(hash, '}'), true
	case simdjson.String:
		hash = hashByte(hash, 's')
		text, ok := decodedText(node, textScratch)
		if !ok {
			return hash, false
		}
		return hashBytes(hash, text), true
	case simdjson.Number:
		if !node.IsInteger() {
			value, ok := node.Float64()
			if !ok {
				return hash, false
			}
			hash = hashByte(hash, 'd')
			return hashUint64(hash, math.Float64bits(value)), true
		}
		if value, ok := node.Int64(); ok {
			hash = hashByte(hash, 'i')
			return hashUint64(hash, uint64(value)), true
		}
		if value, ok := node.Uint64(); ok {
			hash = hashByte(hash, 'u')
			return hashUint64(hash, value), true
		}
		text, ok := node.NumberText()
		if !ok {
			return hash, false
		}
		hash = hashByte(hash, 'g')
		return hashString(hash, text), true
	case simdjson.Bool:
		value, ok := node.Bool()
		if !ok {
			return hash, false
		}
		if value {
			hash = hashByte(hash, 't')
		} else {
			hash = hashByte(hash, 'f')
		}
		return hash, true
	case simdjson.Null:
		return hashByte(hash, 'n'), true
	default:
		return hash, false
	}
}

func semanticDigest(root simdjson.Node, textScratch *[]byte) uint64 {
	hash, ok := digestNode(root, uint64(14695981039346656037), textScratch)
	if !ok {
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

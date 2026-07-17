// Command benchpublish turns one benchmark publication run into the committed
// result record, Markdown tables, and SVG charts.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	var (
		root      = flag.String("root", ".", "repository root")
		input     = flag.String("input", "", "directory containing main.txt, hooks.txt, and crosslang.txt")
		count     = flag.Int("count", 6, "samples per Go benchmark")
		benchtime = flag.String("benchtime", "300ms", "time per Go benchmark sample")
		write     = flag.Bool("write", false, "write results, tables, and charts")
		check     = flag.Bool("check", false, "fail if generated files differ")
		verify    = flag.Bool("verify", false, "parse and render without reading or writing generated files")
	)
	flag.Parse()
	if boolCount(*write, *check, *verify) != 1 {
		fatalf("select exactly one of -write, -check, or -verify")
	}
	absRoot, err := filepath.Abs(*root)
	if err != nil {
		fatalf("resolve repository root: %v", err)
	}
	resultPath := filepath.Join(absRoot, "benchmarks", "results", "latest.json")

	var publication Publication
	if *input != "" {
		publication, err = parsePublication(*input, *count, *benchtime)
		if err != nil {
			fatalf("parse publication: %v", err)
		}
	} else {
		data, readErr := os.ReadFile(resultPath)
		if readErr != nil {
			fatalf("read %s: %v", resultPath, readErr)
		}
		if err = json.Unmarshal(data, &publication); err != nil {
			fatalf("decode %s: %v", resultPath, err)
		}
	}
	if *input != "" {
		data, marshalErr := json.MarshalIndent(publication, "", "  ")
		if marshalErr != nil {
			fatalf("encode results: %v", marshalErr)
		}
		data = append(data, '\n')
		if *write {
			if err := os.MkdirAll(filepath.Dir(resultPath), 0o755); err != nil {
				fatalf("create result directory: %v", err)
			}
			if err := os.WriteFile(resultPath, data, 0o644); err != nil {
				fatalf("preserve normalized results: %v", err)
			}
		}
	}
	if err := publication.validate(); err != nil {
		fatalf("invalid publication: %v", err)
	}
	files, err := renderPublication(absRoot, publication)
	if err != nil {
		fatalf("render publication: %v", err)
	}
	if *verify {
		return
	}
	if *write {
		if err := writeFiles(files); err != nil {
			fatalf("write publication: %v", err)
		}
		return
	}
	if err := checkFiles(files); err != nil {
		fatalf("publication is stale: %v", err)
	}
}

func boolCount(values ...bool) int {
	count := 0
	for _, value := range values {
		if value {
			count++
		}
	}
	return count
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "benchpublish: "+format+"\n", args...)
	os.Exit(1)
}

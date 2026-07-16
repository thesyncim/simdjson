// Stage-level corpus benchmark for C++ simdjson: reports stage 1 alone
// (structural index build + UTF-8 validation, no tape) next to the full
// two-stage DOM parse, so the Go side can be compared stage by stage.
// Methodology matches bench_simdjson.cpp: single thread, reused parser,
// six ~300 ms samples per operation, median ns/op reported.
#include "simdjson.h"

#include <chrono>
#include <cstdio>
#include <string>
#include <vector>
#include <functional>
#include <algorithm>

using namespace simdjson;

static double sample_op(const std::function<void()> &op) {
    using clock = std::chrono::steady_clock;
    int iters = 1;
    for (;;) {
        auto begin = clock::now();
        for (int i = 0; i < iters; i++) op();
        double sec = std::chrono::duration<double>(clock::now() - begin).count();
        if (sec > 0.25 || iters > (1 << 22)) {
            return sec * 1e9 / iters;
        }
        iters = (sec < 0.02) ? iters * 8 : (int)(iters * 0.3 / sec) + 1;
    }
}

static double median6(const std::function<void()> &op) {
    std::vector<double> xs;
    for (int r = 0; r < 6; r++) xs.push_back(sample_op(op));
    std::sort(xs.begin(), xs.end());
    return (xs[2] + xs[3]) / 2;
}

int main(int argc, char **argv) {
    const char *dir = argc > 1 ? argv[1] : ".";
    const char *names[] = {
        "canada_geometry", "citm_catalog", "golang_source", "string_escaped",
        "string_unicode", "synthea_fhir", "twitter_status",
    };
    printf("simdjson C++ %s, implementation: ", SIMDJSON_VERSION);
    printf("%s\n", simdjson::get_active_implementation()->name().c_str());
    for (const char *name : names) {
        std::string path = std::string(dir) + "/" + name + ".json";
        padded_string json;
        if (padded_string::load(path).get(json) != SUCCESS) {
            fprintf(stderr, "cannot load %s\n", path.c_str());
            return 1;
        }
        double bytes = (double)json.size();

        dom::parser parser;
        {
            // First parse allocates capacity so stage1 can run standalone.
            dom::element doc;
            if (parser.parse(json).get(doc) != SUCCESS) {
                fprintf(stderr, "parse error on %s\n", path.c_str());
                return 1;
            }
        }

        // Stage 1 alone: UTF-8 validation + structural mask production +
        // flatten_bits position extraction into structural_indexes. No tape,
        // no string unescaping, no number parsing, no grammar validation.
        const uint8_t *buf = (const uint8_t *)json.data();
        size_t len = json.size();
        double stage1_ns = median6([&] {
            if (parser.implementation->stage1(buf, len, stage1_mode::regular) != SUCCESS) std::abort();
        });
        uint32_t n_structurals = parser.implementation->n_structural_indexes;

        // Full DOM parse (stage 1 + stage 2): tape build, string unescape
        // into the arena, numbers parsed to binary.
        double parse_ns = median6([&] {
            dom::element doc;
            if (parser.parse(json).get(doc) != SUCCESS) std::abort();
        });

        printf("%-16s size=%8.0f structurals=%8u (%.4f pos/byte)  stage1=%10.0fns (%6.2f GB/s, %.3f ns/pos)  parse=%10.0fns (%6.2f GB/s)\n",
               name, bytes, n_structurals, n_structurals / bytes,
               stage1_ns, bytes / stage1_ns, stage1_ns / n_structurals,
               parse_ns, bytes / parse_ns);
    }
    return 0;
}

// Cross-language corpus benchmark: C++ simdjson (DOM parse, serialization).
// Mirrors the Go corpus methodology: single thread, reused parser, six
// ~300 ms samples per operation, median ns/op reported.
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
    // Calibrate iteration count to ~300 ms.
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

        // DOM parse with a reused parser: stage 1 + stage 2, tape and
        // string buffer materialized, buffers amortized across iterations.
        dom::parser parser;
        {
            dom::element doc;
            if (parser.parse(json).get(doc) != SUCCESS) {
                fprintf(stderr, "parse error on %s\n", path.c_str());
                return 1;
            }
        }
        double parse_ns = median6([&] {
            dom::element doc;
            if (parser.parse(json).get(doc) != SUCCESS) std::abort();
        });

        // Serialization of the parsed document back to compact JSON.
        dom::element doc;
        (void)parser.parse(json).get(doc);
        std::string out;
        double ser_ns = median6([&] {
            out.clear();
            out = simdjson::to_string(doc);
        });

        printf("%-16s size=%8.0f parse=%10.0fns (%6.2f GB/s)  serialize=%10.0fns (%6.2f GB/s, out=%zu)\n",
               name, bytes, parse_ns, bytes / parse_ns, ser_ns, (double)out.size() / ser_ns, out.size());
    }
    return 0;
}

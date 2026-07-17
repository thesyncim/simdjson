// Equivalent cross-language contract benchmark.
//
// Each iteration reuses parser-owned storage, parses one complete JSON
// document, visits every object key and value in source order, decodes every
// string and number, and produces the language-independent digest defined
// below. The Go companion performs the same externally observable work. The
// runner compares their untimed reference digests before accepting timings.
#include "simdjson.h"

#include <algorithm>
#include <bit>
#include <chrono>
#include <cstdint>
#include <cstdio>
#include <cstdlib>
#include <functional>
#include <string>
#include <string_view>
#include <vector>

using namespace simdjson;

static volatile uint64_t digest_sink;

static inline void hash_byte(uint64_t &hash, uint8_t value) noexcept {
    hash ^= value;
    hash *= UINT64_C(1099511628211);
}

static inline void hash_u64(uint64_t &hash, uint64_t value) noexcept {
    for (int shift = 0; shift < 64; shift += 8) {
        hash_byte(hash, uint8_t(value >> shift));
    }
}

static inline void hash_bytes(uint64_t &hash, std::string_view value) noexcept {
    hash_u64(hash, value.size());
    for (unsigned char byte : value) {
        hash_byte(hash, byte);
    }
}

static bool digest_element(dom::element element, uint64_t &hash) noexcept {
    switch (element.type()) {
    case dom::element_type::ARRAY: {
        hash_byte(hash, '[');
        dom::array array;
        if (element.get_array().get(array) != SUCCESS) return false;
        for (dom::element child : array) {
            if (!digest_element(child, hash)) return false;
        }
        hash_byte(hash, ']');
        return true;
    }
    case dom::element_type::OBJECT: {
        hash_byte(hash, '{');
        dom::object object;
        if (element.get_object().get(object) != SUCCESS) return false;
        for (dom::key_value_pair field : object) {
            hash_byte(hash, 'k');
            hash_bytes(hash, field.key);
            if (!digest_element(field.value, hash)) return false;
        }
        hash_byte(hash, '}');
        return true;
    }
    case dom::element_type::STRING: {
        std::string_view value;
        if (element.get_string().get(value) != SUCCESS) return false;
        hash_byte(hash, 's');
        hash_bytes(hash, value);
        return true;
    }
    case dom::element_type::INT64: {
        int64_t value;
        if (element.get_int64().get(value) != SUCCESS) return false;
        hash_byte(hash, 'i');
        hash_u64(hash, uint64_t(value));
        return true;
    }
    case dom::element_type::UINT64: {
        uint64_t value;
        if (element.get_uint64().get(value) != SUCCESS) return false;
        hash_byte(hash, 'u');
        hash_u64(hash, value);
        return true;
    }
    case dom::element_type::DOUBLE: {
        double value;
        if (element.get_double().get(value) != SUCCESS) return false;
        hash_byte(hash, 'd');
        hash_u64(hash, std::bit_cast<uint64_t>(value));
        return true;
    }
    case dom::element_type::BIGINT: {
        std::string_view value;
        if (element.get_bigint().get(value) != SUCCESS) return false;
        hash_byte(hash, 'g');
        hash_bytes(hash, value);
        return true;
    }
    case dom::element_type::BOOL: {
        bool value;
        if (element.get_bool().get(value) != SUCCESS) return false;
        hash_byte(hash, value ? 't' : 'f');
        return true;
    }
    case dom::element_type::NULL_VALUE:
        hash_byte(hash, 'n');
        return true;
    }
    return false;
}

static uint64_t semantic_digest(dom::element document) noexcept {
    uint64_t hash = UINT64_C(14695981039346656037);
    if (!digest_element(document, hash)) std::abort();
    return hash;
}

static double sample_op(const std::function<void()> &op) {
    using clock = std::chrono::steady_clock;
    int iterations = 1;
    for (;;) {
        auto begin = clock::now();
        for (int i = 0; i < iterations; i++) op();
        double seconds = std::chrono::duration<double>(clock::now() - begin).count();
        if (seconds > 0.25 || iterations > (1 << 22)) {
            return seconds * 1e9 / iterations;
        }
        iterations = seconds < 0.02
            ? iterations * 8
            : int(iterations * 0.3 / seconds) + 1;
    }
}

static double median6(const std::function<void()> &op) {
    std::vector<double> samples;
    for (int sample = 0; sample < 6; sample++) samples.push_back(sample_op(op));
    std::sort(samples.begin(), samples.end());
    return (samples[2] + samples[3]) / 2;
}

int main(int argc, char **argv) {
    const char *dir = argc > 1 ? argv[1] : ".";
    const char *names[] = {
        "canada_geometry", "citm_catalog", "golang_source", "string_escaped",
        "string_unicode", "synthea_fhir", "twitter_status",
    };

    printf("C++ simdjson %s equivalent contract, implementation: %s\n",
           SIMDJSON_VERSION, simdjson::get_active_implementation()->name().c_str());
    for (const char *name : names) {
        std::string path = std::string(dir) + "/" + name + ".json";
        padded_string json;
        if (padded_string::load(path).get(json) != SUCCESS) {
            fprintf(stderr, "cannot load %s\n", path.c_str());
            return 1;
        }

        dom::parser parser;
        dom::element reference;
        if (parser.parse(json).get(reference) != SUCCESS) {
            fprintf(stderr, "parse error on %s\n", path.c_str());
            return 1;
        }
        uint64_t reference_digest = semantic_digest(reference);

        double elapsed_ns = median6([&] {
            dom::element document;
            if (parser.parse(json).get(document) != SUCCESS) std::abort();
            digest_sink = semantic_digest(document);
        });
        printf("%-16s size=%8zu contract=parse+semantic-digest digest=%016llx time=%10.0fns (%6.2f GB/s)\n",
               name, json.size(), (unsigned long long)reference_digest,
               elapsed_ns, double(json.size()) / elapsed_ns);
    }
    return 0;
}

// Cross-language corpus benchmark: Rust serde_json and simd-json.
// Same methodology as the Go corpus: single thread, six ~300 ms samples,
// median ns/op.
use std::time::Instant;

fn sample_op(mut op: impl FnMut()) -> f64 {
    let mut iters: u64 = 1;
    loop {
        let begin = Instant::now();
        for _ in 0..iters {
            op();
        }
        let sec = begin.elapsed().as_secs_f64();
        if sec > 0.25 || iters > 1 << 22 {
            return sec * 1e9 / iters as f64;
        }
        iters = if sec < 0.02 {
            iters * 8
        } else {
            (iters as f64 * 0.3 / sec) as u64 + 1
        };
    }
}

fn median6(mut op: impl FnMut()) -> f64 {
    let mut xs: Vec<f64> = (0..6).map(|_| sample_op(&mut op)).collect();
    xs.sort_by(|a, b| a.partial_cmp(b).unwrap());
    (xs[2] + xs[3]) / 2.0
}

fn main() {
    let dir = std::env::args().nth(1).unwrap_or_else(|| ".".into());
    let names = [
        "canada_geometry", "citm_catalog", "golang_source", "string_escaped",
        "string_unicode", "synthea_fhir", "twitter_status",
    ];
    println!("rust serde_json + simd-json");
    for name in names {
        let bytes = std::fs::read(format!("{dir}/{name}.json")).expect("read corpus");
        let n = bytes.len() as f64;

        // serde_json: dynamic Value decode (owned tree) and re-encode.
        let parse_ns = median6(|| {
            let v: serde_json::Value = serde_json::from_slice(&bytes).unwrap();
            std::hint::black_box(&v);
        });
        let doc: serde_json::Value = serde_json::from_slice(&bytes).unwrap();
        let mut out: Vec<u8> = Vec::with_capacity(bytes.len());
        let enc_ns = median6(|| {
            out.clear();
            serde_json::to_writer(&mut out, &doc).unwrap();
            std::hint::black_box(&out);
        });
        let out_len = out.len() as f64;

        // simd-json: owned value parse (mutates a scratch copy, per its API).
        let mut scratch = bytes.clone();
        let simd_ns = median6(|| {
            scratch.copy_from_slice(&bytes);
            let v = simd_json::to_owned_value(&mut scratch).unwrap();
            std::hint::black_box(&v);
        });
        // simd-json borrowed (zero-copy strings into the scratch buffer).
        let simd_borrow_ns = median6(|| {
            scratch.copy_from_slice(&bytes);
            let v = simd_json::to_borrowed_value(&mut scratch).unwrap();
            std::hint::black_box(&v);
        });

        println!(
            "{name:16} size={n:8.0} serde_parse={parse_ns:9.0}ns ({:5.2} GB/s) serde_encode={enc_ns:9.0}ns ({:5.2} GB/s, out={out_len:.0}) simd_owned={simd_ns:9.0}ns ({:5.2} GB/s) simd_borrowed={simd_borrow_ns:9.0}ns ({:5.2} GB/s)",
            n / parse_ns, out_len / enc_ns, n / simd_ns, n / simd_borrow_ns
        );
    }
}

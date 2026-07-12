# Upstream

- Repository: https://go.googlesource.com/go
- Commit: `d468ad3648be469ffc4090e4586c29709182d6b6`
- Source: `src/encoding/json/internal/jsontest/_embed/*.json.zst`
- Models: `src/encoding/json/internal/jsontest/testdata.go`

The seven payloads are copied byte-for-byte and the concrete models are
mechanically extracted from the same revision. Run
`scripts/check-stdlib-corpus.sh` to detect additions, removals, or content
drift in the pinned Go tree.

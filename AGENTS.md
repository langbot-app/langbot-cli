# Working on lbctl

- Discover the public contract with `lbctl <command> --help` and `lbctl schema --command '<command>'`.
- During edits, run the affected Go package or named test. For CLI flags, requests, output, or exit codes, use the existing tests under `internal/command` and `internal/integration`. Run `make lint` for formatting and vet checks, then `make check` for the full Go suite with race detection.
- For install, upgrade, rollback, or Core HTTP changes, build the candidate with `make build`. Reuse the relevant script in `scripts/` or run the affected command against the target Core. Check the exit code, structured output, and actual post-operation state. Use isolated test instances for writes and lifecycle changes.
- For endpoint normalization, run `go test ./internal/endpoint -run '^FuzzNormalizeIdempotent$'` to replay seeds. Use `go test ./internal/endpoint -fuzz '^FuzzNormalizeIdempotent$' -fuzztime 10s` to explore new inputs and retain any reproducing inputs.
- When an assertion may miss a specific regression, change one behavior in an isolated copy and run the same test against the original and changed code. Report the command, expected behavior, observed result, and verification still pending.

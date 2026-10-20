# Contributing

1. Create a focused branch; keep security fixes separate from unrelated work.
2. Never use real credentials or operating-system credential stores in tests.
   Use synthetic fixtures, temporary homes, and offline fakes where practical.
3. Run `pre-commit run --all-files`, `go test -race ./...`, `go vet ./...`,
   `go build ./...`, `gofmt -l .`, and `git diff --check`. Run installer/release
   tests with `python3 -m unittest discover -s scripts -p '*_test.py'`.
4. Document user-visible behavior and compatibility changes.

Test each invariant at the lowest layer that owns it. Integration tests prove
cross-layer wiring without duplicating the full lower-level matrix. Retain
regressions protecting durable behavior; prefer table-driven cases to repeated
near-identical tests. Keep process/crash, locking, generation, and publication
assertions explicit. Avoid fixture DSLs or assertion frameworks that hide those
security semantics, and consolidate helpers only when they improve readability.

Architecture/security documentation describes current behavior, guarantees,
trust boundaries, and residual risks, not chronological fix history or source
call-site inventories. ADRs remain intentional historical records.

Authentication format changes must fail closed, preserve unknown JSON fields,
and include synthetic redacted fixtures. New logging requires review for
credential exposure. Do not install tools, publish, or deploy implicitly.

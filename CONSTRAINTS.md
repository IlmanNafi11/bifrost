# Constraints

Last reviewed: 2026-11-16 by @bansos

> Agents: read this before writing code. Do not weaken it to make a change pass.
> A dimension with a number and no command in the "Checked by" column is an aspiration, not a constraint.

> All rows that say "changed files" are diff-scoped: run the tool against the files this change touched, never the whole package/repo. Rows that say "per module" or name a package run at their stated scope.

## Floor (always enforced, no setup required)

- No new suppression comments: `@ts-ignore`, `@ts-expect-error`, `eslint-disable`, `//nolint`, `//nolint:...`, `# type: ignore`, `# noqa`.

- No unimplemented stubs: `throw new Error("Not implemented")`, `panic("not implemented")`, empty `catch {}`, `TODO`/`FIXME` standing where the implementation should be.
- No skipped or deleted tests without a reason in the commit message (`.skip`, `t.Skip`, `xtest`, deleted `_test.go`).
- No secrets in source.
- This file does not get weakened to make a change pass.
- Go: no `sed`/perl edits to source; use `edit`/`write`/`ast_edit`.
- Go: no underscores in filenames except `_test.go`; error strings lowercase, no trailing punctuation.

## Enforced with numbers

| Dimension | Rule | Checked by | Runs at |
|-----------|------|------------|---------|
| Types (Go) | Zero type errors | `go build ./...` + `go vet ./...` per module | every edit |
| Types (UI) | Zero type errors | `cd ui && npm run typecheck` (`tsc --noEmit`) | every edit |
| Lint (Go) | Zero errors from `golangci-lint` | `golangci-lint run ./...` (Makefile `lint`) | every edit |
| Lint (UI) | Zero errors from oxlint correctness | `cd ui && npx oxlint` | every edit |
| Format (Go) | `gofmt`/`goimports` clean on changed files | `gofmt -l <changed>` / `goimports -l <changed>` empty | every edit |
| Format (UI) | oxfmt clean on changed files | `cd ui && npm run format:changed:check` (`oxfmt`) | every edit |
| Secrets | No secrets in source | `gitleaks dir . --redact --no-banner` (pre-commit rev v8.16.3) | every edit |
| i18n | Zero hardcoded UI strings / catalog drift | `cd ui && npm run i18n:check` | every edit |
| Coverage (Go) | Touched package must not fall below its recorded floor | `go test ./<pkg>/ -cover` vs ratchet table | task end |
| Coverage (UI) | Changed files ≥ 80% covered | `npx vitest run --coverage` (needs `@vitest/coverage-v8`) | task end |
| Security: code | Nothing at high or above | `semgrep scan --config p/default --strict --json` (install: `pipx install semgrep`) | CI |
| Security: deps | No known-vulnerable dependency introduced | Snyk Open Source + dependency-review (existing CI) | CI |
| Architecture (UI) | No import cycles, no forbidden boundary crossing | `cd ui && npx depcruise --config .dependency-cruiser.js app` + existing `oxlint import/no-cycle` | CI |

## Measured, not yet enforced

Ratchet: these values must not fall. When a number improves, update it here; when it drops, that is the finding.

| Metric | Today | Direction |
|--------|-------|-----------|
| `core/schemas` coverage | 62.3% | must not fall |
| `core/providers/utils` coverage | 75.3% | must not fall |
| UI coverage | unmeasured (node_modules not installed) | measure on first `npm ci`, then hold |

## Exceptions

| ID | Rule | Path | Reason | Owner | Expires |
|----|------|------|--------|-------|---------|
| — | — | — | none recorded | — | — |

## Placement by cost

| Phase | Command | Budget |
|-------|---------|--------|
| EDIT | `gofmt -l`, `goimports -l`, `go vet`, `golangci-lint run`, `gitleaks dir . --redact`, `npm run typecheck`, `npx oxlint`, `npm run i18n:check` | seconds, changed files only |
| TASK | related `go test ./<pkg>/` + package coverage, `npx vitest run --coverage` | ≤ 300 s |
| REVIEW/CI | `semgrep scan`, Snyk, `cd ui && npx depcruise --config .dependency-cruiser.js app`, harness structural validation (`augment-provider-harness.mjs` + `filter-collection.mjs`) | minutes |

Scope to the diff; never the whole repo. Slowest check before handing work back: **300 s** at task end, unlimited in CI. A failing check **blocks** the agent mid-task.

## Commands (reproducible without an agent)

```bash
# Edit loop
node guard.mjs --base HEAD                       # floor guard (exit 0 clean, 1 violation, 2 cannot-run)
make lint && gofmt -l <changed>                 # golangci-lint + gofmt (Go, changed files only)
cd ui && npm run typecheck && npx oxlint && npm run i18n:check && npm run format:changed:check
gitleaks dir . --redact --no-banner

# Task end
cd core    && go test ./schemas/ ./proxypool/ ./providers/... -cover
cd framework && go test ./configstore/... -cover
cd transports && go test ./bifrost-http/handlers/ -cover
cd ui && npx vitest run --coverage

# CI / review
semgrep scan --config p/default --strict --json
cd ui && npx depcruise --config .dependency-cruiser.js app
node tests/e2e/api/runners/augment-provider-harness.mjs --source tests/e2e/api/collections/provider-harness.json --out tmp/harness-augmented.json
node tests/e2e/api/runners/filter-collection.mjs --source tmp/harness-augmented.json --out tmp/filtered.json --feature "<keyword>"
```

Install commands for missing tools (recorded; machine-wide installs don't persist in the agent sandbox, so CI/pre-commit remain the enforcement point):

```bash
pipx install semgrep
cd ui && npm i -D dependency-cruiser @vitest/coverage-v8
```

## Notes on the current branch (2026-11-16)

- `framework/configstore` does not build on this branch: `alias.BasetenAliasCfg undefined (schemas.AliasConfig)`. Pre-existing cross-module drift, tracked outside this file; not a constraint to relax — fix it in the proxy-pool work (Task 2/3 touch this module).
- `transports/bifrost-http` references `github.com/maximhq/bifrost/core/streamusage`, which is not yet provided by the local `core` module — also pre-existing, resolves via `go mod tidy` in the module dir.

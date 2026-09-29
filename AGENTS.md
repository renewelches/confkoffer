# AGENTS.md

Instructions for AI coding agents working in this repository. Humans: see
[README.md](README.md).

confkoffer is a Go CLI that bundles, encrypts, and ships project
configuration files to an object store (S3-compatible, Azure Blob, GCS, or
a local directory) and reverses the flow on retrieval. It handles
passphrases and plaintext secrets, so treat security as the default
concern in every change.

## Commands

Use the Makefile targets; CI runs the same ones.

| Task | Command |
|---|---|
| Build (`bin/confkoffer`) | `make build` |
| Test | `make test` |
| Test with race detector | `make test-race` |
| Vet | `make vet` |
| Vulnerability scan | `make vuln` |
| Tidy modules | `make tidy` |
| Run locally | `make run ARGS="..."` |

Go version comes from `go.mod`. `govulncheck` installs with
`go install golang.org/x/vuln/cmd/govulncheck@latest`.

## Layout

- `cmd/confkoffer/` — entry point only.
- `internal/cli/` — cobra commands.
- `internal/archive/`, `internal/crypto/`, `internal/store/` — the
  pack → encrypt → upload pipeline. `store` is built on `gocloud.dev/blob`.
- `internal/config/`, `internal/password/`, `internal/scan/`,
  `internal/logging/` — supporting packages.
- `internal/version/` — populated at link time via `-ldflags`; do not
  hardcode version, commit, or date.

## Vulnerability policy

### At the start of a session

Before starting the requested task, run `make vuln` once. If it reports
vulnerabilities **reachable from this code** ("Your code is affected by N
vulnerabilities"), tell the user in your first reply, list the module,
found version, and fixed version for each, and offer to fix them. Do not
fix them unasked in the middle of an unrelated task.

Findings listed as "in modules you require, but your code doesn't appear
to call" are informational. They do not fail `make vuln` and need no
action.

### Before every commit

Run, in order, and do not commit unless all pass:

1. `make vet`
2. `make test-race`
3. `make vuln`

If `make vuln` reports reachable vulnerabilities, fix them in the same
change:

1. For each affected module, `go get <module>@<fixed version>`, using the
   "Fixed in" version from the report. This works for `// indirect`
   modules too: a dependency's `go.mod` sets a minimum, not a pin, so
   raising the version here does not require waiting for upstream.
2. `go mod tidy`
3. Re-run `make vet`, `make test-race`, `make vuln`.
4. Commit the `go.mod`/`go.sum` changes and tell the user which modules
   moved, from which version to which.

**Stop and ask the user instead** when no fixed version exists, the fix
requires a new major version (`/v2` import path), or the upgrade breaks
the build or tests. Never silence a finding, pin around it with a
`replace` directive, or skip the scan without the user's explicit
approval.

If `make vuln` cannot run (offline, tool missing), say so; do not report
the change as scanned.

## Security rules

- Never log, print, or include in errors: passphrases, derived keys, or
  plaintext file contents.
- Do not weaken crypto parameters, blob/entry size caps, or plaintext
  wiping without the user asking for it explicitly. These live across
  `internal/crypto/`, `internal/archive/`, `internal/store/`,
  `internal/password/`, and the `pack`/`unpack` commands in `internal/cli/`.
- Never commit `*.old`, `*.orig`, or `*.bak` copies of files; they are
  gitignored because they tend to hold credential-handling code.

## Commits

Conventional Commits with a scope where it fits: `feat(store): ...`,
`fix(archive): ...`, `docs: ...`, `build: ...`, `ci: ...`. Imperative mood,
lowercase summary. Keep dependency bumps in their own commit when they are
not required by the change itself.

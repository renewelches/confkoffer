# confkoffer — Implementation Plan

## Context

Greenfield Go CLI in `/Users/rene/Documents/Workspace/confkoffer/`. Bundles, encrypts, and ships configuration files (or any project files) to an S3-compatible bucket, and reverses the flow on retrieval. Project-agnostic — not tied to Terraform, Ansible, or any specific tool. The motivation is to keep sensitive files (provider credentials, backend configs, secrets, environment files) safely backed up off-machine without depending on external tooling like `gpg` or `zip`.

Name origin: **conf**iguration + **koffer** (German for suitcase / luggage), playing on the English word *coffer* (strongbox). Pronounceable in either language; evokes "trusted suitcase for your configs."

Subcommands use the suitcase metaphor: `pack`, `unpack`, `init`.

## Project Layout

```
confkoffer/
├── go.mod
├── go.sum
├── Makefile
├── README.md
├── PLAN.md                           # this document
├── main.go                           # thin entrypoint -> cmd.Execute()
├── cmd/
│   ├── root.go                       # cobra root, global flags, slog init
│   ├── pack.go                       # `pack` subcommand (was upload)
│   ├── unpack.go                     # `unpack` subcommand (was download)
│   └── init.go                       # `init` subcommand — scaffold config
├── internal/
│   ├── config/
│   │   ├── config.go                 # YAML load + flag/env merge, resolution order
│   │   ├── patterns.go               # include/exclude evaluation
│   │   └── validate.go               # name + schema validation
│   ├── scan/
│   │   └── scan.go                   # recursive walk + glob matching
│   ├── archive/
│   │   └── zip.go                    # in-memory zip writer/reader, preserves perms
│   ├── crypto/
│   │   └── crypto.go                 # argon2id + AES-GCM, self-describing header v0x02
│   ├── store/
│   │   └── s3.go                     # minio-go client, retry w/ exponential backoff
│   ├── password/
│   │   ├── source.go                 # Source interface
│   │   ├── flag.go                   # --pass value
│   │   ├── env.go                    # CONFKOFFER_PASS
│   │   ├── prompt.go                 # x/term, no-echo, double-confirm on pack
│   │   ├── pass.go                   # passwordstore.org integration
│   │   └── command.go                # universal exec source (Vault/1Password/etc.)
│   └── logging/
│       └── logging.go                # slog setup; secret-scrubbing handler
└── internal/*/.._test.go             # unit tests
```

## Module & Dependencies

```
module confkoffer
go 1.21
```

Direct deps:
- `github.com/spf13/cobra` — CLI framework
- `github.com/minio/minio-go/v7` — S3-compatible client
- `github.com/gobwas/glob` — `**` glob support
- `gopkg.in/yaml.v3` — config file parsing
- `golang.org/x/crypto/argon2` — KDF
- `golang.org/x/term` — no-echo password prompt

**Stdlib for everything else**: `log/slog` (drops logrus), `crypto/aes`, `crypto/cipher`, `crypto/rand`, `crypto/sha256`, `archive/zip`, `encoding/binary`, `os/exec`.

## Encrypted Blob Format (version 0x02)

35-byte cleartext header followed by AES-256-GCM ciphertext+tag. The header is **self-describing**: each blob carries the KDF parameters used to encrypt it. This means changing the project's KDF config later does not break old blobs — every blob decrypts with its own recorded parameters.

```
+-------------------------------+
|  1 byte : version = 0x02      |
|  4 bytes: argon2id memory KiB | (uint32 big-endian)
|  1 byte : argon2id time       |
|  1 byte : argon2id threads    |
| 16 bytes: salt                | (random per blob, crypto/rand)
| 12 bytes: AES-GCM nonce       | (random per blob, crypto/rand)
+-------------------------------+
| N bytes : AES-256-GCM(ZIP)    | (ciphertext + 16-byte auth tag)
+-------------------------------+
```

**The full 35-byte header is passed as Associated Data (AAD) to AES-GCM.** This makes the header tamper-evident: any modification (e.g., weakening the recorded `time` parameter) causes decryption to fail.

### KDF defaults (OWASP second-choice)

```
memory: 19456 KiB (19 MiB)
time:   2
threads: 1
```

Override via `crypto.argon2id` block in `.confkoffer.yaml`. No CLI flag for KDF params (config-only — reduces foot-gun surface).

### Decryption

1. Read first 35 bytes → parse header.
2. Re-derive key with `argon2.IDKey(password, salt, time, memory, threads, 32)`.
3. AES-GCM `Open(ciphertext, nonce, aad=header)`.
4. GCM tag mismatch ⇒ `decryption failed: invalid password or corrupted blob` (exit 1).

## S3 Object Key Convention

```
<name>/<RFC3339-utc-Z>-<host6>-<rand4>.enc
```

Example: `myproj/2026-04-28T12:34:56Z-a3f91c-7d4e.enc`

- `<name>`: project name (required, lowercase alphanumeric + dashes — `[a-z0-9-]+`).
- `<RFC3339-utc-Z>`: `time.Now().UTC().Format(time.RFC3339)`. Always UTC. Colons replaced with `-` for filesystem-safe semantics.
- `<host6>`: first 6 hex chars of `sha256(os.Hostname())`. Stable per machine, opaque (no hostname leak). Falls back to `unknown` if `os.Hostname()` errors.
- `<rand4>`: 4 hex chars from `crypto/rand`. Eliminates same-second-same-machine collisions.

**Listing semantics**: `unpack` calls `ListObjects(prefix=<name>/)` and sorts **by `LastModified`** descending — the server clock is authoritative. The key timestamp is a human-readable label only. Empty listing ⇒ exit 1 with `no snapshots found for project <name>`.

**Info-leak note**: the hostname hash in the key is opaque but is still a per-machine fingerprint. README will document a future option to push the fingerprint into the encrypted header instead, for users with zero leak tolerance.

## Retention

**Deferred to S3 lifecycle rules.** The tool only needs `s3:PutObject`, `s3:ListBucket`, and `s3:GetObject` — no delete permission required. README documents how to configure a bucket lifecycle policy to expire old snapshots.

## YAML Config Schema (`.confkoffer.yaml`)

Lives in CWD. **No walk-up of parent directories** — discovery is CWD-only. Override path with `--config <path>`.

```yaml
# .confkoffer.yaml — project root
name: my-project              # required; matches CONFKOFFER_NAME

storage:                      # all optional; CLI flag and env override
  bucket: my-backups
  endpoint: s3.amazonaws.com
  region: eu-central-1
  prefix: ""                  # optional sub-prefix inside <name>/

crypto:                       # optional; defaults to OWASP second-choice
  argon2id:
    memory_kib: 47104         # OWASP first-choice: stronger
    time: 1
    threads: 1

patterns:
  include:
    - "**/*.tf"
    - "**/*.tfvars"
    - "secrets/prod.env"      # literal paths also work
  exclude:
    - "**/*.tfstate"
    - ".terraform/**"

password:                     # optional; default chain: flag -> env -> prompt
  source: pass                # one of: prompt | env | flag | pass | command
  pass:
    path: backups/confkoffer/my-project
  # OR universal exec source:
  # source: command
  # command:
  #   argv: ["op", "read", "op://Personal/confkoffer/password"]
  #   timeout: 10s
```

**Resolution order (any field):** CLI flag > env var > config file > built-in default.

## Pattern Semantics

Drops the inverted-gitignore cleverness from the original plan. Now uses **explicit** include/exclude lists from YAML — unambiguous, supports both globs and literal paths.

1. `patterns.include` — list of glob patterns (or literal paths) under the source dir. A file is a candidate iff it matches at least one include.
2. `patterns.exclude` — list of patterns. A candidate is dropped iff it matches at least one exclude.
3. Globs use `gobwas/glob` with `**` support, evaluated against the file's path relative to `--source-dir`.
4. Symlinks are skipped (logged at WARN with the path).

## Password Subsystem

Pluggable `Source` interface so new password managers can be added without refactoring:

```go
type Source interface {
    Get(ctx context.Context) ([]byte, error)  // []byte so we can zero it
    Name() string                              // for logging — never values
}
```

**v1 implementations:**

| Source   | Use case                          | Notes |
|----------|-----------------------------------|-------|
| `flag`   | `--pass <value>`                  | Trivial wrapper |
| `env`    | `CONFKOFFER_PASS`                 | `os.Getenv` |
| `prompt` | Interactive (no echo)             | Double-confirm on `pack`; 3 attempts then exit 2 |
| `pass`   | passwordstore.org                 | `exec("pass", "show", path)`; passes through stderr for GPG agent prompts |
| `command`| Universal exec escape hatch       | Run any command, read stdout. Covers Vault wrapper, 1Password (`op`), Bitwarden (`bw`), etc. |

**Roadmap (deferred):** `vault` typed source (Hashicorp Vault HTTP API with AppRole auth, lease renewal). Adding it is a new file in `internal/password/`, no interface changes.

**Default chain** when `password:` is omitted in config: `flag → env → prompt`.

**Security:**
- All password and key material flows as `[]byte`, zeroed after use (best-effort in Go).
- `command.argv` never logged (could contain secret-looking flags). Only `Source.Name()` is logged.
- README documents: for automated/cron backups, use `pass` or `command` — never `--pass` or env vars.

## Workflow Detail

### `pack`
1. Resolve config (flag > env > YAML > defaults). Bail with exit code **2** on missing required (name, bucket, endpoint).
2. Walk `--source-dir` (default `.`) recursively. Apply include/exclude rules. Skip symlinks (warn).
3. Stream into in-memory zip writer (`archive/zip`), preserving each file's `os.FileMode`.
4. Get password from configured `Source` (with double-confirm if `prompt`).
5. Generate random salt + nonce. Derive key via argon2id with config params.
6. AES-256-GCM seal with header as AAD. Build full blob.
7. PUT to S3 at `<name>/<timestamp>-<host6>-<rand4>.enc` with `application/octet-stream`. Retry 3× with exponential backoff (500ms → 2s → 8s) on transient errors.
8. Log object key + size at INFO. **Never log password, derived key, salt-as-secret, or `command.argv`.**

### `unpack`
1. Resolve config. Bail with exit code **2** on missing required.
2. List `<name>/` prefix on bucket. Sort by `LastModified` desc. Pick newest. Empty ⇒ exit 1.
3. (If `--object-key` set, skip list and fetch by key directly.)
4. GET object body (with retry).
5. Read 35-byte header. Get password. Re-derive key with header's recorded params.
6. AES-GCM open with header as AAD. On tag mismatch ⇒ exit 1 with `decryption failed: invalid password or corrupted blob`.
7. Open zip reader on plaintext.
8. For each entry: compute target path under `--output-dir` (default CWD). Skip-if-exists unless `--overwrite`. Write with original mode.
9. Refuse entries whose cleaned path escapes the output dir (zip-slip protection: `filepath.Clean` + prefix check).

### `init`
1. Refuse if `.confkoffer.yaml` exists in CWD unless `--force`.
2. Write a template config with sensible defaults and commented-out examples (KDF override, password sources, exclude patterns).
3. Print next steps (set env vars, edit patterns, run `pack`).

## CLI Flags Summary

**Global (root):**
- `--log-level` (info|debug|warn|error, default info)
- `--region` (overrides config; default `us-east-1`)
- `--config` (path; default `./.confkoffer.yaml`)

**`pack`:**
- `--name` (env `CONFKOFFER_NAME`)
- `--bucket` (env `CONFKOFFER_BUCKET`)
- `--endpoint` (env `AWS_ENDPOINT`)
- `--pass` (env `CONFKOFFER_PASS`) — sensitive
- `--source-dir` (default `.`)

**`unpack`:**
- `--name`, `--bucket`, `--endpoint`, `--pass` (same as `pack`)
- `--output-dir` (default `.`)
- `--overwrite` (bool, default false)
- `--object-key` (optional; if set, skips list-pick-newest)

**`init`:**
- `--force` (bool, default false)

S3 credentials always come from env (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`) — never accepted on the CLI to avoid shell-history leaks.

## Logging & Security

- `log/slog` with `slog.NewTextHandler(os.Stderr, ...)`. Level from `--log-level`.
- Custom `slog.Handler` (or `ReplaceAttr` func) that scrubs any field literally named `pass`, `password`, `key`, `secret`, or `argv`.
- Password sources log `Source.Name()` only — never the value.
- Key material and password bytes are wiped (`for i := range b { b[i] = 0 }`) after use.

## Exit Codes

- `0` — success
- `1` — general runtime error (network, decrypt fail, no snapshot found, file IO, password manager exec failure)
- `2` — config error (missing required fields, malformed YAML, name validation failed, prompt retries exceeded)

## Tests

Every package gets unit-test coverage. Targets ≥80% line coverage for `internal/*`; `cmd/` lower (mostly wiring).

**`internal/config/`**
- `config_test.go` — YAML parse round-trip; flag/env/config/default precedence resolution; missing required fields surface as exit-2 errors; `--config` override path; CWD discovery (file present vs absent).
- `patterns_test.go` — table-driven include/exclude evaluation; literal paths vs `**` globs; symlink skipping; precedence (exclude wins over include).
- `validate_test.go` — `name` regex (`[a-z0-9-]+`); rejection of empty / uppercase / slash / whitespace; schema rejection on unknown keys.

**`internal/scan/`**
- `scan_test.go` — uses `t.TempDir()` to build a fake source tree; verifies recursion, glob matching, exclude precedence, symlink skip-with-warn, hidden file handling.

**`internal/archive/`**
- `zip_test.go` — round-trip preserves bytes and `os.FileMode`; zip-slip paths (`../`, absolute paths, symlink-equivalent) rejected; large file handling (≥10 MiB sample); empty archive case.

**`internal/crypto/`**
- `crypto_test.go` — encrypt → decrypt round-trip; wrong password fails with the canonical error; tampered ciphertext fails GCM auth; **tampered header fails AAD check** (modify recorded `time` or `memory` byte); unsupported version byte rejected; deterministic output is *not* expected (verifies salt/nonce randomness across runs).
- `header_test.go` — header serialize/parse round-trip; truncated header rejected; min-size validation.

**`internal/store/`**
- `s3_test.go` — uses `httptest.Server` to simulate S3 endpoint responses; verifies retry-with-backoff on 5xx; verifies abort on 4xx; key construction; `LastModified`-sorted listing; "no snapshots found" path.
- Manual integration test against real MinIO documented in README (not in CI).

**`internal/password/`**
- `prompt_test.go` — double-confirm happy path; mismatch retry; 3-attempt exhaustion exits 2; uses an `io.Reader` injected into the prompt to feed scripted input (avoids needing a real TTY).
- `pass_test.go` — mocked `exec.Cmd` (via interface boundary or `PATH` shim) returning known stdout; verifies command invocation shape; verifies failure modes (missing entry, gpg-agent timeout).
- `command_test.go` — mocked exec; `argv` substitution; timeout enforcement; **assertion that `argv` never appears in any captured log output** (regression guard for the secret-leak rule).
- `chain_test.go` — default chain (`flag → env → prompt`) tries sources in order, stops on first non-empty.

**`internal/logging/`**
- `logging_test.go` — captures `slog` output to a buffer; asserts that records containing fields literally named `pass`, `password`, `key`, `secret`, `argv` have those values redacted; level filtering works.

**`cmd/`**
- `pack_test.go`, `unpack_test.go`, `init_test.go` — exercise command wiring with a fake config + fake `password.Source` + fake S3 store. Focuses on flag parsing, error-to-exit-code mapping, and that the right components are invoked. Heavy lifting stays in `internal/*` tests.

**Test infrastructure conventions**
- Use stdlib `testing` only — no `testify`, no `ginkgo`. Keeps deps minimal.
- Table-driven where applicable (`tests := []struct{ name string; ... }{ ... }`).
- `t.TempDir()` for any filesystem work; never write outside it.
- No network in unit tests; `httptest` for HTTP.
- Race detector enabled in CI: `go test -race ./...`.
- Coverage gate: `go test -coverprofile=cover.out ./... && go tool cover -func=cover.out`.

## Makefile Targets

```
make build       # go build -o bin/confkoffer ./
make test        # go test ./...
make test-race   # go test -race ./...
make test-cover  # go test -coverprofile=cover.out ./... && go tool cover -func=cover.out
make run         # go run ./ <args>
make tidy        # go mod tidy
make clean       # rm -rf bin/ cover.out
```

## README Sections

1. **Overview & threat model** — protects against bucket exfiltration; password is the trust anchor; recommend long passphrase (≥4 random words or ≥16 chars).
2. **Install / build.**
3. **Quickstart**: `confkoffer init`, edit `.confkoffer.yaml`, `confkoffer pack`, `confkoffer unpack --output-dir restored/`.
4. **Config file reference** — full schema with examples.
5. **Password manager integration** — `pass` and `command` examples; security guidance ("for automation, never use `--pass` or env vars").
6. **Env var reference table.**
7. **Exit codes table.**
8. **S3 lifecycle rules** — recommended retention policy.
9. **MinIO quickstart for local testing.**
10. **Roadmap** — Hashicorp Vault password source; option to move host fingerprint into encrypted header.

## Verification

End-to-end checks after implementation:

1. `make tidy && make build` — must compile clean.
2. `make test` — unit tests pass.
3. Spin up local MinIO:
   ```
   docker run -p 9000:9000 -e MINIO_ROOT_USER=minio -e MINIO_ROOT_PASSWORD=minio12345 minio/minio server /data
   ```
4. Create scratch dir with sample `.tf` / `.tfvars` / `.tfstate` files. Run `confkoffer init`. Edit `.confkoffer.yaml` to exclude `**/*.tfstate`.
5. Pack:
   ```
   AWS_ENDPOINT=http://localhost:9000 \
   AWS_ACCESS_KEY_ID=minio \
   AWS_SECRET_ACCESS_KEY=minio12345 \
   CONFKOFFER_BUCKET=tf-conf \
   CONFKOFFER_PASS=hunter2-but-longer \
   ./bin/confkoffer pack
   ```
   Confirm new object appears at `myproj/<timestamp>-<host6>-<rand4>.enc` in MinIO console.
6. Wipe the scratch dir; run `confkoffer unpack --output-dir restored/`. Confirm `.tf`/`.tfvars` restored, `.tfstate` not in archive.
7. Re-run `unpack` without `--overwrite` over existing files — confirm "skipped (exists)" messages, files untouched.
8. Run `unpack` with wrong password — confirm exit code 1 and `decryption failed` message.
9. Tamper a single byte in the downloaded blob's header — confirm AAD check fails decryption.
10. `pack` twice in the same second from the same machine — confirm both objects exist (rand4 suffix differentiates).
11. Configure `password.source: pass` in YAML, store password with `pass insert backups/confkoffer/my-project`, run `pack` — confirm it fetches password without prompting.

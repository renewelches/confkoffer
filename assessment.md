# confkoffer — Code Review & Feature Gap Assessment

_Generated: 2026-07-24_

---

## 1. Code Improvement Suggestions

### 1.1 Memory hygiene — plaintext buffer never zeroed

**Files:** `internal/cli/pack.go`, `internal/cli/unpack.go`

The password byte slice is correctly wiped via `defer wipe(pw)`, but the plaintext buffer — which holds the full contents of every packed/unpacked file — is never zeroed after use. The `crypto.Zero` helper is already exported for exactly this purpose.

**pack.go** — add immediately after assigning `plaintext`:
```go
plaintext, err := buildArchive(matches)
if err != nil { return err }
defer crypto.Zero(plaintext)
```

**unpack.go** — add immediately after decryption:
```go
plaintext, err := crypto.Decrypt(blob, pw)
if err != nil { return err }
defer crypto.Zero(plaintext)
```

GitHub issue: #6

---

### 1.2 Unbounded reads — `io.ReadAll` and zip entry extraction

**Files:** `internal/store/s3.go`, `internal/archive/zip.go`

`store.Get` calls `io.ReadAll` with no size cap. A malicious or accidentally oversized S3 object will exhaust heap memory. The zip extractor then pipes each entry through `io.Copy` with no per-entry limit, creating a zip-bomb amplification path.

Recommended cap: `256 MiB` for the blob, `128 MiB` per zip entry. Make both configurable via config/env.

GitHub issue: #7

---

### 1.3 Silent TLS downgrade when using `http://` endpoint

**File:** `internal/store/s3.go:normalizeEndpoint`

An `http://` endpoint silently disables TLS — no log message, no stderr warning. AWS credentials flow in cleartext. Should emit at minimum a `slog.Warn`. Consider refusing non-localhost `http://` endpoints unless a dedicated `--insecure` flag is set.

GitHub issue: #8

---

### 1.4 Stale README documentation — `host6` references

**File:** `README.md`

The README still describes the `host6` component in the S3 key format (including the "S3 object key layout" section and the "Subcommands" table). The hostname was removed from the key format in the recent fix for issue #5. These sections are now factually wrong and should be updated.

Also: the roadmap item "Host-fingerprint-in-header option" is now moot since the fingerprint was removed entirely.

---

### 1.5 `config.Load` — unnecessary string conversion of file bytes

**File:** `internal/config/config.go:193`

```go
dec := yaml.NewDecoder(strings.NewReader(string(data)))
```

`os.ReadFile` returns `[]byte`. Converting it to `string` to then wrap in `strings.NewReader` makes an unnecessary allocation. Use `bytes.NewReader(data)` directly.

---

### 1.6 `unpack.go` — `pickKey` unsafe index access is safe but fragile

**File:** `internal/cli/unpack.go:128`

```go
return objs[0].Key, nil // newest
```

This is currently safe because `List` returns `ErrNoSnapshots` before returning an empty slice. However, if `List` semantics ever change, this will panic at runtime with no diagnostic. A bounds check with a clear error message would make the contract explicit.

---

### 1.7 `scan.go` — insertion sort is fine for small inputs, but undocumented limit

**File:** `internal/scan/scan.go:158`

The comment says "real input sizes are small". There is no enforcement of that assumption. A project directory with 50 000 small config fragments would complete but would be O(n²). Consider switching to `slices.SortFunc` (stdlib since Go 1.21) and removing the comment, or add a max-files guard with a clear error.

---

### 1.8 No context propagation through `buildArchive`

**File:** `internal/cli/pack.go:106`

`buildArchive` opens and reads files synchronously with no context. A `pack` on a large tree can't be cancelled via Ctrl-C between file reads. Passing `ctx` through and checking `ctx.Err()` in the loop would fix this.

---

### 1.9 `PasswordOverride` in `Config` struct is never zeroed

**File:** `internal/config/config.go`, `internal/cli/root.go`

`cfg.PasswordOverride` is populated from the `--pass` CLI flag (a Go string — immutable, can't be zeroed at the source) and stored as `[]byte` in the Config struct. The struct is passed through `loadAndResolveConfig` and into the CLI handlers, where the byte slice is used to construct a `FlagSource`. After the source is built, `cfg.PasswordOverride` is never explicitly zeroed. It's a `[]byte` so `crypto.Zero(cfg.PasswordOverride)` could be deferred in `runPack` / `runUnpack` after `buildPasswordSource` returns.

---

### 1.10 `buildArchive` holds all file handles open until after zip close

**File:** `internal/cli/pack.go:106-119`

The pattern:
```go
f, err := os.Open(m.AbsPath)
// ...
err = w.Add(m.RelPath, m.Mode.Perm(), f)
_ = f.Close()
```

`w.Add` calls `io.Copy` into the in-memory zip writer immediately, so the file is read and closed in the same loop iteration. This is correct. No issue, just confirming the pattern is safe.

---

### 1.11 `list` output has no file count or encryption metadata

**File:** `internal/cli/list.go`

The `list` command shows `LAST_MODIFIED`, `SIZE`, and `KEY`. Comparable tools (SOPS, Vault) show richer metadata per snapshot. Consider adding a `--verbose` flag that downloads and decodes only the blob header (35 bytes via a range-GET) to display KDF parameters and file count without a full decrypt.

---

## 2. Security Analysis Summary

| # | Finding | Severity | Status |
|---|---------|----------|--------|
| GH #5 | `host6` in S3 key leaks hostname fingerprints | Medium | **Closed/Fixed** |
| GH #6 | Plaintext buffer not zeroed after pack/unpack | Medium | Open |
| GH #7 | Unbounded `io.ReadAll` in `Get` + zip bomb via `io.Copy` | Medium | Open |
| GH #8 | `http://` endpoint silently disables TLS, credentials in cleartext | Medium | Open |

### Crypto assessment

The cryptographic design is sound:
- **AES-256-GCM** with fresh random nonce per blob — correct.
- **Argon2id** KDF at OWASP second-choice defaults — appropriate.
- **AAD over the full 35-byte header** — prevents parameter-downgrade attacks.
- **ErrDecryption** returned uniformly on auth failure — no oracle leakage.
- **`crypto.Zero`** exported and used for keys and passwords — good hygiene, just not applied to plaintext (issue #6).
- **`credentials.NewEnvAWS()`** — credentials never hit CLI flags — correct.

No issues found in the cryptographic core.

---

## 3. Feature Gap Research — Similar Tools

Evaluated against: **SOPS** (Mozilla), **git-crypt**, **age**, **Ansible Vault**, **BlackBox** (StackExchange), **transcrypt**, **HashiCorp Vault**.

### 3.1 Features confkoffer has that others lack

- **Point-in-time restore** (`--at <RFC3339>`) — unique; most tools don't version at all.
- **S3-native storage with lifecycle integration** — most tools store in git or local filesystem.
- **Pluggable password sources** (prompt, env, flag, pass, command) — flexible; SOPS uses KMS/GPG.
- **Self-describing blobs** (KDF params in header) — allows future parameter changes without breaking old blobs.

### 3.2 Missing features vs comparable tools

#### 3.2.1 Key rotation / re-encryption
**SOPS, Vault, BlackBox** all support re-encrypting existing secrets with a new key/passphrase.
confkoffer has no `rekey` command. Once a blob is uploaded with a passphrase, the only way to change the passphrase is to `unpack`, change password config, and `pack` again — leaving old blobs decryptable with the old passphrase forever (unless lifecycle rules expire them).

**Recommendation**: `confkoffer rekey --old-pass … --new-pass …` that downloads, decrypts, re-encrypts, uploads under the same key name.

#### 3.2.2 Multi-recipient / team key sharing
**SOPS** supports encrypting to multiple KMS keys or GPG recipients. **BlackBox** is built on GPG team keyrings.
confkoffer is symmetric-only. Sharing a passphrase by side-channel is exactly what the README describes as a solved problem, but rotating one person's access out requires changing the shared passphrase and notifying everyone.

**Recommendation**: An optional `age`-recipient layer on top of the current symmetric core, or a `pass` team path convention. This is a larger design decision but is the most-requested feature class in this tool category.

#### 3.2.3 Diff / audit between snapshots
**Vault** has audit logging. **git-crypt** inherits git's history. **SOPS** files can be diffed with `sops --decrypt`.
confkoffer has no way to see what changed between two snapshots. You can only unpack two snapshots into separate directories and `diff -r` them manually.

**Recommendation**: `confkoffer diff <key1> <key2>` that downloads, decrypts both, and streams a unified diff without writing to disk.

#### 3.2.4 Selective extract (single-file unpack)
**SOPS** operates on individual files. **Vault** returns individual secrets.
confkoffer unpacks the entire snapshot or nothing. There is no way to extract just `secrets/prod.env` from a large bundle without writing the rest.

**Recommendation**: `confkoffer unpack --only secrets/prod.env` using the zip central directory to seek directly to the matching entry.

#### 3.2.5 Verify / integrity check without full unpack
**age**, **SOPS** — decryption itself is the verify step. **Vault** has explicit seal/unseal health.
confkoffer has no way to verify a snapshot's integrity without fully downloading, decrypting, and extracting it. A lightweight `verify` command that only validates the GCM tag (without writing files) would catch corruption early.

**Recommendation**: `confkoffer verify [--object-key KEY]` — download, check the 35-byte header, attempt `gcm.Open` against the tag, report pass/fail. No disk writes.

#### 3.2.6 Prune / delete old snapshots
**All comparable tools** leave cleanup to the user, but many provide a helper command.
confkoffer's README correctly says to use S3 lifecycle rules, but there is no `confkoffer prune --keep-last N` command. This is especially needed for high-frequency CI pack runs that would otherwise accumulate thousands of objects.

**Recommendation**: `confkoffer prune --keep-last N` (dry-run by default) that lists objects, keeps the N newest, and calls `s3:DeleteObject` on the rest.

#### 3.2.7 Progress output for large packs
**rclone**, **restic**, **Vault** all display progress bars or transfer stats.
confkoffer prints nothing during the upload/download. For large archives over slow connections, the tool appears to hang.

**Recommendation**: Use a progress writer wrapping the upload/download byte stream (e.g. with `github.com/vbauerster/mpb` or a simple bytes-transferred counter to stderr).

#### 3.2.8 `--dry-run` flag
Already tracked as issue #2. Mentioned here for completeness.

#### 3.2.9 Snapshot commit messages / annotations
Already tracked as issue #3. Mentioned here for completeness.

#### 3.2.10 HashiCorp Vault password source
Already noted in the README roadmap. Would complete the automation story for Vault-native infra teams.

---

## 4. Priority Recommendation

| Priority | Item |
|----------|------|
| Immediate | Fix #6 (zero plaintext), fix #7 (size caps), fix README stale host6 docs |
| Short-term | Fix #8 (TLS warning), add `verify` command, add `prune` command |
| Medium-term | `diff` command, selective extract (`--only`), progress output |
| Longer-term | `rekey` command, multi-recipient design |

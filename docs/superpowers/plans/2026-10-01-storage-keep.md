# Storage Keep Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `spec.storage.keep` lists the paths on a data claim that survive a start. Set, every start deletes everything else on the claim before the renderer and the copies run. Unset, nothing changes.

**Architecture:** The operator renders the list into the game container as `SPAWNERY_KEEP` (entries joined by newlines). `image/entrypoint.sh` calls `spawnery-config --prune` first, which runs the new `internal/prune` package: a walk that matches globs per segment, never follows symlinks, honours mount points and refuses before it deletes. The claim-backed source walk shared with `internal/substitute` moves to `internal/sourcetree`.

**Tech Stack:** Go, kubebuilder markers and CEL, envtest, POSIX shell with test doubles.

**Spec:** `docs/superpowers/specs/2026-10-01-storage-keep-design.md`

## Global Constraints

- Every command runs in the dev shell (`nix develop -c <cmd>`); `git add` new files before any Nix build.
- The field is optional and mutable on `StorageSpec`, 1 to 64 items of at most 256 characters, refused by CEL when an entry starts with `/`, contains `[`, `]`, `\`, a line break, or has an empty, `.` or `..` segment.
- A group without `keep` builds the identical pod: `internal/podspec/hash_golden_test.go` stays unchanged.
- The Velocity entrypoint is untouched.
- Exit codes follow `--substitute`: usage 2, refusal 1.
- Nothing about any real network in code, tests, docs or commits. Example names: a world at `world` (Paper's default layout) and plugin state at `plugins/ExampleGame/state`.
- Generated files after API changes: `make manifests generate`, committed.

## Review Focus

1. **A world is never deleted by a forgotten entry.** Anything no entry keeps that is or holds a `level.dat*` file, a `region` directory or an `.mca` file refuses the start with nothing deleted. Task 3.
2. **Nothing is deleted when a refusal is possible.** The whole plan is built first, the refusals run, then the removals. Task 3.
3. **Mount points survive**, read-only or writable, with their parents and `\040`-escaped paths. Task 3.
4. **`lost+found` is skipped at the root of the claim only**, where mkfs puts it; a nested one is pruned. Task 3.
5. **`config/` is the renderer's.** With `config` not listed, `paper-world-defaults.yml` reverts to Paper's defaults unless a `configOverlay` names it. Documented in the spec and the guide. Task 6.

---

### Task 1: API field

**Files:**
- Modify: `api/v1alpha1/servergroup_types.go` (`StorageSpec`)
- Test: `api/v1alpha1/servergroup_envtest_test.go`
- Generated: `config/crd/bases/spawnery.cloud_servergroups.yaml`, `charts/spawnery/templates/crds.yaml`, `docs/reference/crds.md`, `zz_generated.deepcopy.go`

- [ ] **Step 1: Failing envtest cases.** Accepted on `OnDemand` and `Persistent`; refused: `[]`, `/x`, `a/../b`, `a//b`, `a[b`, an entry with a line break.
- [ ] **Step 2: Add `Keep []string`** with `MinItems=1`, `MaxItems=64`, `items:MaxLength=256` and the `items:XValidation` rule. The doc comment states what is checked: the root `lost+found` and mount points are never deleted, and the start is refused when something no entry keeps is a `level.dat*` file, a `region` directory or an `.mca` file.
- [ ] **Step 3: `make manifests generate`**, run the envtest cases, commit with the generated files.

### Task 2: SPAWNERY_KEEP

**Files:**
- Modify: `internal/podspec/sources.go` (`keepEnv`), `internal/podspec/server.go`
- Test: `internal/podspec/sources_test.go`

- [ ] **Step 1: Failing test.** The variable is present, one entry per line, only when `keep` is set; absent otherwise; the hash golden does not move.
- [ ] **Step 2: Implement `keepEnv`** and append it after `substitutionEnv` in the game container's env.
- [ ] **Step 3: Run `go test ./internal/podspec/`**, commit.

### Task 3: sourcetree and prune

**Files:**
- Create: `internal/sourcetree/sourcetree.go`, `internal/prune/prune.go`, `internal/prune/prune_test.go`
- Modify: `internal/substitute/substitute.go`, `internal/substitute/substitute_test.go`

**Interfaces:** `sourcetree.Pair{From, Into}` with `Walk(fn)` (a missing source is empty, the root `lost+found` of a source is skipped). `prune.Run(dir, keep, mountinfo, pairs, log) error`.

- [ ] **Step 1: Move the walk.** `substitute.Trees` takes `[]sourcetree.Pair`; its tests pass unchanged apart from the type.
- [ ] **Step 2: Failing table tests for `prune`.** Unmatched files and directories go; a matched directory is kept whole; a plugin's state directory survives beside files that go; `*` and `?` match within one segment; an empty directory is fine; a symlink is removed and its target stays; each removal is logged as `spawnery: keep: removing <path>`; a bad entry refuses.
- [ ] **Step 3: Failing refusal and edge tests.** Each of `level.dat`, `level.dat_old`, `level.dat_new`, a `region` directory and an `.mca` file that no entry keeps refuses and deletes nothing; an unreadable subtree refuses; a source carrying a path the list keeps refuses and names it, deleting nothing; a source that ships only unkept paths or is missing is fine; a missing mountinfo refuses; a relative root still sees absolute mount points.
- [ ] **Step 4: Failing mount and `lost+found` tests.** A read-only and a writable mount keep themselves and their parents; a mount path with `\040` is unescaped; the root `lost+found` is untouched; a nested `lost+found` is pruned like any other unmatched path.
- [ ] **Step 5: Implement.** `parseKeep` splits into segments and validates; `mountsBelow` reads mountinfo; `plan` walks top-down without following symlinks and queues what is neither kept, nor a mount point, nor on the way to either, skipping `lost+found` only when `len(rel) == 0`; `holdsWorld` and `refuseKept` run before any `os.RemoveAll`. The root is made absolute and symlink-resolved first.
- [ ] **Step 6: `go test -race ./internal/prune/ ./internal/substitute/`**, commit.

### Task 4: spawnery-config --prune

**Files:**
- Modify: `cmd/spawnery-config/main.go`
- Test: `cmd/spawnery-config/main_test.go`

- [ ] **Step 1: Failing tests.** `--prune` without entries exits 2; it deletes what the list does not keep; a world no entry keeps exits 1 and deletes nothing.
- [ ] **Step 2: Add the flags** `--prune`, `--mountinfo`, `--pair` (repeatable, `from=into`), run `prune.Run` in the working directory and map errors to the exit codes.
- [ ] **Step 3: Run `go test ./cmd/spawnery-config/`**, commit.

### Task 5: entrypoint

**Files:**
- Modify: `image/entrypoint.sh`
- Test: `image/entrypoint_test.go`

- [ ] **Step 1: Failing tests with the `spawnery-config` double.** Prune runs only when `SPAWNERY_KEEP` is set, first, with the keep entries, the mountinfo and one `--pair` per source; it runs before `eula.txt` is written; a refusing prune stops the start before the JVM.
- [ ] **Step 2: Add the prune step** at the top of `entrypoint.sh`, guarded by `SPAWNERY_KEEP`.
- [ ] **Step 3: Run `go test ./image/`**, commit.

### Task 6: docs

**Files:**
- Modify: `docs/guides/persistent-worlds.md` ("What survives a start"), `docs/guides/on-demand-servers.md`, `docs/guides/mounts-and-files.md`, `CLAUDE.md` (entrypoint order), the spec

- [ ] **Step 1: Write the guide section.** The list, the glob rules, matched directories kept whole, the refusals, the log line, that unset deletes nothing, and that the list is part of the pod (a persistent group rolls, an on-demand member picks it up at its next start).
- [ ] **Step 2: Everything under `config/` comes from the renderer and `configOverlay`.** Say it in the spec and the guide: `paper-world-defaults.yml` is rendered only when a `configOverlay` names it, so set per-world defaults there, or list `config` in `keep` and accept that it is then never refreshed.
- [ ] **Step 3: Cross-reference** from the on-demand and mounts guides and update the entrypoint order line in `CLAUDE.md`.
- [ ] **Step 4: Commit.**

### Task 7: verify

- [ ] **Step 1:** `go test -race ./internal/prune/ ./internal/substitute/ ./cmd/spawnery-config/ ./image/ ./internal/podspec/`.
- [ ] **Step 2:** `make manifests generate test lint` in the dev shell; commit any regenerated file.
- [ ] **Step 3:** Version bumps are a separate release PR: new CRD field, entrypoint and binary change, so a minor step for the operator, the chart and the images.

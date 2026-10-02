# Per-claim Storage Growth Implementation Plan

**Goal:** `spec.storage.annotations` lands on each new data claim, and `spec.storage.size` may be lowered.

**Spec:** `docs/superpowers/specs/2026-10-02-storage-growth-per-claim-design.md`

## Global Constraints

- Every command runs in the dev shell (`nix develop -c <cmd>`).
- `growClaim` stays the only write to an existing claim and never lowers a request.
- Generated files after API changes: `make manifests generate`, committed.
- Nothing about any real network or cluster in code, tests, docs or commits.

## Tasks

- [ ] **API.** Delete the `storage.size must not shrink` rule, rewrite the `Size` doc, add `Annotations`. Envtest: a lowered size is accepted for persistent and on-demand groups, annotations round-trip.
- [ ] **Claim.** `BuildDataClaim` clones `Storage.Annotations` onto the claim. Test: copied, not shared, labels unchanged, nil when unset.
- [ ] **Wording.** `growClaim`, `storageResizeCondition`, `StorageResizeError` and `ConditionStorageResize` describe refused resizes from any source.
- [ ] **Behaviour test.** A lowered size leaves an existing claim untouched (same resourceVersion, no resize error); a member created afterwards gets the lowered size and the annotations.
- [ ] **Docs.** "Claims that grow by themselves" in the persistent-worlds guide, a link from the on-demand guide, regenerated CRD reference.

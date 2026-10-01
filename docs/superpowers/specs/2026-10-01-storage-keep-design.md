# What survives a start: spec.storage.keep

**Status:** design, approved 2026-10-01
**Date:** 2026-10-01

## 1. What goes wrong today

An OnDemand or Persistent member keeps its data claim at `/data` across stops.
On every start `image/entrypoint.sh` renders `server.properties` and the Paper
configs, then `cp -R`s `extraFiles` into `/data` and `extraPlugins` into
`/data/plugins`. It never deletes anything, so:

- a plugin or config removed from the sources lingers on every claim;
- a file nobody ships any more stays frozen at its last value;
- a config that carried a secret sits on the claim after the source dropped it;
- a world the source ships mixes with the stale files of the previous one.

## 2. The shape

An optional `spec.storage.keep`, a list of paths that survive a start. Set,
each start first deletes everything on the claim that no entry matches, then
renders and copies as before. Unset, nothing is deleted and the pod is
byte-for-byte what it was.

Example, for a Challenges server:

```yaml
storage:
  size: 5Gi
  keep:
    - worlds/world
    - plugins/Challenges/internal
```

`worlds/world` includes the world's datapacks. They persist with the save on
purpose, so chunks generated later come out like the ones already there.
Guidance is to list the level directory and each plugin's state directory, not
single dimensions or files inside them: a world's name and number are the
game's choice.

## 3. Decisions

- **An allowlist, not a denylist.** What a plugin writes cannot be enumerated,
  so a denylist rots with every plugin update. The allowlist names the few
  things that are state, and everything else is by definition reproducible from
  the sources.
- **Pruned at start, not at stop.** The JVM is PID 1 and a hard kill runs no
  stop hook, so a stop-time cleanup would sometimes not happen. A start always
  runs the entrypoint.
- **Go, not shell.** A walk that matches globs per segment, never follows
  symlinks, honours mount points and refuses before deleting is
  `internal/prune`, tested as a table, not a `find` pipeline. The entrypoint
  only calls `spawnery-config --prune`.
- **A `level.dat` guard.** A level.dat that no entry keeps refuses the start:
  the list forgot a world, and silently deleting one is the one mistake that
  cannot be undone.
- **No overlap between a source and the list.** A source (`extraFiles`,
  `extraPlugins`) that carries a path the list keeps would overwrite the saved
  file on every start, a shipped file silently winning over saved state. The
  start is refused instead; a missing source directory is fine.
- **No dry-run field.** Each removal is logged as
  `spawnery: keep: removing <path>` before it happens, which is the dry-run
  view for the first start after a change.

## 4. API

`StorageSpec.Keep []string`, optional, mutable, 1 to 64 items of at most 256
characters. A CEL rule on the items refuses an entry that starts with `/`,
contains `[`, `]` or `\`, or has an empty, `.` or `..` segment. It is allowed
wherever `storage` is (OnDemand and Persistent; Ephemeral already forbids
storage).

Entries are matched segment by segment with `path.Match`, so `*` and `?` never
cross a `/`. A matched directory is kept whole.

## 5. Delivery

`internal/podspec` emits `SPAWNERY_KEEP`, the entries joined by newlines, only
when the field is set. A group without it builds the identical pod and the
hash golden does not move. A persistent group that sets it, or changes it, rolls. An on-demand group
does not: a running member keeps its pod, and the new list reaches it at its
next start.

## 6. The prune

`spawnery-config --prune "$SPAWNERY_KEEP" --mountinfo "$MOUNTINFO" --pair
"$FILE_SOURCE=." --pair "$PLUGIN_SOURCE=plugins"`, the first step of
`image/entrypoint.sh`, before `eula.txt` and the renderer. The Velocity
entrypoint is untouched. Working directory is the data directory.

1. Read the mount points below the data directory from mountinfo, read-only and
   writable, with `\040` unescaped. A writable claim mount under `/data` is
   common, so read-only alone is not enough.
2. Walk top-down without following symlinks. An entry that matches an entry of
   the list, or is a mount point, is kept and not entered. An entry that is an
   ancestor of something a pattern could match, or of a mount point, is
   entered. `lost+found` is skipped at every level the walk visits. Everything
   else is queued for deletion.
3. Refuse, before deleting anything, when a queued path is or contains a
   `level.dat`, or when a `--pair` source carries a path the list would keep at
   its destination.
4. Remove each queued path with `os.RemoveAll`, logging it. A symlink is
   removed as a link and its target is untouched.

Exit codes follow `--substitute`: usage 2, refusal 1.

## 7. Versions

A new CRD field, an entrypoint change and a binary change: a minor step for the
operator, the chart and the images. Release is a separate PR.

## 8. Testing

- **prune:** unmatched files and directories go; a matched directory is kept
  whole; a plugin's state directory survives beside files that go; globs;
  `lost+found`; a read-only and a writable mount keep themselves and their
  parents; a `level.dat` outside the list refuses and deletes nothing; a source
  carrying a kept path refuses and deletes nothing; a symlink goes and its
  target stays; an empty directory is fine.
- **spawnery-config:** usage exits 2, a refusal exits 1.
- **entrypoint:** prune runs only with `SPAWNERY_KEEP`, before the renderer and
  before `eula.txt`; a refusing prune stops the start before the JVM.
- **API:** accepted on OnDemand and Persistent; `[]`, `/x`, `a/../b`, `a//b`
  and `a[b` refused.
- **podspec:** the variable is present only when set; the golden is unchanged.

## 9. Not in this design

- Pruning the Velocity image: proxies have no claim.
- Deleting at stop, or on a schedule.
- A separate dry-run field.

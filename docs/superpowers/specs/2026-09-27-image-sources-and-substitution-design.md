# Plugins and files from an image, and placeholders filled at start

**Status:** design, decided 2026-09-27
**Date:** 2026-09-27

## 1. What goes wrong today

`extraPlugins` and `extraFiles` name a ReadWriteMany claim, and the entrypoint
copies its content into `/data` on every start. A ReadWriteMany volume is, on
most clusters, one NFS-style server on one node. While that node is gone, no
server of the group can start anywhere; and a node that dies hard can leave the
volume's filesystem needing a manual repair, after which the group stays down
until somebody fixes it.

The content is read once per start and never written by a server. It does not
need a shared filesystem at all.

A second problem sits beside the first. A network that keeps secrets in its
plugin configuration (a database password in a plugin's `config.yml`) has to
write them into the volume, because nothing between the volume and the server
could put them there later.

## 2. The shape

- `extraPlugins` and `extraFiles` can name an **image** instead of a claim. The
  image is mounted as a read-only `image` volume at the path the claim uses
  today, so the entrypoint copies it exactly as it copies a claim.
- A new **`spec.substitution`** on ServerGroup and ProxyGroup fills `{{ NAME }}`
  placeholders in the copied text files from the container's environment, at
  start.

Together they let a network publish its plugins and files as an immutable,
content-addressed artifact without secrets in it, and every node pulls and
caches it on its own.

## 3. API

```yaml
spec:
  extraPlugins:
    image: registry.example.net/lobby-plugins@sha256:…
    pullPolicy: IfNotPresent   # default
  extraFiles:
    image: registry.example.net/lobby-files@sha256:…
  substitution:
    prefix: SECRET_
  env:
    - name: SECRET_DB_PASSWORD
      valueFrom:
        secretKeyRef: { name: lobby-db, key: password }
```

- `ExtraPlugins` and `ExtraFiles` gain `image` (string) and `pullPolicy`
  (`Always`, `IfNotPresent`, `Never`; default `IfNotPresent`). Exactly one of
  `claimName` and `image` is set, checked by CEL. `pullPolicy` without `image`
  is refused.
- `Substitution` has one field, `prefix` (required, non-empty, matching
  `^[A-Z][A-Z0-9_]*$`).
- ServerGroup and ProxyGroup share the `ExtraPlugins` and `ExtraFiles` types,
  so both gain the image source; `substitution` is added to both.
- An image source needs no switch like `allowPluginVolumes`: it pulls an image,
  which `spec.image` already may, and touches no host path.
- Existing objects validate unchanged; a claim source behaves as before.

## 4. The pod

- An image source becomes an `image` volume (`reference`, `pullPolicy`) mounted
  read-only at `/var/run/spawnery/plugins` or `/var/run/spawnery/files`. The
  pod's `imagePullSecrets` (from `Network.spec.defaults.imagePullSecrets`)
  apply to it.
- The image's filesystem root is what the claim's root is today.
- A different reference changes the pod spec and so the group's desired hash:
  publishing a new digest rolls the group like any other spec change.

## 5. Substitution

After the entrypoint has copied plugins and files, a small static program in
the image, `spawnery-substitute`, walks the files that came from those two
sources and replaces placeholders:

- A placeholder is `{{ NAME }}`, spaces inside the braces optional, where
  `NAME` starts with the configured prefix.
- Its value is the environment variable `NAME`, verbatim; no escaping is
  applied, so a value may contain any character.
- Only text files are touched, by extension: `.yml`, `.yaml`, `.json`,
  `.properties`, `.conf`, `.toml`, `.txt`, `.cfg`.
- A placeholder with the prefix whose variable is not set stops the server
  before it starts, naming the file and the placeholder. Shipping a literal
  placeholder into a database password fails later and more confusingly.
- A placeholder without the prefix is left alone: it may be meant for
  something else.
- No `substitution` means nothing is replaced, as today.
- Values never appear in a log line.

A program and not `sed`: a secret may contain `/`, `&` or a newline, and
escaping for sed is where such values break.

## 6. Versions

New CRD fields, a changed entrypoint and a new binary in the images: a minor
step for the operator, the chart and the images, **0.11.0**.

## 7. Testing

- **API:** CEL refuses both sources, neither-with-pullPolicy, and a bad prefix;
  accepts each source alone.
- **podspec:** an image source becomes a read-only image volume at the right
  path with the pull policy; a claim source is unchanged; changing the
  reference changes the pod hash.
- **spawnery-substitute:** replaces with and without inner spaces; leaves other
  prefixes alone; skips non-text files; fails naming file and placeholder when
  a variable is missing; values with `/`, `&`, `$`, quotes and newlines arrive
  verbatim; nothing logs a value.
- **entrypoint (image tests):** a server starts from an image source with a
  placeholder filled from its environment.
- **e2e:** a group with an image source and substitution becomes Ready on a kind
  cluster with a local registry.

## 8. Not in this design

- Building or publishing the artifacts: that is the network's pipeline.
- Mutable shared data (a pool of generated worlds that servers write): still a
  claim.
- Substitution in files that come from elsewhere (the config overlay, the
  image's own files).

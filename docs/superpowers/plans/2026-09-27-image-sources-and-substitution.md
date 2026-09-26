# Image Sources and Substitution Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `extraPlugins` and `extraFiles` can name an image instead of a claim, and `spec.substitution` fills `{{ PREFIX_… }}` placeholders in the copied text files from the container's environment at start.

**Architecture:** The API gains `image`/`pullPolicy` on the shared `ExtraPlugins`/`ExtraFiles` types (CEL: exactly one source) and a `Substitution` type on ServerGroup and ProxyGroup. podspec renders an image source as a read-only `image` volume at the path the claim uses, and passes the prefix as `SPAWNERY_SUBSTITUTION_PREFIX`. Both entrypoints, after their copies, run `spawnery-config --substitute`, a second mode of the binary already in every image, backed by a new `internal/substitute` package.

**Tech Stack:** Go (controller-runtime, envtest), kubebuilder CEL, POSIX sh entrypoints, Nix-built images.

**Spec:** `docs/superpowers/specs/2026-09-27-image-sources-and-substitution-design.md`

## Global Constraints

- Exactly one of `claimName` and `image`; `pullPolicy` only with `image`; `pullPolicy` ∈ `Always`, `IfNotPresent`, `Never`, default `IfNotPresent`.
- `substitution.prefix` required, matching `^[A-Z][A-Z0-9_]*$`.
- Placeholder: `{{ NAME }}`, inner spaces optional, `NAME` = `[A-Z][A-Z0-9_]*` starting with the prefix; value = env `NAME`, verbatim.
- Text files only, by extension: `.yml`, `.yaml`, `.json`, `.properties`, `.conf`, `.toml`, `.txt`, `.cfg`.
- A prefixed placeholder without its variable stops the start, naming file and placeholder; values never appear in any output.
- Only files that came from the plugin and file sources are touched.
- An image source is not gated by `--allow-plugin-volumes` / `--allow-file-volumes` and gets no claim check.
- Version 0.11.0 is its own release commit after review; not in this plan.
- Commits: Conventional Commits with scope, body wrapped at 72, signed, session trailers. Examples invented (`lobby`, `registry.example.net`).
- New Go files carry the `Copyright paul_wtf.` Apache header.

## Rulings against the spec (made while planning)

- §5 names a new binary `spawnery-substitute`. The plan makes it a mode of `spawnery-config` (`--substitute PREFIX`), which is already built into all three images and already runs in both entrypoints; a new binary would need three image definitions and a new package in `flake.nix`. Behaviour is as the spec says.
- §7's e2e on kind with a local registry is replaced by an image test (the real Purpur image, a mounted source, the entrypoint's substitution) plus the live check in the network's own plan. The image volume wiring itself is covered by podspec tests and proven on the live cluster; a kind registry would add a moving part to every CI run for one assertion.

## Commands

```bash
NIX="nix --extra-experimental-features 'nix-command flakes' develop /home/paul/git/spawnery -c"
$NIX go -C /home/paul/git/spawnery test ./internal/substitute/ ./cmd/spawnery-config/ ./internal/podspec/ ./image/ -count=1
$NIX go -C /home/paul/git/spawnery test ./internal/controller/ -run 'TestName' -count=1 -p 1
$NIX make -C /home/paul/git/spawnery manifests generate
$NIX make -C /home/paul/git/spawnery image-test      # container runtime: podman on paul-desktop, check `hostname` first
$NIX make -C /home/paul/git/spawnery manifests generate fmt vet chart-lint toolchain-lint image-tag-lint docs-length-lint crd-docs-test chart-values-docs-test metrics-docs-test
$NIX go -C /home/paul/git/spawnery test -race -p 1 ./...
```

## Review Focus

1. **A secret value with `$`, `&`, `\`, quotes, a newline or `{{`** arrives verbatim and is not re-scanned for placeholders. Pinned in Task 3 (`TestValuesArriveVerbatim`).
2. **A missing variable** stops the start with file and placeholder in the message, and the value of any *present* secret never appears in stderr. Pinned in Task 3 (`TestAMissingVariableNamesFileAndPlaceholder`) and Task 4.
3. **A binary file with a text extension** (or invalid UTF-8) is not corrupted: replacement is byte-wise, and a file without any placeholder is not rewritten. Pinned in Task 3 (`TestAFileWithoutPlaceholdersIsNotRewritten`, `TestBytesOutsidePlaceholdersSurvive`).
4. **A file present in the source but overwritten or removed in `/data` by an earlier step** (the agent jar, a refused path) is skipped, not recreated. Pinned in Task 3 (`TestAFileMissingAtTheDestinationIsSkipped`).
5. **`substitution` set but no source at all** (no extraPlugins/extraFiles) starts normally. Pinned in Task 4 (`TestSubstituteRunsOnlyWithAPrefix` and the no-source case).

---

### Task 1: API — image sources and substitution

**Files:**
- Modify: `api/v1alpha1/common_types.go` (ExtraPlugins, ExtraFiles, new Substitution)
- Modify: `api/v1alpha1/servergroup_types.go`, `api/v1alpha1/proxygroup_types.go` (field `Substitution`)
- Regenerate: `api/v1alpha1/zz_generated.deepcopy.go`, `config/crd/bases/*`, `charts/spawnery/templates/crds.yaml`, `docs/reference/crds.md` (`make manifests generate`)
- Test: `internal/controller/imagesource_envtest_test.go`

**Interfaces:**
- Produces: `ExtraPlugins{ClaimName string; Image string; PullPolicy corev1.PullPolicy}`, same for `ExtraFiles`; `type Substitution struct{ Prefix string }`; `ServerGroupSpec.Substitution *Substitution`, `ProxyGroupSpec.Substitution *Substitution`.

- [ ] **Step 1: Failing envtest**

`internal/controller/imagesource_envtest_test.go` (package and imports as `groupenv_envtest_test.go`):

```go
func imageSourceGroup(ns string, mutate func(*spawneryv1alpha1.ServerGroup)) *spawneryv1alpha1.ServerGroup {
	g := &spawneryv1alpha1.ServerGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "lobby", Namespace: ns},
		Spec: spawneryv1alpha1.ServerGroupSpec{
			NetworkRef: spawneryv1alpha1.ObjectRef{Name: "production"},
			Type:       spawneryv1alpha1.ServerGroupEphemeral,
			Image:      "ghcr.io/spawnery/paper:1.21.4-0.1.0",
			MaxPlayers: 100,
			Scaling:    &spawneryv1alpha1.ScalingSpec{MinReplicas: 1, MaxReplicas: 2, SpareSlots: 10},
		},
	}
	mutate(g)
	return g
}

func TestTheAPIServerTakesExactlyOnePluginSource(t *testing.T) {
	c, ctx := testenv.Client(t)
	ns := testenv.Namespace(t, ctx, c)

	for _, bad := range []struct {
		why string
		ep  spawneryv1alpha1.ExtraPlugins
	}{
		{"both sources", spawneryv1alpha1.ExtraPlugins{ClaimName: "plugins", Image: "registry.example.net/lobby-plugins:1"}},
		{"neither source", spawneryv1alpha1.ExtraPlugins{}},
		{"a pull policy without an image", spawneryv1alpha1.ExtraPlugins{ClaimName: "plugins", PullPolicy: corev1.PullAlways}},
	} {
		ep := bad.ep
		if err := c.Create(ctx, imageSourceGroup(ns, func(g *spawneryv1alpha1.ServerGroup) { g.Spec.ExtraPlugins = &ep })); err == nil {
			t.Errorf("%s was admitted", bad.why)
		}
	}
	ok := imageSourceGroup(ns, func(g *spawneryv1alpha1.ServerGroup) {
		g.Spec.ExtraPlugins = &spawneryv1alpha1.ExtraPlugins{Image: "registry.example.net/lobby-plugins@sha256:" + strings.Repeat("a", 64)}
		g.Spec.ExtraFiles = &spawneryv1alpha1.ExtraFiles{Image: "registry.example.net/lobby-files:1", PullPolicy: corev1.PullAlways}
		g.Spec.Substitution = &spawneryv1alpha1.Substitution{Prefix: "SECRET_"}
	})
	if err := c.Create(ctx, ok); err != nil {
		t.Fatalf("image sources with a substitution were refused: %v", err)
	}
}

func TestTheAPIServerRefusesABadSubstitutionPrefix(t *testing.T) {
	c, ctx := testenv.Client(t)
	ns := testenv.Namespace(t, ctx, c)
	for i, prefix := range []string{"", "secret_", "1X", "SECRET-"} {
		p, name := prefix, fmt.Sprintf("lobby-%d", i)
		if err := c.Create(ctx, imageSourceGroup(ns, func(g *spawneryv1alpha1.ServerGroup) {
			g.Name = name
			g.Spec.Substitution = &spawneryv1alpha1.Substitution{Prefix: p}
		})); err == nil {
			t.Errorf("prefix %q was admitted", p)
		}
	}
}
```

There is deliberately no `+kubebuilder:default` on `pullPolicy`: a default applied by the API server would set it on claim sources too, which the second CEL rule refuses. The spec's "default IfNotPresent" lives in podspec (Task 2), where an empty policy renders as `IfNotPresent`.

Run: `$NIX go -C /home/paul/git/spawnery test ./internal/controller/ -run 'TestTheAPIServerTakesExactlyOnePluginSource|TestTheAPIServerRefusesABadSubstitutionPrefix' -count=1 -p 1`
Expected: FAIL to compile — `unknown field Image`, `undefined: spawneryv1alpha1.Substitution`.

- [ ] **Step 2: Implement the types**

In `common_types.go`, replace the two structs (keep their existing doc comments, extend them by one paragraph on the image source):

```go
// +kubebuilder:validation:XValidation:rule="has(self.claimName) != has(self.image)",message="exactly one of claimName or image must be set"
// +kubebuilder:validation:XValidation:rule="!has(self.pullPolicy) || has(self.image)",message="pullPolicy applies to an image source only"
type ExtraPlugins struct {
	// ClaimName names a ReadWriteMany claim in the group's namespace.
	// +optional
	ClaimName string `json:"claimName,omitempty"`
	// Image names an OCI image whose filesystem root is the plugin tree. It is
	// mounted as a read-only image volume where a claim would be, pulled with
	// the pod's imagePullSecrets. A digest reference rolls the group whenever
	// it changes; a tag does not.
	// +optional
	Image string `json:"image,omitempty"`
	// PullPolicy for Image. Empty means IfNotPresent.
	// +kubebuilder:validation:Enum=Always;IfNotPresent;Never
	// +optional
	PullPolicy corev1.PullPolicy `json:"pullPolicy,omitempty"`
}
```

and the same three fields on `ExtraFiles` with the same two markers (messages naming `extraFiles`' fields identically). Then:

```go
// Substitution fills {{ NAME }} placeholders in the files copied from
// extraPlugins and extraFiles, at every start, from the container's
// environment. Only names beginning with Prefix are replaced, and one whose
// variable is missing stops the start.
type Substitution struct {
	// +kubebuilder:validation:Pattern=`^[A-Z][A-Z0-9_]*$`
	Prefix string `json:"prefix"`
}
```

Add to `ServerGroupSpec` (after `ExtraFiles`) and `ProxyGroupSpec` (after `ExtraFiles`):

```go
	// Substitution fills placeholders in the files the entrypoint copies from
	// extraPlugins and extraFiles. See Substitution.
	// +optional
	Substitution *Substitution `json:"substitution,omitempty"`
```

Run: `$NIX make -C /home/paul/git/spawnery manifests generate`, then `git -C /home/paul/git/spawnery diff --stat` — expected: deepcopy, CRDs, chart crds and `docs/reference/crds.md` change.

- [ ] **Step 3: Pass**

Run the two tests again.
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git -C /home/paul/git/spawnery add api config charts docs/reference internal/controller/imagesource_envtest_test.go
git -C /home/paul/git/spawnery commit -m "feat(api): plugins and files from an image, and spec.substitution

extraPlugins and extraFiles take image and pullPolicy beside claimName,
exactly one source checked by CEL. ServerGroup and ProxyGroup gain
substitution with a prefix for the placeholders the entrypoint fills.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01JQ1B3g7MgNUnuusWCn8aW6"
```

---

### Task 2: podspec and the controller checks

**Files:**
- Modify: `internal/podspec/server.go:386-425`, `internal/podspec/proxy.go` (same block)
- Create: `internal/podspec/sources.go` (shared volume builder)
- Modify: `internal/controller/extraplugins.go`, `internal/controller/extrafiles.go` (skip gate and claim check for an image)
- Test: `internal/podspec/sources_test.go`, `internal/controller/extraplugins_test.go` (or the file that tests `checkExtraPlugins` today — find it with `grep -rn checkExtraPlugins internal/controller/*_test.go`)

**Interfaces:**
- Consumes: Task 1's types.
- Produces: `func sourceVolume(name, claim, image string, pull corev1.PullPolicy) corev1.Volume`; env `SPAWNERY_SUBSTITUTION_PREFIX` (const `EnvSubstitutionPrefix` in podspec).

- [ ] **Step 1: Failing tests**

`internal/podspec/sources_test.go`:

```go
package podspec

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	spawneryv1alpha1 "github.com/spawnery/spawnery/api/v1alpha1"
)

func volumeNamed(pod *corev1.Pod, name string) *corev1.Volume {
	for i := range pod.Spec.Volumes {
		if pod.Spec.Volumes[i].Name == name {
			return &pod.Spec.Volumes[i]
		}
	}
	return nil
}

func TestAnImageSourceIsAReadOnlyImageVolume(t *testing.T) {
	pod := build(t, func(_ *spawneryv1alpha1.Network, g *spawneryv1alpha1.ServerGroup) {
		g.Spec.ExtraPlugins = &spawneryv1alpha1.ExtraPlugins{Image: "registry.example.net/lobby-plugins:1"}
		g.Spec.ExtraFiles = &spawneryv1alpha1.ExtraFiles{Image: "registry.example.net/lobby-files:1", PullPolicy: corev1.PullAlways}
	})
	plugins := volumeNamed(pod, PluginSourceVolumeName)
	if plugins == nil || plugins.Image == nil || plugins.Image.Reference != "registry.example.net/lobby-plugins:1" {
		t.Fatalf("plugin source = %+v, want an image volume", plugins)
	}
	if plugins.Image.PullPolicy != corev1.PullIfNotPresent {
		t.Errorf("empty pull policy rendered %q, want IfNotPresent", plugins.Image.PullPolicy)
	}
	files := volumeNamed(pod, FileSourceVolumeName)
	if files == nil || files.Image == nil || files.Image.PullPolicy != corev1.PullAlways {
		t.Fatalf("file source = %+v, want an image volume with Always", files)
	}
	for _, m := range pod.Spec.Containers[0].VolumeMounts {
		if (m.Name == PluginSourceVolumeName || m.Name == FileSourceVolumeName) && !m.ReadOnly {
			t.Errorf("%s is mounted writable", m.Name)
		}
	}
}

func TestSubstitutionReachesTheContainerAsItsPrefix(t *testing.T) {
	pod := build(t, func(_ *spawneryv1alpha1.Network, g *spawneryv1alpha1.ServerGroup) {
		g.Spec.Substitution = &spawneryv1alpha1.Substitution{Prefix: "SECRET_"}
	})
	found := false
	for _, e := range pod.Spec.Containers[0].Env {
		if e.Name == EnvSubstitutionPrefix {
			found = e.Value == "SECRET_"
		}
	}
	if !found {
		t.Errorf("%s=SECRET_ missing from %v", EnvSubstitutionPrefix, pod.Spec.Containers[0].Env)
	}
	if bare := build(t, nil); envHas(bare, EnvSubstitutionPrefix) {
		t.Error("a group without substitution got the prefix variable")
	}
}

func TestAChangedImageReferenceMovesThePodHash(t *testing.T) {
	net, group := testNetwork(), testGroup()
	group.Spec.ExtraPlugins = &spawneryv1alpha1.ExtraPlugins{Image: "registry.example.net/lobby-plugins@sha256:" + sixtyFour("a")}
	before, err := DesiredServerHash(net, group, nil)
	if err != nil {
		t.Fatal(err)
	}
	group.Spec.ExtraPlugins.Image = "registry.example.net/lobby-plugins@sha256:" + sixtyFour("b")
	after, err := DesiredServerHash(net, group, nil)
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Error("a new digest left the hash unchanged, so publishing it would roll nothing")
	}
}

func envHas(pod *corev1.Pod, name string) bool {
	for _, e := range pod.Spec.Containers[0].Env {
		if e.Name == name {
			return true
		}
	}
	return false
}

func sixtyFour(c string) string {
	out := ""
	for i := 0; i < 64; i++ {
		out += c
	}
	return out
}
```

Add the proxy counterpart (`TestAProxyImageSourceIsAReadOnlyImageVolume`) using `BuildProxyPod(testNetwork(), g, "gateway-abcd", testEndpoint, nil)` with `g := testProxyGroup()` and the same two fields.

For the controller check, add to the existing test file of `checkExtraPlugins` (a unit test with a fake reader, following the file's pattern):

```go
func TestAnImageSourceNeedsNoSwitchAndNoClaim(t *testing.T) {
	reason, message, ok := checkExtraPlugins(context.Background(), emptyReader(t), "games",
		&spawneryv1alpha1.ExtraPlugins{Image: "registry.example.net/lobby-plugins:1"}, false)
	if !ok {
		t.Fatalf("an image source was refused with the plugin volumes switch off: %s %s", reason, message)
	}
	reason, message, ok = checkExtraFiles(context.Background(), emptyReader(t), "games",
		&spawneryv1alpha1.ExtraFiles{Image: "registry.example.net/lobby-files:1"}, false)
	if !ok {
		t.Fatalf("an image file source was refused: %s %s", reason, message)
	}
}
```

`emptyReader` is a fake client with the scheme and no objects; use the file's existing helper if it has one.

Run: `$NIX go -C /home/paul/git/spawnery test ./internal/podspec/ ./internal/controller/ -run 'ImageSource|SubstitutionReaches|ImageReferenceMoves|NeedsNoSwitch' -count=1 -p 1`
Expected: FAIL — no image volume, undefined `EnvSubstitutionPrefix`, the check refuses.

- [ ] **Step 2: Implement**

`internal/podspec/sources.go`:

```go
package podspec

import corev1 "k8s.io/api/core/v1"

// EnvSubstitutionPrefix carries spec.substitution.prefix to the entrypoint.
const EnvSubstitutionPrefix = "SPAWNERY_SUBSTITUTION_PREFIX"

// sourceVolume renders a plugin or file source: a read-only claim, or a
// read-only image volume for an image source.
func sourceVolume(name, claim, image string, pull corev1.PullPolicy) corev1.Volume {
	if image != "" {
		if pull == "" {
			pull = corev1.PullIfNotPresent
		}
		return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{
			Image: &corev1.ImageVolumeSource{Reference: image, PullPolicy: pull},
		}}
	}
	return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{
		PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim, ReadOnly: true},
	}}
}
```

In `server.go` and `proxy.go`, replace each inline `corev1.Volume{…PersistentVolumeClaim…}` for the two sources with
`sourceVolume(PluginSourceVolumeName, ep.ClaimName, ep.Image, ep.PullPolicy)` (and the file equivalent). The mounts stay as they are (read-only).

Where the operator's own env entries are appended (server.go around line 462, and the proxy equivalent), add:

```go
	if group.Spec.Substitution != nil {
		env = append(env, corev1.EnvVar{Name: EnvSubstitutionPrefix, Value: group.Spec.Substitution.Prefix})
	}
```

using the slice name the function actually uses.

In `checkExtraPlugins` and `checkExtraFiles`, right after the `nil` check:

```go
	if ep.Image != "" {
		// An image is pulled, not mounted from the host or a claim, so neither
		// the volume switch nor the claim check applies.
		return "", "", true
	}
```

- [ ] **Step 3: Pass**

Run: `$NIX go -C /home/paul/git/spawnery test ./internal/podspec/ ./internal/controller/ -count=1 -p 1`
Expected: PASS, including every existing extraPlugins/extraFiles test.

- [ ] **Step 4: Commit**

```bash
git -C /home/paul/git/spawnery add internal/podspec internal/controller
git -C /home/paul/git/spawnery commit -m "feat(podspec): an image source is a read-only image volume

The pod mounts an image source where a claim would be, with its pull
policy (empty is IfNotPresent) and the pod's pull secrets, and carries
spec.substitution's prefix as SPAWNERY_SUBSTITUTION_PREFIX. An image
source is not gated by the volume switches and has no claim to check.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01JQ1B3g7MgNUnuusWCn8aW6"
```

---

### Task 3: The substitution itself

**Files:**
- Create: `internal/substitute/substitute.go`
- Test: `internal/substitute/substitute_test.go`

**Interfaces:**
- Produces:
  ```go
  // Pair is a source tree and where the entrypoint copied it.
  type Pair struct{ From, Into string }
  func Trees(pairs []Pair, prefix string, lookup func(string) (string, bool)) error
  ```

- [ ] **Step 1: Failing tests**

```go
package substitute

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tree writes files under a source and their copies under a destination, as
// the entrypoint leaves them.
func tree(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	from, into := t.TempDir(), t.TempDir()
	for rel, content := range files {
		for _, root := range []string{from, into} {
			p := filepath.Join(root, rel)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return from, into
}

func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPlaceholdersAreFilledWithAndWithoutSpaces(t *testing.T) {
	from, into := tree(t, map[string]string{"Lobby/config.yml": "a: {{ SECRET_A }}\nb: {{SECRET_B}}\n"})
	err := Trees([]Pair{{from, into}}, "SECRET_", env(map[string]string{"SECRET_A": "one", "SECRET_B": "two"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(into, "Lobby/config.yml")); got != "a: one\nb: two\n" {
		t.Errorf("got %q", got)
	}
}

func TestOtherPrefixesAreLeftAlone(t *testing.T) {
	from, into := tree(t, map[string]string{"c.yml": "x: {{ OTHER_A }}\n"})
	if err := Trees([]Pair{{from, into}}, "SECRET_", env(nil)); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(into, "c.yml")); got != "x: {{ OTHER_A }}\n" {
		t.Errorf("got %q", got)
	}
}

func TestOnlyTextExtensionsAreTouched(t *testing.T) {
	from, into := tree(t, map[string]string{"plugin.jar": "{{ SECRET_A }}", "notes.md": "{{ SECRET_A }}", "c.properties": "{{ SECRET_A }}"})
	if err := Trees([]Pair{{from, into}}, "SECRET_", env(map[string]string{"SECRET_A": "v"})); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(into, "plugin.jar")) != "{{ SECRET_A }}" || read(t, filepath.Join(into, "notes.md")) != "{{ SECRET_A }}" {
		t.Error("a non-text file was rewritten")
	}
	if read(t, filepath.Join(into, "c.properties")) != "v" {
		t.Error(".properties was not filled")
	}
}

func TestAMissingVariableNamesFileAndPlaceholder(t *testing.T) {
	from, into := tree(t, map[string]string{"Db/config.yml": "user: {{ SECRET_USER }}\npass: {{ SECRET_PASS }}\n"})
	err := Trees([]Pair{{from, into}}, "SECRET_", env(map[string]string{"SECRET_USER": "hunter2"}))
	if err == nil {
		t.Fatal("a missing variable was accepted")
	}
	if !strings.Contains(err.Error(), "Db/config.yml") || !strings.Contains(err.Error(), "SECRET_PASS") {
		t.Errorf("error %q does not name the file and the placeholder", err)
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("error %q leaks a value", err)
	}
}

func TestValuesArriveVerbatim(t *testing.T) {
	value := "a$b&c\\d\"e'f\ng{{ SECRET_A }}/h"
	from, into := tree(t, map[string]string{"c.json": `{"p": "{{ SECRET_A }}"}`})
	if err := Trees([]Pair{{from, into}}, "SECRET_", env(map[string]string{"SECRET_A": value})); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(into, "c.json")); got != `{"p": "`+value+`"}` {
		t.Errorf("got %q", got)
	}
}

func TestAFileWithoutPlaceholdersIsNotRewritten(t *testing.T) {
	from, into := tree(t, map[string]string{"c.yml": "plain\n"})
	p := filepath.Join(into, "c.yml")
	if err := os.Chmod(p, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := Trees([]Pair{{from, into}}, "SECRET_", env(nil)); err != nil {
		t.Fatalf("a file with nothing to fill was written to: %v", err)
	}
}

func TestBytesOutsidePlaceholdersSurvive(t *testing.T) {
	raw := "\xff\xfe{{ SECRET_A }}\x00tail"
	from, into := tree(t, map[string]string{"c.txt": raw})
	if err := Trees([]Pair{{from, into}}, "SECRET_", env(map[string]string{"SECRET_A": "v"})); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(into, "c.txt")); got != "\xff\xfev\x00tail" {
		t.Errorf("got %q", got)
	}
}

func TestAFileMissingAtTheDestinationIsSkipped(t *testing.T) {
	from, into := tree(t, map[string]string{"c.yml": "{{ SECRET_A }}"})
	if err := os.Remove(filepath.Join(into, "c.yml")); err != nil {
		t.Fatal(err)
	}
	if err := Trees([]Pair{{from, into}}, "SECRET_", env(nil)); err != nil {
		t.Fatalf("a file the entrypoint did not copy was demanded: %v", err)
	}
	if _, err := os.Stat(filepath.Join(into, "c.yml")); !os.IsNotExist(err) {
		t.Error("a skipped file was recreated")
	}
}

func TestAMissingSourceIsNoSource(t *testing.T) {
	if err := Trees([]Pair{{filepath.Join(t.TempDir(), "absent"), t.TempDir()}}, "SECRET_", env(nil)); err != nil {
		t.Fatalf("an absent source failed: %v", err)
	}
}
```

Run: `$NIX go -C /home/paul/git/spawnery test ./internal/substitute/ -count=1`
Expected: FAIL to compile — `undefined: Trees`.

- [ ] **Step 2: Implement**

```go
// Package substitute fills {{ NAME }} placeholders in the files the entrypoint
// copied from a group's plugin and file sources.
package substitute

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Pair is a source tree and where the entrypoint copied it.
type Pair struct{ From, Into string }

var placeholder = regexp.MustCompile(`\{\{\s*([A-Z][A-Z0-9_]*)\s*\}\}`)

var textExtensions = map[string]bool{
	".yml": true, ".yaml": true, ".json": true, ".properties": true,
	".conf": true, ".toml": true, ".txt": true, ".cfg": true,
}

// Trees fills, in every text file below each Into that also exists below its
// From, the placeholders whose name begins with prefix. A missing variable is
// an error naming the file and the placeholder, never a value.
func Trees(pairs []Pair, prefix string, lookup func(string) (string, bool)) error {
	for _, p := range pairs {
		err := filepath.WalkDir(p.From, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) && path == p.From {
					return fs.SkipAll
				}
				return err
			}
			if d.IsDir() || !d.Type().IsRegular() || !textExtensions[strings.ToLower(filepath.Ext(path))] {
				return nil
			}
			rel, err := filepath.Rel(p.From, path)
			if err != nil {
				return err
			}
			return fill(filepath.Join(p.Into, rel), rel, prefix, lookup)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func fill(dest, rel, prefix string, lookup func(string) (string, bool)) error {
	content, err := os.ReadFile(dest)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var missing error
	out := placeholder.ReplaceAllFunc(content, func(m []byte) []byte {
		name := string(placeholder.FindSubmatch(m)[1])
		if !strings.HasPrefix(name, prefix) {
			return m
		}
		v, ok := lookup(name)
		if !ok {
			if missing == nil {
				missing = fmt.Errorf("%s: {{ %s }} has no variable %s in this container", rel, name, name)
			}
			return m
		}
		return []byte(v)
	})
	if missing != nil {
		return missing
	}
	if bytes.Equal(out, content) {
		return nil
	}
	info, err := os.Stat(dest)
	if err != nil {
		return err
	}
	return os.WriteFile(dest, out, info.Mode().Perm())
}
```

`ReplaceAllFunc` replaces each match once and does not re-scan inserted text, which is what `TestValuesArriveVerbatim` pins.

- [ ] **Step 3: Pass**

Run: `$NIX go -C /home/paul/git/spawnery test ./internal/substitute/ -count=1 -v`
Expected: all nine tests PASS.

- [ ] **Step 4: Commit**

```bash
git -C /home/paul/git/spawnery add internal/substitute
git -C /home/paul/git/spawnery commit -m "feat(substitute): fill prefixed placeholders in copied text files

Byte-wise, once per match, only in files that came from a source and
still exist where they were copied. A missing variable names the file
and the placeholder and never a value.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01JQ1B3g7MgNUnuusWCn8aW6"
```

---

### Task 4: spawnery-config --substitute, and both entrypoints

**Files:**
- Modify: `cmd/spawnery-config/main.go` (mode `--substitute PREFIX`, repeated `--pair FROM=INTO`)
- Test: `cmd/spawnery-config/main_test.go`
- Modify: `image/entrypoint.sh` (after the agent copy, line ~233), `image/velocity-entrypoint.sh` (after the agent copy, line ~204)
- Test: `image/entrypoint_test.go`, `image/velocity_entrypoint_test.go`

**Interfaces:**
- Consumes: `substitute.Trees`, `substitute.Pair` (Task 3); `SPAWNERY_SUBSTITUTION_PREFIX` (Task 2).

- [ ] **Step 1: Failing tests**

`cmd/spawnery-config/main_test.go` (the file's `run(args, stderr)` is the seam):

```go
func TestSubstituteFillsFromTheEnvironment(t *testing.T) {
	from, into := t.TempDir(), t.TempDir()
	for _, root := range []string{from, into} {
		if err := os.WriteFile(filepath.Join(root, "c.yml"), []byte("p: {{ SECRET_P }}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("SECRET_P", "s3cret")
	var stderr bytes.Buffer
	if code := run([]string{"--substitute", "SECRET_", "--pair", from + "=" + into}, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	b, _ := os.ReadFile(filepath.Join(into, "c.yml"))
	if string(b) != "p: s3cret\n" {
		t.Errorf("got %q", b)
	}
}

func TestSubstituteFailsWithoutLeakingAValue(t *testing.T) {
	from, into := t.TempDir(), t.TempDir()
	for _, root := range []string{from, into} {
		if err := os.WriteFile(filepath.Join(root, "c.yml"), []byte("{{ SECRET_A }} {{ SECRET_B }}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("SECRET_A", "hunter2")
	var stderr bytes.Buffer
	if code := run([]string{"--substitute", "SECRET_", "--pair", from + "=" + into}, &stderr); code == 0 {
		t.Fatal("a missing variable exited 0")
	}
	if !strings.Contains(stderr.String(), "SECRET_B") || strings.Contains(stderr.String(), "hunter2") {
		t.Errorf("stderr = %q", stderr.String())
	}
}
```

`image/entrypoint_test.go` — the stub `spawnery-config` already prints its argv (`SPAWNERY_CONFIG_ARGV:`):

```go
func TestSubstituteRunsOnlyWithAPrefix(t *testing.T) {
	dir := t.TempDir()
	out, err := runEntrypoint(t, dir, 0, "SPAWNERY_SUBSTITUTION_PREFIX=SECRET_")
	if err != nil {
		t.Fatalf("entrypoint: %v\n%s", err, out)
	}
	if !strings.Contains(out, "SPAWNERY_CONFIG_ARGV: --substitute SECRET_ --pair ") {
		t.Errorf("no substitute call:\n%s", out)
	}
	if strings.Index(out, "--substitute") > strings.Index(out, "JAVA_ARGV") {
		t.Error("substitution ran after the JVM started")
	}
	out, err = runEntrypoint(t, t.TempDir(), 0)
	if err != nil {
		t.Fatalf("entrypoint: %v\n%s", err, out)
	}
	if strings.Contains(out, "--substitute") {
		t.Errorf("substitute ran without a prefix:\n%s", out)
	}
}

func TestEntrypointStopsIfSubstitutionRefuses(t *testing.T) {
	out, err := runEntrypoint(t, t.TempDir(), 1, "SPAWNERY_SUBSTITUTION_PREFIX=SECRET_")
	if err == nil || strings.Contains(out, "JAVA_ARGV") {
		t.Errorf("the JVM started after a refusal:\n%s", out)
	}
}
```

The stub exits with `configExit` for every call, so the second test's first `spawnery-config --flavor` call also exits 1 and the test would pass for the wrong reason. Make the stub exit with `configExit` only when `$1` is `--substitute` (add a `substituteExit` parameter to `stubTools`/`runScript`, default 0, passing `configExit` through to the flavor call as today), and set it to 1 in `TestEntrypointStopsIfSubstitutionRefuses` with `configExit` 0. Ledger that helper change.

Mirror both tests in `image/velocity_entrypoint_test.go` with its own run helper.

Run: `$NIX go -C /home/paul/git/spawnery test ./cmd/spawnery-config/ ./image/ -count=1`
Expected: FAIL — unknown flag `--substitute`; no substitute call in the entrypoint output.

- [ ] **Step 2: Implement `--substitute`**

At the top of `run`, before the flavor handling:

```go
	if len(args) > 0 && args[0] == "--substitute" {
		return runSubstitute(args[1:], stderr)
	}
```

and:

```go
// pairs collects repeated --pair FROM=INTO flags.
type pairs []substitute.Pair

func (p *pairs) String() string { return fmt.Sprint(*p) }

func (p *pairs) Set(v string) error {
	from, into, ok := strings.Cut(v, "=")
	if !ok || from == "" || into == "" {
		return fmt.Errorf("want FROM=INTO, got %q", v)
	}
	*p = append(*p, substitute.Pair{From: from, Into: into})
	return nil
}

// runSubstitute fills the placeholders of spec.substitution in what the
// entrypoint copied; see internal/substitute.
func runSubstitute(args []string, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "spawnery-config: --substitute needs a prefix")
		return 2
	}
	prefix := args[0]
	fs := flag.NewFlagSet("spawnery-config --substitute", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var ps pairs
	fs.Var(&ps, "pair", "a source and where it was copied, FROM=INTO; repeatable")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if err := substitute.Trees(ps, prefix, os.LookupEnv); err != nil {
		_, _ = fmt.Fprintf(stderr, "spawnery-config: %v\n", err)
		return 1
	}
	return 0
}
```

(imports: `strings`, `github.com/spawnery/spawnery/internal/substitute`).

- [ ] **Step 3: Wire both entrypoints**

`image/entrypoint.sh`, directly after the agent copy's `fi` (before the JVM flags):

```sh
# spec.substitution: fill the placeholders in what was just copied, and only
# there. After the agent copy, so the agent jar is never touched; before the
# JVM, so a missing secret stops this start instead of a plugin later.
if [ -n "${SPAWNERY_SUBSTITUTION_PREFIX:-}" ]; then
	spawnery-config --substitute "$SPAWNERY_SUBSTITUTION_PREFIX" \
		--pair "$PLUGIN_SOURCE=plugins" --pair "$FILE_SOURCE=." || exit 1
fi
```

The same block in `image/velocity-entrypoint.sh` after its agent copy. Both scripts run with the server directory as working directory, so `plugins` and `.` are the copies' destinations; `FILE_SOURCE` and `PLUGIN_SOURCE` are set earlier in both scripts even when their directory does not exist, and `Trees` treats an absent source as none.

- [ ] **Step 4: Pass**

Run: `$NIX go -C /home/paul/git/spawnery test ./cmd/spawnery-config/ ./image/ ./internal/substitute/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git -C /home/paul/git/spawnery add cmd/spawnery-config image internal/substitute
git -C /home/paul/git/spawnery commit -m "feat(image): the entrypoints fill spec.substitution's placeholders

spawnery-config --substitute PREFIX --pair FROM=INTO fills the copied
plugins and files before the JVM starts; a missing variable stops the
start. Both entrypoints run it only when the operator passed a prefix.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01JQ1B3g7MgNUnuusWCn8aW6"
```

---

### Task 5: The real image fills a placeholder

**Files:**
- Modify: `hack/image-test.sh`

- [ ] **Step 1: Add the check**

Read the script first: it creates `$CONFDIR`, a volume and runs the image with `--read-only`, then waits for the server. Add a second, short run after the existing checks (reuse its variables and `cleanup` trap; add the new names to `cleanup`):

```sh
# spec.substitution, end to end in the real image: a mounted plugin source
# with a placeholder, the prefix and the value in the environment.
SUBDIR=$(mktemp -d)
mkdir -p "$SUBDIR/Demo"
printf 'password: {{ SECRET_DEMO }}\n' >"$SUBDIR/Demo/config.yml"
chmod -R a+rX "$SUBDIR"
SUBNAME="$NAME-substitute"
"$CONTAINER" run -d --name "$SUBNAME" --network none \
	--read-only --tmpfs /tmp:rw,exec,size=256m --tmpfs /data:rw,exec,size=512m \
	-v "$CONFDIR:/etc/spawnery/config:ro" \
	-v "$SUBDIR:/var/run/spawnery/plugins:ro" \
	-e SPAWNERY_SUBSTITUTION_PREFIX=SECRET_ -e 'SECRET_DEMO=a$b&c' \
	"$IMAGE" >/dev/null
for _ in $(seq 1 60); do
	"$CONTAINER" exec "$SUBNAME" test -f /data/plugins/Demo/config.yml 2>/dev/null && break
	sleep 1
done
got=$("$CONTAINER" exec "$SUBNAME" cat /data/plugins/Demo/config.yml)
if [ "$got" != 'password: a$b&c' ]; then
	echo "substitution: got '$got', want 'password: a\$b&c'" >&2
	exit 1
fi
"$CONTAINER" rm -f "$SUBNAME" >/dev/null
rm -rf "$SUBDIR"
echo "substitution: ok"
```

Match the config mount path and `/data` handling to the existing `run` in the script (read them from its first `"$CONTAINER" run`); the values above are the pattern, the script's own paths win.

- [ ] **Step 2: Run it, watching it fail first**

Temporarily remove the entrypoint block from Task 4 in a throwaway worktree (`git worktree add --detach /tmp/claude-1000/st-mut HEAD`, edit `image/entrypoint.sh` there), run `$NIX make -C /tmp/claude-1000/st-mut image-test`.
Expected: `substitution: got 'password: {{ SECRET_DEMO }}'` and exit 1. Remove the worktree.

Then in the real tree: `$NIX make -C /home/paul/git/spawnery image-test`
Expected: `substitution: ok`.

This needs a container runtime; on `dev` check that one exists first (`hostname`, `command -v podman docker`). If none, run it on `paul-desktop` or ledger that it was run by CI only.

- [ ] **Step 3: Commit**

```bash
git -C /home/paul/git/spawnery add hack/image-test.sh
git -C /home/paul/git/spawnery commit -m "test(image): the real image fills a placeholder from its environment

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01JQ1B3g7MgNUnuusWCn8aW6"
```

---

### Task 6: Docs

**Files:**
- Modify: `docs/guides/plugins-from-a-volume.md` (an image as the source; substitution)
- Modify: `docs/guides/mounts-and-files.md` (one line pointing to it)

- [ ] **Step 1: Write**

In `plugins-from-a-volume.md`, a new section after the claim description:

```markdown
## From an image instead of a claim

`extraPlugins` and `extraFiles` can name an image instead of a claim:

    spec:
      extraPlugins:
        image: registry.example.net/lobby-plugins@sha256:…
      extraFiles:
        image: registry.example.net/lobby-files@sha256:…

The image's filesystem root is what the claim's root would be. It is mounted
read-only as an image volume and copied exactly as a claim is. Every node pulls
and caches it on its own, so no single server holds the group's plugins, and a
new digest rolls the group like any other change. The pod's
`imagePullSecrets` (from the Network) apply. An image source needs neither
`--allow-plugin-volumes` nor `--allow-file-volumes`.

## Placeholders filled at start

    spec:
      substitution:
        prefix: SECRET_
      env:
        - name: SECRET_DB_PASSWORD
          valueFrom:
            secretKeyRef: { name: lobby-db, key: password }

After copying, the entrypoint replaces every `{{ SECRET_… }}` in the copied text
files (`.yml`, `.yaml`, `.json`, `.properties`, `.conf`, `.toml`, `.txt`,
`.cfg`) with the variable of that name, verbatim. A placeholder whose variable
is missing stops the start and names the file and the placeholder. This keeps
secrets out of the image or claim: the artifact carries the placeholder, the
cluster the value.
```

In `mounts-and-files.md`, where `extraPlugins` is introduced, add: "It can also name an image; see [From an image instead of a claim](plugins-from-a-volume.md#from-an-image-instead-of-a-claim)."

Run: `$NIX make -C /home/paul/git/spawnery docs-length-lint`
Expected: exit 0.

- [ ] **Step 2: Commit**

```bash
git -C /home/paul/git/spawnery add docs
git -C /home/paul/git/spawnery commit -m "docs(guides): plugins and files from an image, and placeholders

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01JQ1B3g7MgNUnuusWCn8aW6"
```

---

## After the last task

Whole suite, final review, then the release commit `chore: 0.11.0, …` (operator, chart and images; paragraphs in `flake.nix` and `Chart.yaml`) as for 0.10.0.

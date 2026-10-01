/*
Copyright paul_wtf.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package prune

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/spawnery/spawnery/internal/sourcetree"
)

// claim writes files (a trailing slash makes an empty directory) under a new
// data directory.
func claim(t *testing.T, files ...string) string {
	t.Helper()
	dir := t.TempDir()
	put(t, dir, files...)
	return dir
}

func put(t *testing.T, dir string, files ...string) {
	t.Helper()
	for _, f := range files {
		p := filepath.Join(dir, f)
		if strings.HasSuffix(f, "/") {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(f), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// left lists every file below dir, relative, sorted.
func left(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// noMounts writes an empty mount table.
func noMounts(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "mountinfo")
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func run(dir string, keep []string, mountinfo string, pairs ...sourcetree.Pair) error {
	return Run(dir, keep, mountinfo, pairs, io.Discard)
}

func TestPrune(t *testing.T) {
	tests := []struct {
		name  string
		files []string
		keep  []string
		want  []string
	}{
		{
			name:  "unmatched files and directories go",
			files: []string{"server.properties", "logs/latest.log", "empty/", "worlds/world/level.dat"},
			keep:  []string{"worlds/world"},
			want:  []string{"worlds/world/level.dat"},
		},
		{
			name:  "a matched directory is kept whole",
			files: []string{"worlds/world/region/r.0.0.mca", "worlds/world/data/a.dat", "worlds/old/x"},
			keep:  []string{"worlds/world"},
			want:  []string{"worlds/world/data/a.dat", "worlds/world/region/r.0.0.mca"},
		},
		{
			name:  "a state directory survives beside files that go",
			files: []string{"plugins/Challenges/internal/db.json", "plugins/Challenges/config.yml", "plugins/old.jar"},
			keep:  []string{"plugins/Challenges/internal"},
			want:  []string{"plugins/Challenges/internal/db.json"},
		},
		{
			name:  "globs match within a segment",
			files: []string{"worlds/world/level.dat", "worlds/world/level.dat_old", "worlds/world/session.lock", "worlds/world/dimensions/minecraft/map1/a", "worlds/world/dimensions/minecraft/other/b"},
			keep:  []string{"worlds/world/level.dat*", "worlds/world/dimensions/minecraft/map*"},
			want:  []string{"worlds/world/dimensions/minecraft/map1/a", "worlds/world/level.dat", "worlds/world/level.dat_old"},
		},
		{
			name:  "a glob in a middle segment",
			files: []string{"plugins/Challenges/internal/x.json", "plugins/Challenges/config.yml"},
			keep:  []string{"plugins/*/internal"},
			want:  []string{"plugins/Challenges/internal/x.json"},
		},
		{
			name:  "a question mark is one character",
			files: []string{"a1/f", "a12/f"},
			keep:  []string{"a?"},
			want:  []string{"a1/f"},
		},
		{
			name:  "an empty claim is fine",
			files: nil,
			keep:  []string{"worlds/world"},
			want:  nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := claim(t, tc.files...)
			if err := run(dir, tc.keep, noMounts(t)); err != nil {
				t.Fatal(err)
			}
			if got := left(t, dir); fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("left %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLostAndFoundIsNotTouched(t *testing.T) {
	dir := claim(t, "lost+found/orphan", "junk")
	if err := run(dir, []string{"worlds"}, noMounts(t)); err != nil {
		t.Fatal(err)
	}
	if got := left(t, dir); fmt.Sprint(got) != "[lost+found/orphan]" {
		t.Errorf("left %v", got)
	}
}

func TestMountPointsAndTheirParentsAreKept(t *testing.T) {
	dir := claim(t, "mods/ro/a", "mods/other", "data/rw/b", "data/rw/deeper/c", "junk")
	root, _ := filepath.EvalSymlinks(dir)
	info := filepath.Join(t.TempDir(), "mountinfo")
	table := fmt.Sprintf("1 0 0:1 / / rw - overlay overlay rw\n"+
		"2 1 0:2 / %s/mods/ro ro,nosuid - ext4 /dev/a ro\n"+
		"3 1 0:3 / %s/data/rw rw,relatime - ext4 /dev/b rw\n", root, root)
	if err := os.WriteFile(info, []byte(table), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(dir, []string{"worlds"}, info); err != nil {
		t.Fatal(err)
	}
	want := "[data/rw/b data/rw/deeper/c mods/ro/a]"
	if got := left(t, dir); fmt.Sprint(got) != want {
		t.Errorf("left %v, want %s", got, want)
	}
}

func TestAMountPointWithASpaceIsUnescaped(t *testing.T) {
	dir := claim(t, "my data/a", "junk")
	root, _ := filepath.EvalSymlinks(dir)
	info := filepath.Join(t.TempDir(), "mountinfo")
	table := fmt.Sprintf("2 1 0:2 / %s/my\\040data rw - ext4 /dev/a rw\n", root)
	if err := os.WriteFile(info, []byte(table), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(dir, []string{"worlds"}, info); err != nil {
		t.Fatal(err)
	}
	if got := left(t, dir); fmt.Sprint(got) != "[my data/a]" {
		t.Errorf("left %v", got)
	}
}

func TestALevelDatNoEntryKeepsRefusesAndDeletesNothing(t *testing.T) {
	dir := claim(t, "junk", "worlds/world/level.dat", "old/world/level.dat")
	err := run(dir, []string{"worlds/world"}, noMounts(t))
	if err == nil || !strings.Contains(err.Error(), "old") {
		t.Fatalf("err = %v, want a refusal naming old", err)
	}
	if got := left(t, dir); len(got) != 3 {
		t.Errorf("deleted before refusing: %v", got)
	}
}

func TestASourceCarryingAKeptPathRefusesAndDeletesNothing(t *testing.T) {
	dir := claim(t, "junk", "plugins/Challenges/internal/db.json")
	src := claim(t, "Challenges/internal/seed.json", "Challenges/config.yml")
	err := run(dir, []string{"plugins/Challenges/internal"}, noMounts(t), sourcetree.Pair{From: src, Into: "plugins"})
	if err == nil || !strings.Contains(err.Error(), "plugins/Challenges/internal/seed.json") {
		t.Fatalf("err = %v, want a refusal naming the path", err)
	}
	if got := left(t, dir); len(got) != 2 {
		t.Errorf("deleted before refusing: %v", got)
	}
}

func TestASourceThatShipsOnlyUnkeptPathsIsFine(t *testing.T) {
	dir := claim(t, "junk")
	src := claim(t, "Challenges/config.yml", "lost+found/x")
	missing := filepath.Join(t.TempDir(), "absent")
	err := run(dir, []string{"plugins/Challenges/internal"}, noMounts(t),
		sourcetree.Pair{From: src, Into: "plugins"}, sourcetree.Pair{From: missing, Into: "."})
	if err != nil {
		t.Fatal(err)
	}
}

func TestASymlinkIsRemovedAndItsTargetStays(t *testing.T) {
	outside := claim(t, "precious")
	dir := claim(t, "keep/a")
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := run(dir, []string{"keep"}, noMounts(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "link")); err == nil {
		t.Error("the link survived")
	}
	if got := left(t, outside); fmt.Sprint(got) != "[precious]" {
		t.Errorf("target changed: %v", got)
	}
}

func TestEachRemovalIsLogged(t *testing.T) {
	dir := claim(t, "keep/a", "plugins/old.jar")
	var log strings.Builder
	if err := Run(dir, []string{"keep"}, noMounts(t), nil, &log); err != nil {
		t.Fatal(err)
	}
	if log.String() != "spawnery: keep: removing plugins\n" {
		t.Errorf("log = %q", log.String())
	}
}

func TestABadEntryRefuses(t *testing.T) {
	for _, k := range []string{"", "/x", "a/../b", "a//b", "a/./b", "a[b"} {
		if err := run(claim(t), []string{k}, "none"); err == nil {
			t.Errorf("entry %q was accepted", k)
		}
	}
}

func TestAMissingMountinfoRefusesAndDeletesNothing(t *testing.T) {
	dir := claim(t, "junk", "keep/a")
	if err := run(dir, []string{"keep"}, filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("a missing mountinfo was accepted")
	}
	if got := left(t, dir); len(got) != 2 {
		t.Errorf("deleted before refusing: %v", got)
	}
}

func TestARelativeRootStillSeesAbsoluteMountPoints(t *testing.T) {
	dir := claim(t, "mods/ro/a", "data/rw/b", "junk")
	root, _ := filepath.EvalSymlinks(dir)
	info := filepath.Join(t.TempDir(), "mountinfo")
	table := fmt.Sprintf("2 1 0:2 / %s/mods/ro ro - ext4 /dev/a ro\n3 1 0:3 / %s/data/rw rw - ext4 /dev/b rw\n", root, root)
	if err := os.WriteFile(info, []byte(table), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	if err := run(".", []string{"worlds"}, info); err != nil {
		t.Fatal(err)
	}
	want := "[data/rw/b mods/ro/a]"
	if got := left(t, dir); fmt.Sprint(got) != want {
		t.Errorf("left %v, want %s", got, want)
	}
}

func TestEveryShapeOfAWorldRefusesAndDeletesNothing(t *testing.T) {
	tests := map[string][]string{
		"rename leftovers only": {"old/world/level.dat_old", "old/world/level.dat_new"},
		"a bare region":         {"old/world/region/r.0.0.mca"},
		"an empty region":       {"old/world/region/"},
		"a stray mca":           {"old/r.0.0.mca"},
	}
	for name, files := range tests {
		t.Run(name, func(t *testing.T) {
			dir := claim(t, append([]string{"keep/a"}, files...)...)
			err := run(dir, []string{"keep"}, noMounts(t))
			if err == nil || !strings.Contains(err.Error(), "old") {
				t.Fatalf("err = %v, want a refusal naming old", err)
			}
			for _, p := range []string{"keep/a", "old"} {
				if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
					t.Errorf("%s was deleted before refusing: %v", p, err)
				}
			}
		})
	}
}

func TestAnUnreadableSubtreeRefusesAndDeletesNothing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every directory")
	}
	dir := claim(t, "keep/a", "junk/x", "old/sealed/y")
	sealed := filepath.Join(dir, "old", "sealed")
	if err := os.Chmod(sealed, 0o300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sealed, 0o755) })
	err := run(dir, []string{"keep"}, noMounts(t))
	if err == nil || !strings.Contains(err.Error(), "old") {
		t.Fatalf("err = %v, want a refusal naming old", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "junk", "x")); err != nil {
		t.Errorf("junk was deleted before refusing: %v", err)
	}
}

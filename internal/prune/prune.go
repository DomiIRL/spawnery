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

// Package prune deletes, at the start of a server, everything on the data
// claim that spec.storage.keep does not list.
package prune

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spawnery/spawnery/internal/sourcetree"
)

// Run deletes everything below dir that no keep entry matches, logging each
// path to log first. It refuses, before deleting anything, when that would
// delete a world or when a pair's source carries a path that keep holds
// at its destination. mountinfo is read for the mount points below dir, which
// are never entered.
func Run(dir string, keep []string, mountinfo string, pairs []sourcetree.Pair, log io.Writer) error {
	pats, err := parseKeep(keep)
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	root, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return err
	}
	mounts, err := mountsBelow(root, mountinfo)
	if err != nil {
		return err
	}
	var doomed []string
	if err := plan(root, nil, pats, mounts, &doomed); err != nil {
		return err
	}
	for _, rel := range doomed {
		world, err := holdsWorld(filepath.Join(root, rel))
		if err != nil {
			return fmt.Errorf("cannot tell whether %s holds a world: %w", rel, err)
		}
		if world {
			return fmt.Errorf("spec.storage.keep does not keep %s, which holds a world", rel)
		}
	}
	for _, p := range pairs {
		if err := refuseKept(p, pats); err != nil {
			return err
		}
	}
	for _, rel := range doomed {
		_, _ = fmt.Fprintf(log, "spawnery: keep: removing %s\n", rel)
		if err := os.RemoveAll(filepath.Join(root, rel)); err != nil {
			return err
		}
	}
	return nil
}

func parseKeep(keep []string) ([][]string, error) {
	var pats [][]string
	for _, k := range keep {
		segs := strings.Split(k, "/")
		for _, s := range segs {
			if _, err := path.Match(s, ""); err != nil || s == "" || s == "." || s == ".." {
				return nil, fmt.Errorf("spec.storage.keep entry %q is not a relative path of plain, * and ? segments", k)
			}
		}
		pats = append(pats, segs)
	}
	return pats, nil
}

// segMatch reports whether pattern segments pat match the leading segments of
// rel; with exact set, rel must be as long as pat.
func segMatch(pat, rel []string, exact bool) bool {
	if len(pat) > len(rel) || exact && len(pat) != len(rel) {
		return false
	}
	for i, s := range pat {
		if ok, _ := path.Match(s, rel[i]); !ok {
			return false
		}
	}
	return true
}

// kept reports whether rel is, or lies below, a path a pattern matches.
func kept(pats [][]string, rel []string) bool {
	for _, p := range pats {
		if segMatch(p, rel, false) {
			return true
		}
	}
	return false
}

// plan appends to doomed the relative paths below root that are neither kept
// nor on the way to something that is.
func plan(root string, rel []string, pats, mounts [][]string, doomed *[]string) error {
	entries, err := os.ReadDir(filepath.Join(append([]string{root}, rel...)...))
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		// lost+found exists at the root of a freshly formatted volume only
		if name == "lost+found" && len(rel) == 0 {
			continue
		}
		r := append(append([]string{}, rel...), name)
		switch {
		case kept(pats, r) || isOneOf(mounts, r):
		case e.Type()&fs.ModeType == fs.ModeDir && onTheWay(pats, mounts, r):
			if err := plan(root, r, pats, mounts, doomed); err != nil {
				return err
			}
		default:
			*doomed = append(*doomed, strings.Join(r, "/"))
		}
	}
	return nil
}

func isOneOf(set [][]string, rel []string) bool {
	for _, s := range set {
		if strings.Join(s, "/") == strings.Join(rel, "/") {
			return true
		}
	}
	return false
}

// onTheWay reports whether rel is a proper ancestor of a path a pattern could
// match or of a mount point.
func onTheWay(pats, mounts [][]string, rel []string) bool {
	for _, p := range pats {
		if len(p) > len(rel) && segMatch(p[:len(rel)], rel, true) {
			return true
		}
	}
	for _, m := range mounts {
		if len(m) > len(rel) && strings.Join(m[:len(rel)], "/") == strings.Join(rel, "/") {
			return true
		}
	}
	return false
}

// holdsWorld reports whether p is, or holds, a level.dat or one of its
// rename leftovers (level.dat_old, level.dat_new), a region directory or an
// .mca file. It fails on any path it cannot read.
func holdsWorld(p string) (bool, error) {
	found := false
	err := filepath.WalkDir(p, func(q string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		n := d.Name()
		if d.IsDir() && n == "region" || !d.IsDir() && (strings.HasPrefix(n, "level.dat") || strings.HasSuffix(n, ".mca")) {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	return found, err
}

// refuseKept refuses when the source carries a path keep holds at its
// destination, which the copy would silently overwrite with the shipped file.
func refuseKept(p sourcetree.Pair, pats [][]string) error {
	return p.Walk(func(q string, d fs.DirEntry) error {
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(p.From, q)
		if err != nil {
			return err
		}
		dest := filepath.ToSlash(filepath.Join(p.Into, rel))
		if kept(pats, strings.Split(dest, "/")) {
			return fmt.Errorf("%s is shipped by a source and kept by spec.storage.keep, so the saved file would be overwritten on every start", dest)
		}
		return nil
	})
}

// mountsBelow lists, relative to root, the mount points in a mountinfo file.
// mountinfo writes a space in a path as \040.
func mountsBelow(root, mountinfo string) ([][]string, error) {
	b, err := os.ReadFile(mountinfo)
	if err != nil {
		return nil, err
	}
	var out [][]string
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		if rel, ok := strings.CutPrefix(unescape(f[4]), root+"/"); ok {
			out = append(out, strings.Split(rel, "/"))
		}
	}
	return out, nil
}

func unescape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 <= len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

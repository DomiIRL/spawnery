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

// Package sourcetree walks the claim-backed source trees the entrypoint copies
// into the data directory.
package sourcetree

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Pair is a source tree and where the entrypoint copies it.
type Pair struct{ From, Into string }

// Walk calls fn for every entry below p.From that the entrypoint copies. A
// source that does not exist is empty. At the top level the copy's globs
// (* and .[!.]*) never match a name starting with two dots, it skips a
// dangling symlink, and it skips lost+found, the ext4 claim's own root-owned
// 0700 directory, by name.
func (p Pair) Walk(fn func(path string, d fs.DirEntry) error) error {
	return filepath.WalkDir(p.From, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && path == p.From {
				return fs.SkipAll
			}
			return err
		}
		if filepath.Dir(path) == filepath.Clean(p.From) && path != filepath.Clean(p.From) && !copied(path, d) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		return fn(path, d)
	})
}

func copied(path string, d fs.DirEntry) bool {
	n := d.Name()
	if n == "lost+found" || strings.HasPrefix(n, "..") {
		return false
	}
	if d.Type()&fs.ModeSymlink != 0 {
		if _, err := os.Stat(path); err != nil {
			return false
		}
	}
	return true
}

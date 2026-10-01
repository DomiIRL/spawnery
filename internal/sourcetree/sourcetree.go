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
	"path/filepath"
)

// Pair is a source tree and where the entrypoint copies it.
type Pair struct{ From, Into string }

// Walk calls fn for every entry below p.From. A source that does not exist is
// empty, and the root lost+found of an ext4 claim is skipped.
func (p Pair) Walk(fn func(path string, d fs.DirEntry) error) error {
	return filepath.WalkDir(p.From, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && path == p.From {
				return fs.SkipAll
			}
			return err
		}
		if d.IsDir() && d.Name() == "lost+found" && filepath.Dir(path) == filepath.Clean(p.From) {
			// The filesystem's own directory on an ext4 claim, root-owned and
			// 0700; the entrypoint skips it by name for the same reason.
			return fs.SkipDir
		}
		return fn(path, d)
	})
}

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
			if d.IsDir() && d.Name() == "lost+found" && filepath.Dir(path) == filepath.Clean(p.From) {
				// mkfs's own directory on an ext4 claim, root-owned and 0700;
				// the entrypoint skips it by name for the same reason.
				return fs.SkipDir
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

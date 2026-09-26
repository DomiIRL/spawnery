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

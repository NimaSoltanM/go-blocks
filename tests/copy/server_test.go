package copycheck

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Keep the maintained example honest: it must contain the actual block source,
// not a substitute implementation or an import back into the development module.
func TestExampleContainsCurrentServerSource(t *testing.T) {
	sourceDir := "../../blocks/server"
	exampleDir := "../../examples/basic-api/internal/server"
	files := sourceFiles(t, sourceDir)
	if len(files) == 0 || !slices.Equal(files, sourceFiles(t, exampleDir)) {
		t.Fatal("server block and example contain different source files")
	}
	for _, name := range files {
		source, err := os.ReadFile(filepath.Join(sourceDir, name))
		if err != nil {
			t.Fatal(err)
		}
		copied, err := os.ReadFile(filepath.Join(exampleDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(source, copied) {
			t.Errorf("refresh examples/basic-api/internal/server/%s from blocks/server/%s", name, name)
		}
	}
}

func sourceFiles(t *testing.T, path string) []string {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") && !strings.HasSuffix(entry.Name(), "_test.go") {
			names = append(names, entry.Name())
		}
	}
	return names
}

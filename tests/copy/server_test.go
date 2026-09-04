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
	assertSourceCopy(t, "../../blocks/server", "../../examples/basic-api/internal/server")
	assertSourceCopy(t, "../../blocks/server", "../../examples/phone-auth-api/internal/server")
}

func TestPhoneAuthExampleContainsCurrentAuthSourceAndMigrations(t *testing.T) {
	assertSourceCopy(t, "../../blocks/auth", "../../examples/phone-auth-api/internal/auth")
	for _, name := range []string{"000001_create_auth_users.up.sql", "000001_create_auth_users.down.sql"} {
		assertSameFile(t,
			filepath.Join("../../blocks/auth/migrations", name),
			filepath.Join("../../examples/phone-auth-api/internal/auth/migrations", name),
		)
	}
}

func assertSourceCopy(t *testing.T, sourceDir, exampleDir string) {
	t.Helper()
	files := sourceFiles(t, sourceDir)
	if len(files) == 0 || !slices.Equal(files, sourceFiles(t, exampleDir)) {
		t.Fatalf("%s and %s contain different source files", sourceDir, exampleDir)
	}
	for _, name := range files {
		assertSameFile(t, filepath.Join(sourceDir, name), filepath.Join(exampleDir, name))
	}
}

func assertSameFile(t *testing.T, sourcePath, examplePath string) {
	t.Helper()
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	copied, err := os.ReadFile(examplePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(source, copied) {
		t.Errorf("refresh %s from %s", examplePath, sourcePath)
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

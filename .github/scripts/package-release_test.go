package main

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackageLibraryIncludesLicense(t *testing.T) {
	t.Chdir(t.TempDir())
	library := []byte("offline shared library fixture")
	license := []byte("MIT License\nCopyright (c) 2026 massiveits\nCopyright (c) 2026 dillonzq\n")
	for name, content := range map[string][]byte{"cpa-opencode-go.so": library, "LICENSE": license} {
		if err := os.WriteFile(name, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	data, err := packageLibrary("cpa-opencode-go.so", "release.zip")
	if err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile("release.zip")
	if err != nil || !bytes.Equal(data, written) {
		t.Fatalf("returned archive differs from the file used for release checksums: %v", err)
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if len(reader.File) != 2 {
		t.Fatalf("archive entries = %d, want library and license", len(reader.File))
	}
	want := map[string][]byte{"cpa-opencode-go.so": library, "LICENSE": license}
	for _, file := range reader.File {
		expected, ok := want[file.Name]
		if !ok || filepath.Base(file.Name) != file.Name || !file.Mode().IsRegular() {
			t.Fatalf("unexpected archive entry: %s (%s)", file.Name, file.Mode())
		}
		handle, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(handle)
		handle.Close()
		if err != nil || !bytes.Equal(content, expected) {
			t.Fatalf("archive entry %s differs from its source: %v", file.Name, err)
		}
		if file.Name == "cpa-opencode-go.so" && file.Mode().Perm() != 0o755 {
			t.Fatalf("library mode = %s, want 0755", file.Mode())
		}
		delete(want, file.Name)
	}
}

func TestPackageLibraryRequiresLicense(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("cpa-opencode-go.so", []byte("offline shared library fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := packageLibrary("cpa-opencode-go.so", "release.zip"); err == nil || !strings.Contains(err.Error(), "read license") {
		t.Fatalf("missing license must fail release packaging, got %v", err)
	}
}

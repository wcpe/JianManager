package jdk

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeArchivePath(t *testing.T) {
	dest := t.TempDir()
	cases := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"normal", "jdk-21/bin/java", false},
		{"nested", "a/b/c.txt", false},
		{"slip-rel", "../etc/passwd", true},
		{"slip-deep", "a/../../etc/passwd", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := sanitizeArchivePath(dest, c.input)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got path %q", c.input, out)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if !strings.HasPrefix(out, dest) {
				t.Fatalf("path %q escapes dest %q", out, dest)
			}
			if filepath.Clean(out) != out {
				t.Fatalf("path %q not clean", out)
			}
		})
	}
}

func TestParseMajor(t *testing.T) {
	cases := map[string]int{
		"21.0.4+9":  21,
		"17":        17,
		"17.0.12":   17,
		"1.8.0_412": 8,
		"11.0.20+8": 11,
		"":          0,
		"unknown":   0,
	}
	for v, want := range cases {
		if got := parseMajor(v); got != want {
			t.Errorf("parseMajor(%q) = %d, want %d", v, got, want)
		}
	}
}

func TestNormalizeVendor(t *testing.T) {
	cases := map[string]string{
		"Eclipse Adoptium": "Temurin",
		"Azul Systems":     "Zulu",
		"Amazon Corretto":  "Corretto",
		"Microsoft":        "OpenJDK",
		"":                 "Unknown",
		"Random Distributor": "Random Distributor",
	}
	for in, want := range cases {
		if got := normalizeVendor(in); got != want {
			t.Errorf("normalizeVendor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestListAndRemoveOnEmptyDir(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(filepath.Join(dir, "jdks"), nil)
	infos, err := m.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(infos) != 0 {
		t.Fatalf("empty dir, got %d jdks", len(infos))
	}
	if err := m.Remove(filepath.Join(dir, "jdks", "no-such")); err != nil {
		t.Fatalf("Remove non-existent: %v", err)
	}
}

func TestRemoveBlocksOutsideRoot(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(filepath.Join(dir, "jdks"), nil)
	if err := m.Remove(filepath.Join(dir, "outside")); err == nil {
		t.Fatalf("expected error removing outside root")
	}
}

func TestBuildDownloadURL(t *testing.T) {
	if _, err := buildDownloadURL("Temurin", 21, "x64"); err != nil {
		t.Fatalf("Temurin URL failed: %v", err)
	}
	if _, err := buildDownloadURL("Zulu", 21, "x64"); err == nil {
		t.Fatalf("Zulu should return not-implemented")
	}
	if _, err := buildDownloadURL("Random", 21, "x64"); err == nil {
		t.Fatalf("unknown vendor should fail")
	}
}

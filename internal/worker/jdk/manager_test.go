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
	// Temurin / Corretto 构造静态 URL，不触网，默认指向官方源。client 仅 Zulu 路径用到，此处传 nil。
	if u, err := buildDownloadURL(nil, "Temurin", 21, "x64", ""); err != nil || !strings.Contains(u, "api.adoptium.net") {
		t.Fatalf("Temurin 默认 URL 异常: url=%q err=%v", u, err)
	}
	if u, err := buildDownloadURL(nil, "Corretto", 21, "x64", ""); err != nil || !strings.Contains(u, "corretto.aws") {
		t.Fatalf("Corretto 默认 URL 异常: url=%q err=%v", u, err)
	}
	if _, err := buildDownloadURL(nil, "Random", 21, "x64", ""); err == nil {
		t.Fatalf("unknown vendor should fail")
	}
}

// TestBuildDownloadURL_MirrorConfigurable 验证下载源可经环境变量覆盖（FR-033「下载源可配」）。
func TestBuildDownloadURL_MirrorConfigurable(t *testing.T) {
	t.Setenv("JIANMANAGER_JDK_TEMURIN_BASE", "https://mirror.example.com/adoptium")
	u, err := buildDownloadURL(nil, "Temurin", 17, "x64", "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(u, "mirror.example.com/adoptium") {
		t.Fatalf("镜像基址未生效: %s", u)
	}
	if strings.Contains(u, "api.adoptium.net") {
		t.Fatalf("仍指向默认源: %s", u)
	}
}

// TestBuildDownloadURL_MirrorBaseOverridesEnv 验证 CP 下发的 mirrorBase（来自平台设置 jdk.mirror.*）
// 优先于环境变量与默认源（FR-063 平台设置真生效）。
func TestBuildDownloadURL_MirrorBaseOverridesEnv(t *testing.T) {
	t.Setenv("JIANMANAGER_JDK_TEMURIN_BASE", "https://env.example.com/adoptium")
	u, err := buildDownloadURL(nil, "Temurin", 17, "x64", "https://setting.example.com/adoptium")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(u, "setting.example.com/adoptium") {
		t.Fatalf("设置下发的镜像基址未生效: %s", u)
	}
	if strings.Contains(u, "env.example.com") || strings.Contains(u, "api.adoptium.net") {
		t.Fatalf("mirrorBase 应压过 env/默认: %s", u)
	}
}

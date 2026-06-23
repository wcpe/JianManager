package search

import "testing"

func TestMatcherDefaults(t *testing.T) {
	m := newMatcher(nil)
	cases := map[string]bool{
		"server.properties":             false,
		"plugins/Essentials/config.yml": false,
		"readme.md":                     false,
		"logs/latest.log":               true,
		"cache/x":                       true,
		"world/region/r.0.0.mca":        true,
		"world_nether/level.dat":        true,
		"plugins/foo.jar":               true,
		"server.jar":                    true,
		"backup.zip":                    true,
		".git/config":                   true,
		"node_modules/pkg/index.js":     true,
		"crash-reports/crash.txt":       true,
		"image.png":                     true,
		"data/x.dat":                    true,
		"nested/deep/plugins/inner.jar": true,
		"nested/notjar.jartxt":          false,
	}
	for rel, want := range cases {
		if got := m.ignored(rel); got != want {
			t.Errorf("ignored(%q) = %v, want %v", rel, got, want)
		}
	}
}

func TestMatcherCustom(t *testing.T) {
	m := newMatcher([]string{"secret/", "*.tmp", "vendor"})
	cases := map[string]bool{
		"secret/a.txt":   true,
		"a/secret/b.txt": true, // 目录前缀对任一段 secret 命中
		"x.tmp":          true,
		"vendor/lib.go":  true, // 路径段 vendor 命中
		"keep.txt":       false,
	}
	for rel, want := range cases {
		if got := m.ignored(rel); got != want {
			t.Errorf("custom ignored(%q) = %v, want %v", rel, got, want)
		}
	}
}

func TestLooksBinary(t *testing.T) {
	if !looksBinary([]byte("abc\x00def")) {
		t.Error("NUL byte should be binary")
	}
	if looksBinary([]byte("plain ascii text")) {
		t.Error("ascii should not be binary")
	}
	if looksBinary([]byte("中文 UTF-8 文本内容")) {
		t.Error("valid UTF-8 should not be binary")
	}
	if looksBinary(nil) {
		t.Error("empty should not be binary")
	}
}

func TestTokenize(t *testing.T) {
	toks := tokenize("online-mode=FALSE\nlevel_name world2")
	for _, want := range []string{"online", "mode", "false", "level_name", "world2"} {
		if _, ok := toks[want]; !ok {
			t.Errorf("expected token %q in %v", want, toks)
		}
	}
}

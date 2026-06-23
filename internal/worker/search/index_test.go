package search

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeFile 在 workDir 下写一个文件（自动建父目录）。
func writeFile(t *testing.T, workDir, rel, content string) string {
	t.Helper()
	p := filepath.Join(workDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	return p
}

// hitPaths 提取命中的相对路径集合。
func hitPaths(r Result) map[string]bool {
	m := map[string]bool{}
	for _, h := range r.Hits {
		m[h.Path] = true
	}
	return m
}

func TestBuildAndContentSearch(t *testing.T) {
	work := t.TempDir()
	idxRoot := t.TempDir()
	writeFile(t, work, "server.properties", "online-mode=false\nmotd=Welcome\nlevel-name=world")
	writeFile(t, work, "plugins/Essentials/config.yml", "kits:\n  starter:\n    welcome: true")
	writeFile(t, work, "readme.txt", "This server uses Paper and Essentials.")

	ix := NewIndex(idxRoot, "inst-1", nil)
	n, err := ix.Update(work)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if n != 3 {
		t.Fatalf("indexed file count = %d, want 3", n)
	}

	// 关键字 essentials 命中两个文件（config 路径不含但内容含；readme 含）。
	res, err := ix.SearchContent(work, "essentials", 50)
	if err != nil {
		t.Fatalf("SearchContent: %v", err)
	}
	paths := hitPaths(res)
	if !paths["readme.txt"] {
		t.Fatalf("expected readme.txt hit, got %v", paths)
	}

	// 命中行号与片段正确。
	res2, _ := ix.SearchContent(work, "online-mode", 50)
	if len(res2.Hits) == 0 {
		t.Fatalf("expected online-mode hit")
	}
	h := res2.Hits[0]
	if h.Path != "server.properties" || h.Line != 1 {
		t.Fatalf("hit = %+v, want server.properties:1", h)
	}
	if !strings.Contains(h.Snippet, "online-mode=false") {
		t.Fatalf("snippet = %q, want to contain online-mode=false", h.Snippet)
	}
}

func TestContentSearchCaseInsensitiveSubstring(t *testing.T) {
	work := t.TempDir()
	idxRoot := t.TempDir()
	writeFile(t, work, "a.yml", "EnableFeature: TRUE")
	ix := NewIndex(idxRoot, "inst", nil)
	if _, err := ix.Update(work); err != nil {
		t.Fatalf("Update: %v", err)
	}
	res, _ := ix.SearchContent(work, "enablefeature", 10)
	if len(res.Hits) != 1 {
		t.Fatalf("case-insensitive match failed: %+v", res.Hits)
	}
}

func TestContentSearchMultiTokenAND(t *testing.T) {
	work := t.TempDir()
	idxRoot := t.TempDir()
	writeFile(t, work, "match.txt", "alpha beta gamma on one line")
	writeFile(t, work, "partial.txt", "alpha only here\nbeta on another line")
	ix := NewIndex(idxRoot, "inst", nil)
	if _, err := ix.Update(work); err != nil {
		t.Fatalf("Update: %v", err)
	}
	// "alpha beta" 作为子串只在 match.txt 同一行出现；partial.txt 两词分行，子串不连续。
	res, _ := ix.SearchContent(work, "alpha beta", 10)
	paths := hitPaths(res)
	if !paths["match.txt"] {
		t.Fatalf("expected match.txt for 'alpha beta', got %v", paths)
	}
	if paths["partial.txt"] {
		t.Fatalf("partial.txt should not match substring 'alpha beta', got %v", paths)
	}
}

func TestIncrementalUpdate(t *testing.T) {
	work := t.TempDir()
	idxRoot := t.TempDir()
	writeFile(t, work, "f.txt", "needle present")
	ix := NewIndex(idxRoot, "inst", nil)
	if _, err := ix.Update(work); err != nil {
		t.Fatalf("Update1: %v", err)
	}
	if got := len(ix.SearchContentMust(t, work, "needle").Hits); got != 1 {
		t.Fatalf("after build: needle hits = %d, want 1", got)
	}

	// 修改文件内容（移除 needle，加入 haystack）。mtime 需推进以触发增量。
	time.Sleep(10 * time.Millisecond)
	writeFile(t, work, "f.txt", "haystack only")
	if _, err := ix.Update(work); err != nil {
		t.Fatalf("Update2: %v", err)
	}
	if got := len(ix.SearchContentMust(t, work, "needle").Hits); got != 0 {
		t.Fatalf("after modify: needle hits = %d, want 0 (incremental should drop)", got)
	}
	if got := len(ix.SearchContentMust(t, work, "haystack").Hits); got != 1 {
		t.Fatalf("after modify: haystack hits = %d, want 1", got)
	}

	// 新增文件。
	writeFile(t, work, "g.txt", "needle returns")
	if _, err := ix.Update(work); err != nil {
		t.Fatalf("Update3: %v", err)
	}
	res := ix.SearchContentMust(t, work, "needle")
	if !hitPaths(res)["g.txt"] {
		t.Fatalf("after add: expected g.txt to match needle, got %v", hitPaths(res))
	}

	// 删除文件 g.txt → needle 再次无命中。
	if err := os.Remove(filepath.Join(work, "g.txt")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := ix.Update(work); err != nil {
		t.Fatalf("Update4: %v", err)
	}
	if got := len(ix.SearchContentMust(t, work, "needle").Hits); got != 0 {
		t.Fatalf("after delete: needle hits = %d, want 0", got)
	}
	if ix.fileCount() != 1 {
		t.Fatalf("file count after delete = %d, want 1 (only f.txt)", ix.fileCount())
	}
}

func TestIgnoreRulesGlobAndDir(t *testing.T) {
	work := t.TempDir()
	idxRoot := t.TempDir()
	writeFile(t, work, "config.yml", "secret-token=KEYWORD")
	writeFile(t, work, "logs/latest.log", "KEYWORD in log should be ignored")
	writeFile(t, work, "plugin.jar.txt", "this is a text decoy KEYWORD") // .jar.txt 不是 .jar，不忽略
	writeFile(t, work, "build/output.jar", "KEYWORD inside jar text")    // *.jar 忽略
	writeFile(t, work, "world/region.txt", "KEYWORD in world dir ignored")

	ix := NewIndex(idxRoot, "inst", nil)
	if _, err := ix.Update(work); err != nil {
		t.Fatalf("Update: %v", err)
	}
	res := ix.SearchContentMust(t, work, "KEYWORD")
	paths := hitPaths(res)
	if !paths["config.yml"] {
		t.Fatalf("config.yml should match, got %v", paths)
	}
	if !paths["plugin.jar.txt"] {
		t.Fatalf("plugin.jar.txt (text) should match, got %v", paths)
	}
	if paths["logs/latest.log"] {
		t.Fatalf("logs/ must be ignored, got %v", paths)
	}
	if paths["build/output.jar"] {
		t.Fatalf("*.jar must be ignored, got %v", paths)
	}
	if paths["world/region.txt"] {
		t.Fatalf("world/ must be ignored, got %v", paths)
	}
}

func TestCustomIgnoreRule(t *testing.T) {
	work := t.TempDir()
	idxRoot := t.TempDir()
	writeFile(t, work, "keep.txt", "KEYWORD keep")
	writeFile(t, work, "secret/data.txt", "KEYWORD secret")
	// 自定义忽略 secret/ 目录。
	ix := NewIndex(idxRoot, "inst", []string{"secret/"})
	if _, err := ix.Update(work); err != nil {
		t.Fatalf("Update: %v", err)
	}
	paths := hitPaths(ix.SearchContentMust(t, work, "KEYWORD"))
	if !paths["keep.txt"] {
		t.Fatalf("keep.txt should match, got %v", paths)
	}
	if paths["secret/data.txt"] {
		t.Fatalf("custom-ignored secret/ should be excluded, got %v", paths)
	}
}

func TestBinaryFileSkipped(t *testing.T) {
	work := t.TempDir()
	idxRoot := t.TempDir()
	// 不带二进制扩展，但内容含 NUL → 二进制探测拦截。
	writeFile(t, work, "blob.txt", "KEYWORD\x00\x00binarycontent")
	writeFile(t, work, "text.txt", "KEYWORD plain text")
	ix := NewIndex(idxRoot, "inst", nil)
	if _, err := ix.Update(work); err != nil {
		t.Fatalf("Update: %v", err)
	}
	paths := hitPaths(ix.SearchContentMust(t, work, "KEYWORD"))
	if !paths["text.txt"] {
		t.Fatalf("text.txt should match, got %v", paths)
	}
	if paths["blob.txt"] {
		t.Fatalf("binary blob.txt content must be skipped, got %v", paths)
	}
	// 但文件名仍应被 filename 搜索命中（已登记文件全集）。
	fnames := hitPaths(ix.SearchFilename("blob", 10))
	if !fnames["blob.txt"] {
		t.Fatalf("blob.txt should be found by filename search, got %v", fnames)
	}
}

func TestLargeFileNameOnly(t *testing.T) {
	work := t.TempDir()
	idxRoot := t.TempDir()
	// 超过 maxIndexedFileBytes 的文本文件：内容不索引，仅文件名。
	big := strings.Repeat("KEYWORD filler line\n", (maxIndexedFileBytes/20)+100)
	writeFile(t, work, "huge.log.txt", big)
	ix := NewIndex(idxRoot, "inst", nil)
	if _, err := ix.Update(work); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := len(ix.SearchContentMust(t, work, "KEYWORD").Hits); got != 0 {
		t.Fatalf("oversized file content should not be indexed, got %d hits", got)
	}
	if !hitPaths(ix.SearchFilename("huge", 10))["huge.log.txt"] {
		t.Fatalf("oversized file should still be findable by name")
	}
}

func TestFilenameSearch(t *testing.T) {
	work := t.TempDir()
	idxRoot := t.TempDir()
	writeFile(t, work, "server.properties", "x")
	writeFile(t, work, "plugins/Essentials/config.yml", "y")
	writeFile(t, work, "bukkit.yml", "z")
	ix := NewIndex(idxRoot, "inst", nil)
	if _, err := ix.Update(work); err != nil {
		t.Fatalf("Update: %v", err)
	}
	res := ix.SearchFilename("yml", 10)
	paths := hitPaths(res)
	if !paths["plugins/Essentials/config.yml"] || !paths["bukkit.yml"] {
		t.Fatalf("yml filename search = %v, want config.yml & bukkit.yml", paths)
	}
	if paths["server.properties"] {
		t.Fatalf("server.properties should not match 'yml'")
	}
	// basename 命中应排在仅路径命中之前。
	res2 := ix.SearchFilename("config", 10)
	if len(res2.Hits) == 0 || res2.Hits[0].Path != "plugins/Essentials/config.yml" {
		t.Fatalf("config filename search first hit = %+v", res2.Hits)
	}
}

func TestTruncation(t *testing.T) {
	work := t.TempDir()
	idxRoot := t.TempDir()
	var b strings.Builder
	for i := 0; i < 20; i++ {
		b.WriteString("KEYWORD line\n")
	}
	writeFile(t, work, "many.txt", b.String())
	ix := NewIndex(idxRoot, "inst", nil)
	if _, err := ix.Update(work); err != nil {
		t.Fatalf("Update: %v", err)
	}
	res, _ := ix.SearchContent(work, "KEYWORD", 5)
	if len(res.Hits) != 5 || !res.Truncated {
		t.Fatalf("truncation: hits=%d truncated=%v, want 5/true", len(res.Hits), res.Truncated)
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	work := t.TempDir()
	idxRoot := t.TempDir()
	writeFile(t, work, "a.txt", "persisted KEYWORD content")
	ix := NewIndex(idxRoot, "inst-persist", nil)
	if _, err := ix.Update(work); err != nil {
		t.Fatalf("Update: %v", err)
	}
	// 落盘文件应存在。
	if _, err := os.Stat(filepath.Join(idxRoot, "inst-persist", indexFileName)); err != nil {
		t.Fatalf("index file not persisted: %v", err)
	}

	// 新 Index 对象（模拟 Worker 重启）：不重新 Update，直接搜，应从落盘加载命中。
	ix2 := NewIndex(idxRoot, "inst-persist", nil)
	res := ix2.SearchFilename("a.txt", 10)
	if !hitPaths(res)["a.txt"] {
		t.Fatalf("filename search after reload failed: %v", hitPaths(res))
	}
	res2, _ := ix2.SearchContent(work, "KEYWORD", 10)
	if !hitPaths(res2)["a.txt"] {
		t.Fatalf("content search after reload failed: %v", hitPaths(res2))
	}
}

func TestRemove(t *testing.T) {
	work := t.TempDir()
	idxRoot := t.TempDir()
	writeFile(t, work, "a.txt", "x KEYWORD")
	ix := NewIndex(idxRoot, "inst-rm", nil)
	if _, err := ix.Update(work); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := ix.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(idxRoot, "inst-rm")); !os.IsNotExist(err) {
		t.Fatalf("index dir should be gone, stat err=%v", err)
	}
}

func TestEmptyQuery(t *testing.T) {
	work := t.TempDir()
	idxRoot := t.TempDir()
	writeFile(t, work, "a.txt", "x")
	ix := NewIndex(idxRoot, "inst", nil)
	if _, err := ix.Update(work); err != nil {
		t.Fatalf("Update: %v", err)
	}
	res, _ := ix.SearchContent(work, "   ", 10)
	if len(res.Hits) != 0 {
		t.Fatalf("empty query should yield no hits")
	}
	if len(ix.SearchFilename("", 10).Hits) != 0 {
		t.Fatalf("empty filename query should yield no hits")
	}
}

// SearchContentMust 是测试辅助：调用 SearchContent 并在出错时 t.Fatal。
func (ix *Index) SearchContentMust(t *testing.T, workDir, query string) Result {
	t.Helper()
	r, err := ix.SearchContent(workDir, query, 100)
	if err != nil {
		t.Fatalf("SearchContent(%q): %v", query, err)
	}
	return r
}

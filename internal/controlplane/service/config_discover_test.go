package service

import (
	"errors"
	"sort"
	"testing"
)

// fakeTree 用「目录相对路径 → 直接子项」描述一棵目录树，模拟 Worker.ListFiles。
type fakeTree map[string][]walkEntry

func (ft fakeTree) listDir(dir string) ([]walkEntry, error) {
	entries, ok := ft[dir]
	if !ok {
		return nil, errors.New("目录不存在")
	}
	return entries, nil
}

func pathsOf(cs []DiscoveredConfig) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Path)
	}
	sort.Strings(out)
	return out
}

func TestWalkConfigPaths_RecursiveDiscoveryFlat(t *testing.T) {
	tree := fakeTree{
		"": {
			{Name: "server.properties", IsDir: false},
			{Name: "world", IsDir: true},        // 非配置文件 / 子目录
			{Name: "plugins", IsDir: true},
			{Name: "start.sh", IsDir: false},     // 非配置扩展名，过滤掉
			{Name: "eula.txt", IsDir: false},     // txt 算配置
		},
		"world": {
			{Name: "level.dat", IsDir: false}, // 过滤
		},
		"plugins": {
			{Name: "Essentials", IsDir: true},
			{Name: "bukkit-fallback.yml", IsDir: false},
		},
		"plugins/Essentials": {
			{Name: "config.yml", IsDir: false},
		},
	}
	got, truncated := walkConfigPaths(tree.listDir, defaultConfigWalkLimits)
	if truncated {
		t.Fatalf("不应截断")
	}
	want := []string{
		"eula.txt",
		"plugins/Essentials/config.yml",
		"plugins/bukkit-fallback.yml",
		"server.properties",
	}
	gotPaths := pathsOf(got)
	if len(gotPaths) != len(want) {
		t.Fatalf("发现数量不符: got=%v want=%v", gotPaths, want)
	}
	for i := range want {
		if gotPaths[i] != want[i] {
			t.Fatalf("发现路径不符: got=%v want=%v", gotPaths, want)
		}
	}
}

func TestWalkConfigPaths_MarksSupportedSchema(t *testing.T) {
	tree := fakeTree{
		"": {
			{Name: "server.properties", IsDir: false}, // 内置 schema → supported
			{Name: "random.json", IsDir: false},       // 非内置 schema → 仅文本
		},
	}
	got, _ := walkConfigPaths(tree.listDir, defaultConfigWalkLimits)
	bySupported := map[string]bool{}
	for _, c := range got {
		bySupported[c.Path] = c.Supported
	}
	if !bySupported["server.properties"] {
		t.Fatalf("server.properties 应命中内置 schema(supported=true)")
	}
	if bySupported["random.json"] {
		t.Fatalf("random.json 不应命中内置 schema")
	}
}

func TestWalkConfigPaths_DepthLimitTruncates(t *testing.T) {
	// 构造深目录链：a/b/c/...，超过 maxDepth 后下钻应被截断。
	tree := fakeTree{
		"":      {{Name: "a", IsDir: true}, {Name: "root.yml", IsDir: false}},
		"a":     {{Name: "b", IsDir: true}, {Name: "a.yml", IsDir: false}},
		"a/b":   {{Name: "c", IsDir: true}, {Name: "b.yml", IsDir: false}},
		"a/b/c": {{Name: "deep.yml", IsDir: false}},
	}
	got, truncated := walkConfigPaths(tree.listDir, configWalkLimits{maxDepth: 2, maxDirs: 100})
	if !truncated {
		t.Fatalf("超过深度应标记 truncated")
	}
	// maxDepth=2 时遍历 ""(0)→a(1)→a/b(2)；a/b/c(3) 不下钻，deep.yml 不出现。
	for _, c := range got {
		if c.Path == "a/b/c/deep.yml" {
			t.Fatalf("超深文件不应被发现: %v", pathsOf(got))
		}
	}
}

func TestWalkConfigPaths_DirCountLimitTruncates(t *testing.T) {
	tree := fakeTree{
		"":  {{Name: "d1", IsDir: true}, {Name: "d2", IsDir: true}},
		"d1": {{Name: "x.yml", IsDir: false}},
		"d2": {{Name: "y.yml", IsDir: false}},
	}
	// maxDirs=1：只遍历根目录就停，子目录不下钻。
	got, truncated := walkConfigPaths(tree.listDir, configWalkLimits{maxDepth: 8, maxDirs: 1})
	if !truncated {
		t.Fatalf("超过目录上限应标记 truncated")
	}
	if len(got) != 0 {
		t.Fatalf("根目录无配置文件时应为空, 实际 %v", pathsOf(got))
	}
}

func TestWalkConfigPaths_SkipsUnreadableDir(t *testing.T) {
	// "secret" 目录列取报错（如权限/竞态删除），应跳过而非中断整体发现。
	tree := fakeTree{
		"": {{Name: "ok.yml", IsDir: false}, {Name: "secret", IsDir: true}},
		// 故意不提供 "secret" 的子项 → listDir 返回 error
	}
	got, _ := walkConfigPaths(tree.listDir, defaultConfigWalkLimits)
	if len(got) != 1 || got[0].Path != "ok.yml" {
		t.Fatalf("应跳过不可读目录并保留可读结果, 实际 %v", pathsOf(got))
	}
}

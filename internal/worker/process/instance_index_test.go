package process

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 实例清单快照是 jmctl 识别「这是什么服」的唯一来源，因此要保证：
// 结构正确、排序稳定、原子替换（读不到半截 JSON）、写入失败不影响实例运行。

// 快照包含 jmctl 需要的识别字段。
func TestInstanceIndex_ContainsIdentifyingFields(t *testing.T) {
	m := NewManager(t.TempDir())
	// 直接注入实例表，避免真起进程（本用例只验投影逻辑）。
	m.instances = map[string]*Instance{
		"uuid-a": {UUID: "uuid-a", Name: "survival-1", WorkDir: "/data/survival-1", ServerPort: 25565},
		"uuid-b": {UUID: "uuid-b", Name: "lobby", WorkDir: "/data/lobby"},
	}

	idx := m.buildInstanceIndexLocked()

	if idx.GeneratedAt == "" {
		t.Error("应记录生成时刻，便于判断快照新旧")
	}
	if len(idx.Instances) != 2 {
		t.Fatalf("应包含 2 个实例，实际 %d", len(idx.Instances))
	}
	byName := map[string]InstanceEntry{}
	for _, e := range idx.Instances {
		byName[e.Name] = e
	}
	a, ok := byName["survival-1"]
	if !ok {
		t.Fatal("缺少 survival-1")
	}
	if a.UUID != "uuid-a" || a.WorkDir != "/data/survival-1" || a.ServerPort != 25565 {
		t.Errorf("识别字段丢失: %+v", a)
	}
}

// 按名称排序，空名排最后：文件是给人看的，顺序必须稳定。
func TestInstanceIndex_SortedByName(t *testing.T) {
	m := NewManager(t.TempDir())
	m.instances = map[string]*Instance{
		"u3": {UUID: "u3", Name: ""},
		"u1": {UUID: "u1", Name: "bravo"},
		"u2": {UUID: "u2", Name: "alpha"},
	}

	idx := m.buildInstanceIndexLocked()
	names := make([]string, 0, len(idx.Instances))
	for _, e := range idx.Instances {
		names = append(names, e.Name)
	}
	want := []string{"alpha", "bravo", ""}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("顺序不符: 得到 %v，期望 %v", names, want)
		}
	}
}

// 写入后能被解析回来，且文件名固定为 instances.json。
func TestWriteInstanceIndex_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	m.instances = map[string]*Instance{
		"u1": {UUID: "u1", Name: "survival", WorkDir: "/data/survival"},
	}

	if err := m.WriteInstanceIndex(dir); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, InstanceIndexFilename))
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	var idx InstanceIndex
	if err := json.Unmarshal(raw, &idx); err != nil {
		t.Fatalf("JSON 不合法（jmctl 会解析失败）: %v", err)
	}
	if len(idx.Instances) != 1 || idx.Instances[0].Name != "survival" {
		t.Errorf("内容不符: %+v", idx.Instances)
	}
}

// 目录不存在时自动创建，不让调用方先 mkdir。
func TestWriteInstanceIndex_CreatesDir(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "nested", "servers")
	m := NewManager(dir)

	if err := m.WriteInstanceIndex(dir); err != nil {
		t.Fatalf("应自动创建目录: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, InstanceIndexFilename)); err != nil {
		t.Errorf("快照未生成: %v", err)
	}
}

// 重复写入不得残留临时文件——原子替换的失败路径最容易漏清理。
func TestWriteInstanceIndex_NoTempResidue(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	m.instances = map[string]*Instance{"u1": {UUID: "u1", Name: "x"}}

	for i := 0; i < 3; i++ {
		if err := m.WriteInstanceIndex(dir); err != nil {
			t.Fatalf("第 %d 次写入失败: %v", i, err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != InstanceIndexFilename {
			t.Errorf("残留文件: %s", e.Name())
		}
	}
}

// serversDir 为空时异步刷新直接返回，不 panic、不起 goroutine。
func TestRefreshInstanceIndex_NoServersDir(t *testing.T) {
	m := NewManager("")
	m.refreshInstanceIndexAsync("test") // 不应 panic
}

// 异步刷新最终会落盘（合并后至少写一次）。
func TestRefreshInstanceIndex_WritesEventually(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	m.instances = map[string]*Instance{"u1": {UUID: "u1", Name: "survival"}}

	// 连续触发多次，验证合并逻辑不会漏掉最后一次。
	for i := 0; i < 10; i++ {
		m.refreshInstanceIndexAsync("test")
	}

	target := filepath.Join(dir, InstanceIndexFilename)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(target); err == nil {
			return // 已落盘
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("异步刷新未在 3 秒内落盘")
}

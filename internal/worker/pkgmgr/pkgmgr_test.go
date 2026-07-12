package pkgmgr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRenderNpmrc .npmrc 生成规则（FR-306）：默认源 registry= + @scope 域源 + 带 token 的鉴权行。
func TestRenderNpmrc(t *testing.T) {
	regs := []Registry{
		{URL: "https://registry.npmmirror.com"},
		{Scope: "myco", URL: "https://npm.myco.com", Token: "sekret"},
		{Scope: "other", URL: "https://npm.other.com"},
	}
	out := renderNpmrc(regs)
	want := []string{
		"registry=https://registry.npmmirror.com",
		"@myco:registry=https://npm.myco.com",
		"//npm.myco.com/:_authToken=sekret",
		"@other:registry=https://npm.other.com",
	}
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Fatalf(".npmrc 缺少行 %q，实际:\n%s", w, out)
		}
	}
	// 无 token 的 other 源不应产出 _authToken 行
	if strings.Contains(out, "npm.other.com/:_authToken") {
		t.Fatal("无 token 的源不应写 _authToken")
	}
}

// TestParseNpmrc 反解托管 .npmrc（GetPMConfig 回读，token 清空不回传）。
func TestParseNpmrc(t *testing.T) {
	content := "registry=https://registry.npmmirror.com\n" +
		"@myco:registry=https://npm.myco.com\n" +
		"//npm.myco.com/:_authToken=sekret\n"
	regs := parseNpmrc(content)
	if len(regs) != 2 {
		t.Fatalf("应解析 2 条 registry，got %d: %+v", len(regs), regs)
	}
	var def, scoped *Registry
	for i := range regs {
		if regs[i].Scope == "" {
			def = &regs[i]
		} else if regs[i].Scope == "myco" {
			scoped = &regs[i]
		}
	}
	if def == nil || def.URL != "https://registry.npmmirror.com" {
		t.Fatalf("默认源解析错: %+v", def)
	}
	if scoped == nil || scoped.URL != "https://npm.myco.com" {
		t.Fatalf("@myco 源解析错: %+v", scoped)
	}
	if scoped.Token != "" {
		t.Fatal("回读不应带出 token（凭据不回传）")
	}
}

// TestWriteNpmrcAtomic 写托管 .npmrc 到 configDir，原子可覆盖。
func TestWriteNpmrcAtomic(t *testing.T) {
	dir := t.TempDir()
	m := &Manager{configDir: dir}
	if err := m.writeNpmrc([]Registry{{URL: "https://a.example"}}); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, ".npmrc")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "registry=https://a.example") {
		t.Fatalf(".npmrc 内容错: %s", b)
	}
	// 覆盖写
	if err := m.writeNpmrc([]Registry{{URL: "https://b.example"}}); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(p)
	if strings.Contains(string(b), "a.example") || !strings.Contains(string(b), "b.example") {
		t.Fatalf("覆盖写失败: %s", b)
	}
}

// TestValidatePM PM 枚举校验（FR-306）。
func TestValidatePM(t *testing.T) {
	for _, ok := range []string{"npm", "pnpm", "yarn"} {
		if err := validatePM(ok); err != nil {
			t.Fatalf("%s 应合法: %v", ok, err)
		}
	}
	for _, bad := range []string{"bun", "", "NPM "} {
		if err := validatePM(bad); err == nil {
			t.Fatalf("%q 应非法", bad)
		}
	}
}

// TestValidateRegistry registry url/scope 校验。
func TestValidateRegistry(t *testing.T) {
	if err := validateRegistry(Registry{URL: "https://ok.example"}); err != nil {
		t.Fatalf("合法源报错: %v", err)
	}
	if err := validateRegistry(Registry{URL: "ftp://bad"}); err == nil {
		t.Fatal("非 http(s) 应拒")
	}
	if err := validateRegistry(Registry{URL: "https://ok", Scope: "@bad"}); err == nil {
		t.Fatal("scope 不应带 @ 前缀（前端约定裸 scope）")
	}
}

// TestFindNodeBin 定位托管 node（取最高 major 的 bin/node），兼容嵌套布局。
func TestFindNodeBin(t *testing.T) {
	root := t.TempDir()
	// nodejs-20 与 nodejs-22 两个托管目录，各含 <top>/bin/node
	for _, layout := range []string{
		filepath.Join("nodejs-20", "node-v20.0.0-linux-x64", "bin"),
		filepath.Join("nodejs-22", "node-v22.17.0-linux-x64", "bin"),
	} {
		d := filepath.Join(root, layout)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "node"), []byte("#stub"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	m := &Manager{runtimesRoot: root}
	bin := m.findNodeBin()
	if !strings.Contains(bin, "nodejs-22") {
		t.Fatalf("应选最高 major(nodejs-22) 的 node，got %s", bin)
	}
}

package dataroot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolvePriority(t *testing.T) {
	t.Setenv(EnvVar, filepath.Join(t.TempDir(), "from-env"))
	override := filepath.Join(t.TempDir(), "from-override")

	cases := []struct {
		name     string
		override string
		wantSuf  string
	}{
		{"override wins over env", override, "from-override"},
		{"env when no override", "", "from-env"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := Resolve(c.override)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if !filepath.IsAbs(r.Base()) {
				t.Fatalf("base not absolute: %q", r.Base())
			}
			if filepath.Base(r.Base()) != c.wantSuf {
				t.Fatalf("base = %q, want suffix %q", r.Base(), c.wantSuf)
			}
		})
	}
}

func TestResolveDefault(t *testing.T) {
	t.Setenv(EnvVar, "")
	r, err := Resolve("")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if filepath.Base(r.Base()) != DefaultDir {
		t.Fatalf("default base = %q, want suffix %q", r.Base(), DefaultDir)
	}
}

func TestEnsureLayoutCreatesFHSDirs(t *testing.T) {
	base := filepath.Join(t.TempDir(), "data")
	r, err := Init(base)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	wantDirs := []string{
		r.BinDir(),
		r.EtcDir(),
		r.JDKsDir(),
		r.ServersDir(),
		r.LogDir(),
		r.ArtifactsDir(),
		r.IndexDir(),
		r.CacheDir(),
	}
	for _, d := range wantDirs {
		info, err := os.Stat(d)
		if err != nil {
			t.Fatalf("expected dir %q to exist: %v", d, err)
		}
		if !info.IsDir() {
			t.Fatalf("%q is not a directory", d)
		}
	}

	// 幂等：重复 Init 不报错
	if _, err := Init(base); err != nil {
		t.Fatalf("second Init should be idempotent: %v", err)
	}
}

func TestLayoutMatchesADR010(t *testing.T) {
	base := filepath.Join(t.TempDir(), "data")
	r, err := Init(base)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	cases := map[string]string{
		r.JDKsDir():      filepath.Join("opt", "jdks"),
		r.ServersDir():   filepath.Join("var", "servers"),
		r.LogDir():       filepath.Join("var", "log"),
		r.ArtifactsDir(): filepath.Join("var", "artifacts"),
		r.IndexDir():     filepath.Join("var", "index"),
		r.CacheDir():     "cache",
	}
	for got, wantRel := range cases {
		want := filepath.Join(base, wantRel)
		if got != want {
			t.Errorf("layout path = %q, want %q", got, want)
		}
	}
}

func TestAbsAndRelRoundTrip(t *testing.T) {
	base := filepath.Join(t.TempDir(), "data")
	r, err := Resolve(base)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	rel := "var/servers/my-server-a1b2c3d4"
	abs := r.Abs(rel)
	if !filepath.IsAbs(abs) {
		t.Fatalf("Abs(%q) not absolute: %q", rel, abs)
	}
	if got := r.Rel(abs); got != rel {
		t.Fatalf("Rel(Abs(%q)) = %q, want %q", rel, got, rel)
	}
}

func TestAbsKeepsAbsoluteInput(t *testing.T) {
	r, err := Resolve(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// 用一个平台相关的绝对路径（Windows 带盘符，*nix 以 / 开头）。
	abs, err := filepath.Abs(filepath.Join(t.TempDir(), "external", "srv"))
	if err != nil {
		t.Fatalf("filepath.Abs: %v", err)
	}
	if got := r.Abs(abs); got != filepath.Clean(abs) {
		t.Fatalf("Abs on absolute input = %q, want %q", got, filepath.Clean(abs))
	}
}

func TestRelOutsideRootReturnedAsIs(t *testing.T) {
	r, err := Resolve(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "elsewhere", "x")
	got := r.Rel(outside)
	// 数据根之外的绝对路径无法相对化时原样返回（含 .. 也视为外部）。
	if !strings.Contains(got, "elsewhere") {
		t.Fatalf("Rel outside root = %q, expected original absolute path", got)
	}
}

// TestPortability 模拟「数据根整体拷贝到另一机器」：相对登记路径在新根下解析仍自洽。
func TestPortability(t *testing.T) {
	rel := "var/servers/srv-deadbeef"

	machineA, err := Resolve(filepath.Join(t.TempDir(), "machineA", "data"))
	if err != nil {
		t.Fatalf("Resolve A: %v", err)
	}
	machineB, err := Resolve(filepath.Join(t.TempDir(), "machineB", "data"))
	if err != nil {
		t.Fatalf("Resolve B: %v", err)
	}

	absA := machineA.Abs(rel)
	absB := machineB.Abs(rel)
	if absA == absB {
		t.Fatalf("absolute paths should differ per machine, both = %q", absA)
	}
	if machineA.Rel(absA) != rel || machineB.Rel(absB) != rel {
		t.Fatalf("relative登记应在两机一致: A=%q B=%q want=%q", machineA.Rel(absA), machineB.Rel(absB), rel)
	}
}

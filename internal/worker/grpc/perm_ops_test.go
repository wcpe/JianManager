package grpc

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFormatFileMode(t *testing.T) {
	oct, str := formatFileMode(0o644)
	if oct != "0644" {
		t.Fatalf("octal=%q want 0644", oct)
	}
	if str != "rw-r--r--" {
		t.Fatalf("str=%q want rw-r--r--", str)
	}
	oct, str = formatFileMode(0o755)
	if oct != "0755" || str != "rwxr-xr-x" {
		t.Fatalf("755: oct=%q str=%q", oct, str)
	}
}

func TestFormatPermErrorPermission(t *testing.T) {
	err := &os.PathError{Op: "open", Path: "/x", Err: os.ErrPermission}
	msg := formatPermError("读取目录", err)
	if msg == "" || msg == err.Error() {
		t.Fatalf("应中文化，got %q", msg)
	}
	if !containsAll(msg, "没有权限") {
		t.Fatalf("文案缺关键字: %q", msg)
	}
}

func TestProbeAndChmodRoundTrip(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(file, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	a := probePathAccess(file)
	if !a.Exists || a.IsDir {
		t.Fatalf("exists/isDir: %+v", a)
	}
	if !a.Readable {
		t.Fatalf("应可读: %+v", a)
	}
	if !a.Writable {
		t.Fatalf("应可写: %+v", a)
	}
	if a.ModeOctal == "" && runtime.GOOS != "windows" {
		t.Fatalf("Unix 应有 modeOctal")
	}

	// 去掉 owner 写：0o444
	if err := os.Chmod(file, 0o444); err != nil {
		t.Fatal(err)
	}
	a = probePathAccess(file)
	if a.Writable {
		// 某些环境（root 跑测）可能仍可写，仅告警不硬失败
		t.Logf("chmod 0444 后仍报可写（可能是 root 跑测）: %+v", a)
	}

	oct, err := applyChmod(file, "")
	if err != nil {
		t.Fatalf("applyChmod: %v", err)
	}
	if oct == "" {
		t.Fatal("改后应有 modeOctal")
	}
	a = probePathAccess(file)
	if !a.Writable && runtime.GOOS != "windows" {
		// 非 root 下应恢复可写
		st, _ := os.Stat(file)
		t.Fatalf("修复后应可写 mode=%v access=%+v", st.Mode(), a)
	}

	// 显式 mode
	if _, err := applyChmod(file, "0644"); err != nil {
		t.Fatalf("chmod 0644: %v", err)
	}
}

func TestApplyChmodInvalidMode(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "b.txt")
	_ = os.WriteFile(f, []byte("x"), 0o600)
	if _, err := applyChmod(f, "not-octal"); err == nil {
		t.Fatal("非法 mode 应失败")
	}
}

func TestProbeMissing(t *testing.T) {
	a := probePathAccess(filepath.Join(t.TempDir(), "no-such"))
	if a.Exists {
		t.Fatal("不应存在")
	}
	if a.Reason == "" {
		t.Fatal("应有 reason")
	}
}

func TestDirWritableProbe(t *testing.T) {
	dir := t.TempDir()
	a := probePathAccess(dir)
	if !a.IsDir || !a.Readable || !a.Writable {
		t.Fatalf("临时目录应可读写: %+v", a)
	}
}

func containsAll(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		(func() bool {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		})())
}

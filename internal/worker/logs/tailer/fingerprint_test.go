package tailer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFingerprintStableOnlyPastThreshold 锤死指纹的**稳定性边界**。
//
// # 为什么这条比“能算指纹”更重要
//
// 指纹的实现很容易写成「对全部内容哈希」——那在单测里看起来完全正常，
// 但会让生产出现「每次重启都从头重读」：因为文件一直在增长，内容一变指纹就变，
// registry 永远命中不到。
//
// 本用例把边界明确地写成可验证的事实：
//
//	≥ 1024 字节：追加内容 ⇒ 指纹不变（稳定，能命中 registry）
//	< 1024 字节：追加内容 ⇒ 指纹会变（已知局限，代价是重读小文件）
//
// 变异验证：把 fingerprintOf 改成对全部内容哈希，第一条断言立即转红。
func TestFingerprintStableOnlyPastThreshold(t *testing.T) {
	dir := t.TempDir()

	t.Run("超过阈值后追加不改变指纹", func(t *testing.T) {
		p := filepath.Join(dir, "big.log")
		// 写 2000 字节：超过 FingerprintLength(1024)
		if err := os.WriteFile(p, []byte(strings.Repeat("A", 2000)), 0o644); err != nil {
			t.Fatal(err)
		}
		before, err := IdentifyFile(p)
		if err != nil {
			t.Fatal(err)
		}
		appendTo(t, p, "more content appended\n")
		after, err := IdentifyFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if before.Fingerprint != after.Fingerprint {
			t.Fatalf("超过 %d 字节后追加内容，指纹必须不变（否则重启后会重读），before=%s after=%s",
				FingerprintLength, before.Fingerprint[:12], after.Fingerprint[:12])
		}
	})

	t.Run("不足阈值时追加会改变指纹（已知局限）", func(t *testing.T) {
		p := filepath.Join(dir, "small.log")
		if err := os.WriteFile(p, []byte("a\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		before, _ := IdentifyFile(p)
		appendTo(t, p, "b\n")
		after, _ := IdentifyFile(p)
		if before.Fingerprint == after.Fingerprint {
			t.Log("小文件追加后指纹仍相同（实现可能变了，请重审 growing 逻辑）")
		}
		// 不断言必须不同：这是“已知局限”，不是需求。
		// 但必须保证**能算出指纹**，不能空字符串。
		if after.Fingerprint == "" {
			t.Fatal("小文件也必须能算出指纹，否则会退到 inode 身份")
		}
	})
}

// TestFingerprintUsesOnlyPrefix 确认指纹只取前 FingerprintLength 字节。
//
// 变异验证：把读取长度改成不限（读全文），超长文件的指纹会随尾部内容变。
func TestFingerprintUsesOnlyPrefix(t *testing.T) {
	dir := t.TempDir()
	prefix := strings.Repeat("P", FingerprintLength)
	a := filepath.Join(dir, "a.log")
	b := filepath.Join(dir, "b.log")
	// 两个文件前 1024 字节完全相同，尾部不同
	if err := os.WriteFile(a, []byte(prefix+"tail-A"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte(prefix+"tail-B-different"), 0o644); err != nil {
		t.Fatal(err)
	}
	ia, _ := IdentifyFile(a)
	ib, _ := IdentifyFile(b)
	if ia.Fingerprint != ib.Fingerprint {
		t.Fatalf("前 %d 字节相同时指纹应当相同（指纹只看前缀），实际不同：a=%s b=%s",
			FingerprintLength, ia.Fingerprint[:12], ib.Fingerprint[:12])
	}
}

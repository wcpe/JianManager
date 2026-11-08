package rollog

import (
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// seedRotated 造一个轮转档（内容可为空），并可选造一个压缩中间文件。
func seedRotated(t *testing.T, dir, base, ts string, withTmp, withGz bool) (logPath, tmpPath string) {
	t.Helper()
	logPath = filepath.Join(dir, base+"-"+ts+".log")
	tmpPath = logPath + ".gz.tmp"

	if err := os.WriteFile(logPath, []byte(strings.Repeat("line\n", 20)), 0o644); err != nil {
		t.Fatalf("造 .log 失败: %v", err)
	}
	if withTmp {
		// 只有 gzip 头的半截文件：模拟压缩中途崩溃。
		if err := os.WriteFile(tmpPath, []byte{0x1f, 0x8b, 0x08, 0x00}, 0o644); err != nil {
			t.Fatalf("造 .tmp 失败: %v", err)
		}
	}
	if withGz {
		if err := os.WriteFile(logPath+".gz", []byte("gz-placeholder"), 0o644); err != nil {
			t.Fatalf("造 .gz 失败: %v", err)
		}
	}
	return logPath, tmpPath
}

// TestOrphanTmpSweptWhenSourceGone 是 2026-11-08 真机发现的“孤儿 .tmp 永久残留”缺陷的回归。
//
// # 缺陷形态
//
// 压缩中途崩溃只会留下 `.log` + `.log.gz.tmp`（无 `.gz`）。下次启动时：
//   - 若 `.log` 还在 → 补压任务重做，`.tmp` 被 O_TRUNC 覆盖后被 rename 发布，自然消化；
//   - 若 `.log` 已不在（超期被保留策略删掉、或人工清理）→ **这个 .tmp 永远没人管**。
//
// 后者不是罕见组合：维护顺序是“先清理后压缩”，所以“超期的 .log 被删 + 它的 .tmp 残留”
// 恰好是最容易被造出来的状态。而 `.gz.tmp` 既不在 listRotated 的 glob（只看 .log/.gz）里，
// 也不受任何保留规则约束——会永久堆在日志目录里。
//
// 真机现场实测：预置该组合后启动，目录里就真的只留一个 4 字节的 `.log.gz.tmp`。
func TestOrphanTmpSweptWhenSourceGone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	ts := time.Now().Add(-2 * time.Hour).Format(rotatedTimeLayout)
	logPath, tmpPath := seedRotated(t, dir, "app", ts, true, false)

	// 把 .log 删掉，只留 .tmp —— 模拟“保留策略先删了源文件”。
	if err := os.Remove(logPath); err != nil {
		t.Fatalf("删除 .log 失败: %v", err)
	}
	if _, err := os.Stat(tmpPath); err != nil {
		t.Fatalf("前置条件不成立，.tmp 应存在: %v", err)
	}

	w, err := Open(Config{Path: path, MaxBackups: 10, MaxAgeDays: 30})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer w.Close()

	waitFor(t, "孤儿 .tmp 被清扫", func() bool {
		_, statErr := os.Stat(tmpPath)
		return os.IsNotExist(statErr)
	})
}

// TestOrphanTmpKeptWhenSourceAlive 验证：原 `.log` 还在时，压缩仍能正常完成。
//
// # 这个用例到底能锁住什么（说清楚，不自欺）
//
// 它锁的是**端到端正确性**：补压后 `.gz` 解出的内容必须与原 `.log` 逐字节一致。
// 它**不能**锁住“sweep 是否对源文件存在做过检查”——因为 `compressOne` 会以
// `O_CREATE|O_TRUNC` 重新创建 `.tmp`，即使 sweep 多此一举先删了它，压缩结果仍然正确。
//
// 换句话说，“sweep 必须先检查源文件”这个约束**没有可观测的后果**，
// 它只是避免一次多余 unlink。我不会为它编造一个转红的断言——
// 那种断言守的东西是假的（参见 CONVENTIONS「注释里的前提要能被验证」）。
// 真要有价值，靠的是下面这条内容一致性断言。
func TestOrphanTmpKeptWhenSourceAlive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	ts := time.Now().Add(-2 * time.Hour).Format(rotatedTimeLayout)
	logPath, _ := seedRotated(t, dir, "app", ts, true, false)

	want, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("读取原档失败: %v", err)
	}

	w, err := Open(Config{Path: path, MaxBackups: 10, MaxAgeDays: 30})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer w.Close()

	// 等补压完成：应产出 .gz，且原 .log 被消化。
	waitFor(t, "补压产物 .gz 出现", func() bool {
		_, statErr := os.Stat(logPath + ".gz")
		return statErr == nil
	})
	waitFor(t, "压缩完成后 .log 被消化", func() bool {
		_, statErr := os.Stat(logPath)
		return os.IsNotExist(statErr)
	})

	// 关键断言：.gz 解出的内容必须与原 .log 逐字节一致。
	// 若 sweep 误删了 .tmp，压缩要么失败、要么产出空/残缺档，这里就会转红。
	got, err := gunzipFile(logPath + ".gz")
	if err != nil {
		t.Fatalf("解压 .gz 失败（可能 .tmp 被误删导致产物不完整）: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("压缩产物内容不匹配（.tmp 可能被误删）:\n got=%q\nwant=%q", got, want)
	}
}

// gunzipFile 解开一个 .gz 并返回全部字节。
func gunzipFile(p string) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return io.ReadAll(zr)
}

// TestOrphanTmpSweptWhenOnlyGzRemains 覆盖“压缩已完成但 .tmp 反而残留”的形态。
//
// 成因：rename 发布 .gz 成功后进程立刻崩溃，某些文件系统/异常路径下 .tmp 未被清。
// 这种情况 .gz 是完整的，`.tmp` 是纯垃圾，启动时应被扫掉。
func TestOrphanTmpSweptWhenOnlyGzRemains(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	ts := time.Now().Add(-3 * time.Hour).Format(rotatedTimeLayout)
	logPath, tmpPath := seedRotated(t, dir, "app", ts, true, true)

	// 只留 .gz 与 .tmp，删掉 .log。
	if err := os.Remove(logPath); err != nil {
		t.Fatalf("删除 .log 失败: %v", err)
	}

	w, err := Open(Config{Path: path, MaxBackups: 10, MaxAgeDays: 30})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer w.Close()

	waitFor(t, "只剩 .gz 时 .tmp 被清扫", func() bool {
		_, statErr := os.Stat(tmpPath)
		return os.IsNotExist(statErr)
	})
	// .gz 本身必须保留（它是唯一完成品）。
	if _, err := os.Stat(logPath + ".gz"); err != nil {
		t.Fatalf(".gz 不应被误删: %v", err)
	}
}

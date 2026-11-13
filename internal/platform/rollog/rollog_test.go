package rollog

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// waitFor 轮询等待条件成立，避免用固定 sleep（CI 上不稳定）。
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("超时未等到：%s", what)
}

// TestWriteAndAppend 验证基本写入与重启后续写（不截断已有内容）。
func TestWriteAndAppend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	w, err := Open(Config{Path: path, DisableCompress: true})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	if _, err := w.Write([]byte("first\n")); err != nil {
		t.Fatalf("Write 失败: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}

	// 重启：不能截断旧内容，必须从当前大小继续追加。
	w2, err := Open(Config{Path: path, DisableCompress: true})
	if err != nil {
		t.Fatalf("重新 Open 失败: %v", err)
	}
	if got := w2.Size(); got != int64(len("first\n")) {
		t.Fatalf("重启后 Size 应等于旧文件大小 %d，实际 %d", len("first\n"), got)
	}
	if _, err := w2.Write([]byte("second\n")); err != nil {
		t.Fatalf("第二次 Write 失败: %v", err)
	}
	_ = w2.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取日志失败: %v", err)
	}
	if string(data) != "first\nsecond\n" {
		t.Fatalf("内容应为两行拼接，实际 %q", string(data))
	}
}

// TestRotateBySize 验证达阈值后自动轮转，且轮转是 rename（原文件内容完整进入轮转档）。
func TestRotateBySize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	w, err := Open(Config{Path: path, MaxSizeBytes: 16, MaxBackups: 10, DisableCompress: true})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer w.Close()

	// 写三次，每次会跨过 16 字节阈值。
	for i := 0; i < 3; i++ {
		if _, err := w.Write([]byte("0123456789\n")); err != nil { // 11 bytes
			t.Fatalf("Write %d 失败: %v", i, err)
		}
	}

	rotated, err := filepath.Glob(filepath.Join(dir, "app-*.log"))
	if err != nil {
		t.Fatalf("Glob 失败: %v", err)
	}
	if len(rotated) == 0 {
		t.Fatal("应该产生至少一个轮转档")
	}

	// 轮转档内容必须是完整的两行（rename 语义：不丢字节）。
	data, err := os.ReadFile(rotated[0])
	if err != nil {
		t.Fatalf("读取轮转档失败: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("轮转档不应为空")
	}
	if !strings.Contains(string(data), "0123456789") {
		t.Fatalf("轮转档内容异常: %q", string(data))
	}
}

// TestRotateIsRenameNotCopy 是核心回归：轮转不得复制字节。
//
// 验证方式：轮转后原活跃文件的 inode 必然变化（rename 后新建了文件），
// 而轮转档保留原 inode。若实现退化成 copytruncate（读+写+截断），
// 活跃文件 inode 不变且会有一个瞬间的 “两文件同 inode” 状态。
func TestRotateIsRenameNotCopy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	w, err := Open(Config{Path: path, MaxSizeBytes: 8, MaxBackups: 10, DisableCompress: true})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer w.Close()

	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat 失败: %v", err)
	}

	if _, err := w.Write([]byte("0123456789\n")); err != nil {
		t.Fatalf("Write 失败: %v", err)
	}

	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("轮转后 Stat 失败: %v", err)
	}
	if os.SameFile(before, after) {
		t.Fatal("轮转后活跃文件应是一个新文件（rename + 新建），但 inode 未变 ⇒ 实现更像 copytruncate")
	}
}

// TestCompressAsync 验证轮转后异步 gzip，且 .gz 能正常解压还原。
func TestCompressAsync(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	w, err := Open(Config{Path: path, MaxSizeBytes: 16, MaxBackups: 10})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer w.Close()

	const payload = "compressed-content-0123456789\n"
	if _, err := w.Write([]byte(payload)); err != nil {
		t.Fatalf("Write 失败: %v", err)
	}

	var gzPath string
	waitFor(t, ".gz 出现", func() bool {
		m, _ := filepath.Glob(filepath.Join(dir, "app-*.log.gz"))
		if len(m) > 0 {
			gzPath = m[0]
			return true
		}
		return false
	})

	f, err := os.Open(gzPath)
	if err != nil {
		t.Fatalf("打开 .gz 失败: %v", err)
	}
	defer f.Close()
	r, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip 解压器创建失败: %v", err)
	}
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("读取 gzip 内容失败: %v", err)
	}
	if string(got) != payload {
		t.Fatalf("gzip 内容不匹配:\n got=%q\nwant=%q", string(got), payload)
	}
}

// TestRetention 验证超过 MaxBackups 后旧档被删除。
func TestRetention(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	w, err := Open(Config{Path: path, MaxSizeBytes: 4, MaxBackups: 2})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer w.Close()

	for i := 0; i < 6; i++ {
		if _, err := w.Write([]byte(fmt.Sprintf("line-%d\n", i))); err != nil {
			t.Fatalf("Write %d 失败: %v", i, err)
		}
		// 同秒内多次轮转靠序号区分；稍作间隔确保时间戳不同。
		time.Sleep(2 * time.Millisecond)
	}

	waitFor(t, "轮转档收敛到 MaxBackups 以内", func() bool {
		plain, _ := filepath.Glob(filepath.Join(dir, "app-*.log"))
		gz, _ := filepath.Glob(filepath.Join(dir, "app-*.log.gz"))
		return len(plain)+len(gz) <= 2
	})
}

// TestResumePendingCompression 验证：进程在 rename 后、压缩前崩溃留下的 .log，
// 下次 Open 时会被补压（崩溃可恢复）。
func TestResumePendingCompression(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	// 手工造一个“上次遗留”的未压缩轮转档。
	//
	// 时间戳必须落在保留窗口内（默认 30 天）：用一个十个月前的固定日期，
	// 它会被 MaxAgeDays 当成过期档删掉，测的就不是补压而是清理了。
	leftoverTS := time.Now().Add(-2 * time.Hour).Format(rotatedTimeLayout)
	leftover := filepath.Join(dir, "app-"+leftoverTS+".log")
	if err := os.WriteFile(leftover, []byte("leftover-content\n"), 0o644); err != nil {
		t.Fatalf("写入遗留档失败: %v", err)
	}

	w, err := Open(Config{Path: path, MaxSizeBytes: 1024, MaxBackups: 10})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer w.Close()

	waitFor(t, "遗留档被补压", func() bool {
		_, statErr := os.Stat(leftover + ".gz")
		return statErr == nil
	})

	// 补压后原 .log 应被删除，避免重复占用。
	waitFor(t, "补压后原 .log 被删除", func() bool {
		_, statErr := os.Stat(leftover)
		return os.IsNotExist(statErr)
	})
}

// TestConcurrentWrite 验证并发写入安全，且不丢字节（-race 下跑）。
func TestConcurrentWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	// 阈值调大，确保本次测试不触发轮转（只验证写并发安全）。
	w, err := Open(Config{Path: path, MaxSizeBytes: 1 << 30, DisableCompress: true})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}

	const (
		goroutines = 16
		perRoutine = 200
	)
	var wg sync.WaitGroup
	line := []byte("0123456789abcdef\n") // 17 bytes
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perRoutine; j++ {
				if _, err := w.Write(line); err != nil {
					t.Errorf("并发 Write 失败: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if err := w.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取日志失败: %v", err)
	}
	want := goroutines * perRoutine * len(line)
	if len(data) != want {
		t.Fatalf("并发写入丢字节：期望 %d，实际 %d", want, len(data))
	}
	if got := bytes.Count(data, []byte("\n")); got != goroutines*perRoutine {
		t.Fatalf("行数不匹配：期望 %d，实际 %d", goroutines*perRoutine, got)
	}
}

// TestRotateEmptyFileNoop 验证空文件不轮转（避免零字节归档）。
func TestRotateEmptyFileNoop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	w, err := Open(Config{Path: path, DisableCompress: true})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer w.Close()

	if err := w.Rotate(); err != nil {
		t.Fatalf("空文件 Rotate 不应报错: %v", err)
	}
	rotated, _ := filepath.Glob(filepath.Join(dir, "app-*.log"))
	if len(rotated) != 0 {
		t.Fatalf("空文件不应产生轮转档，实际 %v", rotated)
	}
}

// TestPath 验证 Path() 返回配置路径（诊断接口依赖它）。
func TestPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	w, err := Open(Config{Path: path, DisableCompress: true})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer w.Close()
	if w.Path() != path {
		t.Fatalf("Path() 应为 %q，实际 %q", path, w.Path())
	}
}

// TestOpenCreatesParentDir 验证父目录不存在时自动创建。
func TestOpenCreatesParentDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b", "c", "app.log")
	w, err := Open(Config{Path: path, DisableCompress: true})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer w.Close()
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Fatalf("父目录应被创建: %v", err)
	}
}

// TestOnErrorCallback 验证轮转错误经 OnError 上报而不是吞掉。
func TestOnErrorCallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	errs := make(chan error, 4)
	w, err := Open(Config{
		Path:            path,
		MaxSizeBytes:    1 << 30,
		DisableCompress: true,
		OnError:         func(e error) { errs <- e },
	})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer w.Close()

	// 写正常内容不应产生 OnError 回调。
	if _, err := w.Write([]byte("ok\n")); err != nil {
		t.Fatalf("Write 失败: %v", err)
	}
	select {
	case e := <-errs:
		t.Fatalf("正常写入不应回调 OnError，实际: %v", e)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestRetentionByAge 验证 MaxAgeDays：超过保留天数的轮转档被删除，未超的保留。
//
// 注意造档方式：把文件名里的时间戳设成过去，而 **mtime 故意设成现在**。
// 这是本用例的关键——若实现用 mtime 而非文件名，两个档会因 mtime 新而都活下来，
// 用例即转红（已做变异验证）。
func TestRetentionByAge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	oldName := filepath.Join(dir, "app-"+time.Now().AddDate(0, 0, -40).Format(rotatedTimeLayout)+".log")
	freshName := filepath.Join(dir, "app-"+time.Now().AddDate(0, 0, -1).Format(rotatedTimeLayout)+".log")
	for _, p := range []string{oldName, freshName} {
		if err := os.WriteFile(p, []byte("content\n"), 0o644); err != nil {
			t.Fatalf("造档失败: %v", err)
		}
		// mtime 统一设为现在：证明判据不依赖它。
		now := time.Now()
		if err := os.Chtimes(p, now, now); err != nil {
			t.Fatalf("改 mtime 失败: %v", err)
		}
	}

	// MaxBackups 放开，只留 MaxAgeDays 一条规则。
	// 构造一次轮转即触发一次维护（保留清理）。
	w, err := Open(Config{Path: path, MaxSizeBytes: 8, MaxBackups: -1, MaxAgeDays: 30, DisableCompress: true})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer w.Close()

	if _, err := w.Write([]byte("0123456789\n")); err != nil {
		t.Fatalf("Write 失败: %v", err)
	}

	waitFor(t, "超过 30 天的档被删除", func() bool {
		_, statErr := os.Stat(oldName)
		return os.IsNotExist(statErr)
	})

	if _, err := os.Stat(freshName); err != nil {
		t.Fatalf("未超期的档（1 天前）不应被删: %v", err)
	}
}

// TestRetentionByAgeDisabled 验证 MaxAgeDays 为负时关闭按天保留。
func TestRetentionByAgeDisabled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	// 一年前的档：保留天数为负时必须留下。
	veryOld := filepath.Join(dir, "app-"+time.Now().AddDate(-1, 0, 0).Format(rotatedTimeLayout)+".log")
	if err := os.WriteFile(veryOld, []byte("ancient\n"), 0o644); err != nil {
		t.Fatalf("造档失败: %v", err)
	}

	w, err := Open(Config{Path: path, MaxSizeBytes: 8, MaxBackups: -1, MaxAgeDays: -1, DisableCompress: true})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer w.Close()

	if _, err := w.Write([]byte("0123456789\n")); err != nil {
		t.Fatalf("Write 失败: %v", err)
	}

	// 等维护轮跑完（轮转档会出现），再断言旧档仍在。
	waitFor(t, "本次轮转档产生", func() bool {
		n := 0
		for _, f := range mustGlob(t, filepath.Join(dir, "app-*.log")) {
			if f != veryOld {
				n++
			}
		}
		return n > 0
	})
	// 给维护协程一点时间做（错误的）清理；如果它要错删，这里才看得到。
	time.Sleep(50 * time.Millisecond)

	if _, err := os.Stat(veryOld); err != nil {
		t.Fatalf("MaxAgeDays<0 表示关闭按天保留，旧档不应被删: %v", err)
	}
}

// TestRetentionWithoutCompress 是“DisableCompress 时 MaxBackups 失效”的回归。
//
// # 缺陷形态
//
// 旧实现把保留清理放在压缩协程里顺带做。而 DisableCompress 时压缩协程根本不启动，
// 于是**MaxBackups 完全不生效**——保留 N 份的承诺静默作废，磁盘无界增长。
// 而默认配置（压缩开启）下一切正常，所以原有用例测不出来。
func TestRetentionWithoutCompress(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	w, err := Open(Config{Path: path, MaxSizeBytes: 4, MaxBackups: 2, DisableCompress: true})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer w.Close()

	for i := 0; i < 6; i++ {
		if _, err := w.Write([]byte(fmt.Sprintf("line-%d\n", i))); err != nil {
			t.Fatalf("Write %d 失败: %v", i, err)
		}
		time.Sleep(2 * time.Millisecond)
	}

	waitFor(t, "关压缩时轮转档仍收敛到 MaxBackups 以内", func() bool {
		return len(mustGlob(t, filepath.Join(dir, "app-*.log"))) <= 2
	})
}

// TestRetentionKeepsNewestWhenSameSecond 是“同秒序号档排序反转”的回归。
//
// # 缺陷形态
//
// 同秒多次轮转会生成 `app-<ts>.log` 与 `app-<ts>-1.log`。旧实现按字符串排序，
// 而 `-`(0x2D) < `.`(0x2E) ⇒ 无序号的那个被当成更早的，保留策略**删错文件**：
// 留下更旧的、删掉更新的。
//
// 本用例直接手造成同秒两档，断言留下的是序号更大的（更新的）那份。
func TestRetentionKeepsNewestWhenSameSecond(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	ts := time.Now().Add(-time.Hour).Format(rotatedTimeLayout)
	a := filepath.Join(dir, "app-"+ts+".log")   // 同秒第 0 次
	b := filepath.Join(dir, "app-"+ts+"-1.log") // 同秒第 1 次（更新）
	if err := os.WriteFile(a, []byte("older\n"), 0o644); err != nil {
		t.Fatalf("造档失败: %v", err)
	}
	if err := os.WriteFile(b, []byte("newer\n"), 0o644); err != nil {
		t.Fatalf("造档失败: %v", err)
	}

	// MaxBackups=1：只能留一个，而且必须是 b（同秒内序号更大的）。
	//
	// 【为何不靠 Write 触发轮转】写入会**再生成第三份**轮转档，且它是最新的；
	// MaxBackups=1 只留最新一份，于是 a 与 b 双双被汰，用例必然超时。
	// 那不是排序缺陷，只是“1 个名额被更新的文件占了”的应有行为。
	// 这里直接调用维护函数，把被测对象限定为**排序 + 汰旧**本身。
	w, err := Open(Config{Path: path, MaxSizeBytes: 8, MaxBackups: 1, DisableCompress: true})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer w.Close()

	if err := w.applyRetention(); err != nil {
		t.Fatalf("applyRetention 失败: %v", err)
	}

	// 关键断言：更新的那个必须留下。
	if _, statErr := os.Stat(b); statErr != nil {
		t.Fatalf("同秒内序号更大的那份（更新）必须留下，实际被删: %v", statErr)
	}
	if _, statErr := os.Stat(a); statErr == nil {
		t.Fatalf("同秒内无序号的那份（更旧）应被清理，实际仍存在")
	}
}

// TestRetentionIgnoresForeignFiles 验证保留策略不删自己认不得的文件。
//
// 理由：日志目录里可能有运维手工拷进来的文件（取证快照、改名的旧日志）。
// 「认不得就不删」比「尽量清干净」安全——后者误删证据的代价不可逆。
func TestRetentionIgnoresForeignFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	// 名字不符合本包命名规则的旧文件（用 Unrelated 前缀，glob 都不会匹配）。
	foreign := filepath.Join(dir, "unrelated-backup.log")
	if err := os.WriteFile(foreign, []byte("evidence\n"), 0o644); err != nil {
		t.Fatalf("造档失败: %v", err)
	}
	// 前缀匹配但时间戳不合法的文件（如人工命名 app-important.log）。
	oddName := filepath.Join(dir, "app-important.log")
	if err := os.WriteFile(oddName, []byte("keep me\n"), 0o644); err != nil {
		t.Fatalf("造档失败: %v", err)
	}

	w, err := Open(Config{Path: path, MaxSizeBytes: 4, MaxBackups: 1, DisableCompress: true})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer w.Close()

	for i := 0; i < 4; i++ {
		if _, err := w.Write([]byte(fmt.Sprintf("line-%d\n", i))); err != nil {
			t.Fatalf("Write %d 失败: %v", i, err)
		}
		time.Sleep(2 * time.Millisecond)
	}

	waitFor(t, "轮转档收敛", func() bool {
		return len(mustGlob(t, filepath.Join(dir, "app-*.log"))) <= 2
	})

	for _, p := range []string{foreign, oddName} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("不应删除认不得的文件 %s: %v", filepath.Base(p), err)
		}
	}
}

// TestMaxAgeDefaultApplied 验证零值 Config 会填上默认保留天数（与 MaxBackups 同口径）。
func TestMaxAgeDefaultApplied(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(Config{Path: filepath.Join(dir, "app.log"), DisableCompress: true})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer w.Close()

	if w.cfg.MaxAgeDays != DefaultMaxAgeDays {
		t.Fatalf("MaxAgeDays 零值应填充为 %d，实际 %d", DefaultMaxAgeDays, w.cfg.MaxAgeDays)
	}
}

// mustGlob 是 filepath.Glob 的测试封装：出错即失败（测试里不该忽略错误）。
func mustGlob(t *testing.T, pattern string) []string {
	t.Helper()
	m, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatalf("Glob %q 失败: %v", pattern, err)
	}
	return m
}

// TestNextRotatedNameAvoidsCompressedSibling 是 2026-11-08 真机并发压测暴露的缺陷的回归。
//
// # 缺陷形态
//
// `nextRotatedName` 原先只检查候选名的 `.log` 是否存在。而轮转档会被异步压缩
// 成 `.log.gz`（`.log` 随之消失），于是同一秒内就出现“名字被再次发放”：
//
//	t0  轮转 → <ts>.log
//	t1  压缩完成 → 只剩 <ts>.log.gz
//	t2  同秒再轮转 → 看起来 <ts>.log 不存在 ⇒ 又发 <ts>.log
//
// 结果 `<ts>.log` 与 `<ts>.log.gz` 并存，内容却是**两批不同数据**：
// 真机现场实测前者的 seq 区间是 114314..112882，后者是 113641..112703，毫无重叠。
// 两者在 parseRotatedName 眼里都是 (when, seq=0)，保留策略无法区分先后。
//
// # 为什么要直接测 nextRotatedName
//
// 想用“真实写入压出同秒多次轮转”非常依赖机器速度（本次就是靠 8 协程才撞出来的），
// 不适合放进 CI。直接造出“同名 .gz 已存在”这一状态，用小代价钉住判据。
func TestNextRotatedNameAvoidsCompressedSibling(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	w, err := Open(Config{Path: path, DisableCompress: true})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer w.Close()

	ts := time.Now().Format(rotatedTimeLayout)

	// 造出三种“名字已被占”的形态，逐个验证 nextRotatedName 会跳过它们。
	cases := []struct {
		name string
		file string
	}{
		{"未压缩 .log 已占", "app-" + ts + ".log"},
		{"已压缩 .gz 已占（真机遇到的形态）", "app-" + ts + ".log.gz"},
		{"压缩中 .gz.tmp 已占", "app-" + ts + ".log.gz.tmp"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(dir, tc.file)
			if err := os.WriteFile(p, []byte("occupied\n"), 0o644); err != nil {
				t.Fatalf("造占位文件失败: %v", err)
			}
			defer os.Remove(p)

			got, err := w.nextRotatedName()
			if err != nil {
				t.Fatalf("nextRotatedName 失败: %v", err)
			}
			if filepath.Base(got) == filepath.Base(p) {
				t.Fatalf("候选名 %q 已被 %q 占用，却仍被发放", filepath.Base(got), tc.file)
			}
		})
	}
}

// TestNextRotatedNameUniqueAcrossSiblingForms 验证：同一秒内 .log 与 .log.gz
// 不会拿到同一个基础名，且序号递增。
func TestNextRotatedNameUniqueAcrossSiblingForms(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(Config{Path: filepath.Join(dir, "app.log"), DisableCompress: true})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer w.Close()

	// 模拟“第一次轮转已完成并压缩”：只有 .gz，没有 .log。
	ts := time.Now().Format(rotatedTimeLayout)
	gz := filepath.Join(dir, "app-"+ts+".log.gz")
	if err := os.WriteFile(gz, []byte("old-batch\n"), 0o644); err != nil {
		t.Fatalf("造档失败: %v", err)
	}

	name, err := w.nextRotatedName()
	if err != nil {
		t.Fatalf("nextRotatedName 失败: %v", err)
	}

	// 必须拿到带序号的名字（因为同秒无序号名已被 .gz 占领）。
	base := filepath.Base(name)
	if base == "app-"+ts+".log" {
		t.Fatalf("同秒基础名已被 .gz 占用，却仍发放了无序号名 %q", base)
	}
	if !strings.HasPrefix(base, "app-"+ts+"-") {
		t.Fatalf("应拿到带序号的名字，实际 %q", base)
	}
}

package rollog

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestReopenAfterExternalDelete 是 2026-10-29 真机验证发现的缺陷的回归。
//
// # 缺陷形态
//
// 进程持有打开的文件描述符。外部把文件删掉后，写操作**不会报错**——
// 只会写进一个已无目录项的 inode（`/proc/<pid>/fd` 里显示 `(deleted)`）。
// 后果：面板与 grep 再也看不到新日志，而所有日志调用一路“成功”。
//
// # 为什么要锁住它
//
// 这不是理论风险：部署后用 `rm control-plane.log` 清一下测试数据就当场踩到。
// 而排障时“日志文件明明存在却一直没有新行”是最容易误判的一类现象
// （会一路去怀疑日志级别、怀疑组件没启动）。
//
// # 断言
//
// 删除后继续写，路径上的文件必须被重建并包含新内容。
func TestReopenAfterExternalDelete(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	w, err := Open(Config{
		Path:            path,
		DisableCompress: true,
		// 把检测窗口压到极小，避免测试等 1s。
		OnError: func(error) {},
	})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer w.Close()
	w.statInterval = 0 // 每次都检测

	if _, err := w.Write([]byte("before-delete\n")); err != nil {
		t.Fatalf("首次写入失败: %v", err)
	}

	// 模拟运维清理 / 误删。
	if err := os.Remove(path); err != nil {
		t.Fatalf("删除日志文件失败: %v", err)
	}

	// 删除后继续写：不得报错，且必须重建文件。
	if _, err := w.Write([]byte("after-delete\n")); err != nil {
		t.Fatalf("删除后写入失败（不应报错）: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("日志文件应被重建，但 Stat 失败: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("重建后的文件不应为空（应已写入 after-delete）")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取重建后的日志失败: %v", err)
	}
	if want := "after-delete\n"; string(data) != want {
		t.Fatalf("重建文件内容应为 %q（删除前的行随旧 inode 丢弃），实际 %q", want, string(data))
	}

	// 继续写入应当正常落到重建后的文件。
	if _, err := w.Write([]byte("third\n")); err != nil {
		t.Fatalf("重建后继续写入失败: %v", err)
	}
	data, _ = os.ReadFile(path)
	if want := "after-delete\nthird\n"; string(data) != want {
		t.Fatalf("重建后追加内容不匹配:\n got=%q\nwant=%q", string(data), want)
	}
}

// TestReopenReportsDiagnostic 验证重建会通过 OnError 报告一次。
//
// 为什么要断言它：重建本身会丢“删除前那一段”日志，若完全静默，
// 运维会以为日志从未中断。报告一次让他能定位到“有人在 19:50 删过文件”。
func TestReopenReportsDiagnostic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	errs := make(chan error, 4)
	w, err := Open(Config{
		Path:            path,
		DisableCompress: true,
		OnError:         func(e error) { errs <- e },
	})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer w.Close()
	w.statInterval = 0

	if _, err := w.Write([]byte("x\n")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if _, err := w.Write([]byte("y\n")); err != nil {
		t.Fatalf("删除后写入失败: %v", err)
	}

	select {
	case <-errs:
		// 期望：报告一次重建诊断
	case <-time.After(time.Second):
		t.Fatal("重建应通过 OnError 报告诊断，实际静默")
	}
}

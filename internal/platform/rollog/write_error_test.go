package rollog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// failReopenByBlockingPath 让 Writer 的下一次「重建活跃文件」必然失败，
// 但**不删除日志目录**。
//
// # 为什么不用「删掉目录」来构造
//
// 早期写法是 os.RemoveAll(dir)。它在 Linux 上没问题，在 Windows 上却既不稳也不对：
//   - Windows 删除仍被打开的文件是延迟删除，目录项要等句柄全关才消失；
//   - 更隐晦的是 Open 会立刻起后台维护协程做 filepath.Glob，Glob 本身也持有目录句柄；
//   - 于是 RemoveAll 与二者竞争同一目录，随机报 ERROR_SHARING_VIOLATION
//     （实测几十次里偶发一次，CI 满载时更容易踩中）。
//
// 为了压掉这个竞态，一度在产品代码里加了 maintMu + 测试专用方法，
// 代价是产品类型上出现了「只有测试会调用」的 API——这是不该有的坏味道。
//
// # 现在的构造
//
// 用例要的其实是「重建必然失败」，不必等于「目录不存在」。把活跃文件路径本身
// 换成一个**同名目录**即可：openActiveLocked 里的 os.OpenFile(path, O_WRONLY, ...)
// 会因 EISDIR 稳定失败。
//
// 好处：
//   - 完全不删目录，因此不与后台 Glob 竞争，无需任何产品侧配合；
//   - 只删单个文件，而 Windows 在申请了 FILE_SHARE_DELETE 后允许在句柄打开时删除
//     （这正是 reopen_test.go 依赖的能力），两平台一致；
//   - EISDIR 对应真实故障：有人误把日志路径建成目录、或挂载点异常。
//
// 副作用可控：reopenIfVanishedLocked 用 os.Stat 判断「目录项还在吗」，
// 而同名目录的 Stat 会成功，因此**不会**误触发自愈重建——那正是本用例要的：
// 让 Write 走到「文件句柄已失效 + 无法重建」的分支。
func failReopenByBlockingPath(t *testing.T, w *Writer) {
	t.Helper()
	// 先让 Writer 交出活跃文件句柄：否则 Windows 上 os.Remove 会遇到延迟删除。
	// 这里只关文件、不置 w.closed——置了之后 Write 会直接返回 ErrClosed，
	// 就走不到用例要验证的「重建失败上报」路径了。
	w.mu.Lock()
	if w.file != nil {
		if err := w.file.Close(); err != nil {
			w.mu.Unlock()
			t.Fatalf("关闭活跃文件: %v", err)
		}
		w.file = nil
		w.size = 0
	}
	w.mu.Unlock()

	// 删文件（不是删目录），再在同一路径建成目录，使后续 OpenFile 必失败。
	if err := os.Remove(w.cfg.Path); err != nil && !os.IsNotExist(err) {
		t.Fatalf("移除活跃文件: %v", err)
	}
	if err := os.Mkdir(w.cfg.Path, 0o755); err != nil {
		t.Fatalf("占位活跃文件路径为目录: %v", err)
	}
}

// TestWriteReportsReopenFailureViaOnError 守护「写不进去必须有人知道」。
//
// 背景：Write 的两个失败分支原本只 `return 0, err`，不经 OnError。
// 而调用方是 slog——标准库 logger.go 里是 `_ = l.Handler().Handle(...)`，
// **显式丢弃** Handler 返回的错误；io.MultiWriter 也只透传第一个错误。
// 于是链路是：日志写不进去 → 文件里没内容 → OnError 没被调 → stderr 也空。
// 运维看到的是“日志停在某行之后没了”，无任何入口可查。
func TestWriteReportsReopenFailureViaOnError(t *testing.T) {
	dir := t.TempDir()
	errs := make(chan error, 8)
	w, err := Open(Config{
		Path: filepath.Join(dir, "app.log"),
		OnError: func(e error) {
			select {
			case errs <- e:
			default:
			}
		},
		MaxBackups:      1,
		MaxAgeDays:      1,
		DisableCompress: true,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer w.Close()

	// 构造「重建必然失败」，但**不删目录**（见 failReopenByBlockingPath 的说明）。
	failReopenByBlockingPath(t, w)
	w.statInterval = 0 // 禁用节流，让本次 Write 必然走到重建

	if _, werr := w.Write([]byte("boom\n")); werr == nil {
		t.Fatal("目录已不存在，写入应当失败")
	}

	// 必须至少有一次 OnError，且能看出是“写入丢丢”。
	var got []string
	for {
		select {
		case e := <-errs:
			got = append(got, e.Error())
			continue
		default:
		}
		break
	}
	if len(got) == 0 {
		t.Fatal("写入失败必须经 OnError 上报；否则 slog 吞掉 error 后完全无痕迹")
	}
	// 必须有一条能明确认出是「重建失败」的 OnError。
	//
	// 断言必须精确到具体文案，不要用 || 容错：
	// 曾经这里写的是 `!含A && !含B`，其中 B（“写入”）能匹配上别处的消息，
	// 于是删掉重建失败分支的上报也不会转红——两个上报点共用一个宽松断言，
	// 等于两个都没守住。现在每个上报点各写一条用例。
	joined := strings.Join(got, " | ")
	if !strings.Contains(joined, "丢失且重建失败") {
		t.Fatalf("重建失败必须经 OnError 上报（带明确文案），实际: %s", joined)
	}
}

// TestWriteRecoversWithoutWaitingStatInterval 守护「file==nil 时不等 1s 窗口」。
//
// statInterval 的用意是把 os.Stat 降到 1s 一次，避免高频日志把 stat 变成热点。
// 但它不该在 w.file == nil 时生效——那时**每次写都在丢日志**，
// 等窗口是纯损失，而代价只是一次 OpenFile。
//
// 构造方式：先让重建失败（目录搬走）→ w.file 置 nil，
// 再把目录恢复、且**把 statInterval 调大**，使正常路径不可能重建成功。
func TestWriteRecoversWithoutWaitingStatInterval(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	var errCount int
	w, err := Open(Config{
		Path:            filepath.Join(dir, "app.log"),
		OnError:         func(error) { errCount++ },
		MaxBackups:      1,
		MaxAgeDays:      1,
		DisableCompress: true,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer w.Close()

	// 1. 让重建失败 → w.file == nil。同样不删目录（见 failReopenByBlockingPath）。
	failReopenByBlockingPath(t, w)
	w.statInterval = 0
	if _, werr := w.Write([]byte("lost\n")); werr == nil {
		t.Fatal("重建路径被占位时写入应失败")
	}

	// 2. 解除占位（删掉同名目录），并把 statInterval 设成很大：
	//    若实现仍依赖 reopenIfVanishedLocked 去重建，本次写入必失败。
	if err := os.Remove(w.cfg.Path); err != nil {
		t.Fatalf("解除路径占位: %v", err)
	}
	// 此时无并发写，直接改字段与既有 reopen_test.go 同口径。
	w.statInterval = 24 * time.Hour

	if _, werr := w.Write([]byte("recovered\n")); werr != nil {
		t.Fatalf("file==nil 时应立即重试重建，不得等 statInterval：%v", werr)
	}

	data, err := os.ReadFile(filepath.Join(dir, "app.log"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "recovered") {
		t.Fatalf("自愈后的内容应写进新文件，实际: %q", string(data))
	}
	if errCount == 0 {
		t.Fatal("丢过日志就应上报：不报的话运维无从得知中间少了一段")
	}
}

// TestWriteReportsUnderlyingWriteFailureViaOnError 守护「底层写失败也要上报」。
//
// 前面两个用例用的是「重建失败」路径（w.file == nil），走不到 file.Write 失败那一分支。
// 而磁盘满（ENOSPC）、fd 失效（EBADF）等都会让 file.Write 返回错误——
// 这才是生产上最常见的一种「日志静默停住」。
//
// 构造：同包内直接关掉 writer 持有的 fd（绕过 w.Close），等价于 EBADF。
// 此时 w.file 仍非 nil，Write 会跳过重建分支、直接走到 file.Write 失败处。
func TestWriteReportsUnderlyingWriteFailureViaOnError(t *testing.T) {
	dir := t.TempDir()
	errs := make(chan error, 8)
	w, err := Open(Config{
		Path: filepath.Join(dir, "app.log"),
		OnError: func(e error) {
			select {
			case errs <- e:
			default:
			}
		},
		MaxBackups:      1,
		MaxAgeDays:      1,
		DisableCompress: true,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer w.Close()

	if _, err := w.Write([]byte("ok\n")); err != nil {
		t.Fatalf("首次写入应成功: %v", err)
	}

	// 关掉底层 fd 但保持 w.file 非 nil（不调 w.Close），
	// 使 Write 跳过重建分支、走到 file.Write。
	if err := w.file.Close(); err != nil {
		t.Fatalf("关闭底层 fd: %v", err)
	}

	if _, werr := w.Write([]byte("will-fail\n")); werr == nil {
		t.Fatal("底层 fd 已关闭，写入应当失败")
	}

	var got []string
	for {
		select {
		case e := <-errs:
			got = append(got, e.Error())
			continue
		default:
		}
		break
	}
	if len(got) == 0 {
		t.Fatal("底层写失败必须经 OnError 上报；slog 会吞掉返回的 error（logger.go 里 `_ = ...Handle(...)`）")
	}
	// 断言精确到 “写入” 具体文案（不带 || 容错）：
	// 这条只验证 file.Write 失败这一分支，与重建失败那一支各守各的。
	joined := strings.Join(got, " | ")
	if !strings.Contains(joined, "写入") {
		t.Fatalf("底层写失败必须带“写入”文案上报，实际: %v", got)
	}
	if strings.Contains(joined, "丢失且重建失败") {
		t.Fatalf("本用例走的是 file.Write 失败，不应报成重建失败（两个上报点必须可区分）: %v", got)
	}
}

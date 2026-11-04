// JianManager FR-498 容量基线发生器（入库、可复算）。
//
// # 为什么入库
//
// FR-498 的历次读数（60 源 × 4 档 × 2 口径）都跑在 `.tmp/` 里的临时脚本上，而
// `.tmp/` 被 gitignore ⇒ clone 后**拿不到同一套夹具**，读数无法复算。
// 这正是 PRD 里「harness 需用当前 HEAD 重建重跑」这条长期挂着的根因：
// 不是没能力跑，是**跑过的那套东西没留下来**。
//
// 本工具把夹具固定成仓库内的可执行入口。它只做一件事：**按指定速率往指定的源文件追加日志行**，
// 不碰 Worker、不碰 VL、不判定任何指标——指标由 Worker 自己的打点与 /metrics 给出。
// 这样「产生负载」与「观测」两件事互不耦合，任一侧换实现都不影响另一侧的可比性。
//
// # 用法
//
//	go run ./scripts/capacity-load/ -dir /tmp/cap -sources 60 -rate 30 -duration 120s
//
// 参数：
//
//	-dir        源文件目录（每源一个 <id>.log，不存在则创建）
//	-sources    源数量（默认 60，对齐 FR-498 的 60 源口径）
//	-rate       每源每秒行数（默认 30）
//	-duration   持续时间（默认 120s；长稳窗口按需加长）
//	-prefix     源 id 前缀（默认 inst:cap）
//	-json       结束时把统计以 JSON 打到 stdout（便于脚本消费）
//
// # 口径（复算时必须沿用，否则读数不可比）
//
//   - 每行定长填充（`-width` 默认 120 字节正文），使「行数 × 速率」与「字节速率」线性对应；
//   - 每行带单调递增序号与 RFC3339 时间戳，便于核对丢失/重复；
//   - 速率控制用**固定间隔 ticker**而不是「跑一批再睡」——后者的实际速率会随批次大小漂移，
//     跨机器复算时数字对不上；
//   - 结束时打印实际写入行数与实测速率（**不打印目标值**：实测与目标的差距本身是信息）。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	var (
		dir      = flag.String("dir", "", "源文件目录（必填）")
		sources  = flag.Int("sources", 60, "源数量")
		rate     = flag.Int("rate", 30, "每源每秒行数")
		duration = flag.Duration("duration", 120*time.Second, "持续时间")
		prefix   = flag.String("prefix", "inst:cap", "源 id 前缀")
		width    = flag.Int("width", 120, "每行正文填充宽度（字节）")
		asJSON   = flag.Bool("json", false, "结束时以 JSON 输出统计")
	)
	flag.Parse()

	if *dir == "" {
		fmt.Fprintln(os.Stderr, "capacity-load: -dir 必填")
		os.Exit(2)
	}
	if *sources <= 0 || *rate <= 0 {
		fmt.Fprintln(os.Stderr, "capacity-load: -sources 与 -rate 必须为正")
		os.Exit(2)
	}
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "capacity-load: 创建目录失败: %v\n", err)
		os.Exit(1)
	}

	// 每源一个句柄：先全部打开，避免运行中途 open 抖动被算进速率。
	type sourceFile struct {
		id   string
		path string
		f    *os.File
	}
	files := make([]sourceFile, 0, *sources)
	for i := 0; i < *sources; i++ {
		id := fmt.Sprintf("%s-%03d", *prefix, i)
		// 文件名把 ':' 换掉——Windows 上冒号是非法字符，而这个工具要在两个平台都能跑。
		name := strings.NewReplacer(":", "-", "/", "-").Replace(id) + ".log"
		path := filepath.Join(*dir, name)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "capacity-load: 打开 %s 失败: %v\n", path, err)
			os.Exit(1)
		}
		files = append(files, sourceFile{id: id, path: path, f: f})
	}
	defer func() {
		for _, sf := range files {
			_ = sf.f.Close()
		}
	}()

	interval := time.Second / time.Duration(*rate)
	if interval <= 0 {
		interval = time.Nanosecond
	}

	var written atomic.Int64
	var failed atomic.Int64
	stop := make(chan struct{})
	startedAt := time.Now()

	var wg sync.WaitGroup
	for _, sf := range files {
		wg.Add(1)
		go func(sf sourceFile) {
			defer wg.Done()
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			var seq uint64
			for {
				select {
				case <-stop:
					return
				case <-ticker.C:
					seq++
					line := buildLine(sf.id, seq, *width)
					if _, err := sf.f.WriteString(line); err != nil {
						failed.Add(1)
						continue
					}
					written.Add(1)
				}
			}
		}(sf)
	}

	time.Sleep(*duration)
	close(stop)
	wg.Wait()
	elapsed := time.Since(startedAt)

	// 刷盘：读数要基于「真的落到文件里的字节」，不能只算内存写成功。
	for _, sf := range files {
		if err := sf.f.Sync(); err != nil {
			failed.Add(1)
		}
	}

	total := written.Load()
	rateActual := float64(total) / elapsed.Seconds() / float64(*sources)

	if *asJSON {
		payload := map[string]any{
			"sources":        *sources,
			"targetRate":     *rate,
			"actualRate":     rateActual,
			"durationSec":    elapsed.Seconds(),
			"linesWritten":   total,
			"writeFailures":  failed.Load(),
			"width":          *width,
			"dir":            *dir,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(payload); err != nil {
			fmt.Fprintf(os.Stderr, "capacity-load: 输出 JSON 失败: %v\n", err)
			os.Exit(1)
		}
		return
	}

	fmt.Printf("源数=%d 目标速率=%d 行/s/源 持续=%s\n", *sources, *rate, elapsed.Round(time.Millisecond))
	fmt.Printf("实写行数=%d 实测速率=%.2f 行/s/源 写失败=%d\n", total, rateActual, failed.Load())
	fmt.Printf("源文件目录=%s\n", *dir)
	if f := failed.Load(); f > 0 {
		os.Exit(1)
	}
}

// buildLine 生成一行固定形状的日志：`[HH:MM:SS] [Server thread/INFO]: <seq> <ts> <padding>`。
//
// 为什么形状要固定：Worker 的 normalizer 会把「不以时间戳/级别开头的行」判为多行续行，
// 形状一旦随 seq 变化（比如变长），事件切分就会跟着变，读数在机器之间不可比。
func buildLine(id string, seq uint64, width int) string {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	head := fmt.Sprintf("[%s] [Server thread/INFO]: %s seq=%d ts=%s ",
		time.Now().Format("15:04:05"), id, seq, now)
	if width <= len(head) {
		return head + "\n"
	}
	return head + strings.Repeat("x", width-len(head)) + "\n"
}

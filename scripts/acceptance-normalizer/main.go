// JianManager FR-475 验收 runner（入库、跨平台）：用真 VL 独立核对 Multiline 归一化与恢复语义。
//
// 为什么入库：规格 §5 要求「真实环境验收证据与自动化测试分开记录；测试全绿不替代
// 真 Worker/VL 验收」，而 Windows 侧复验必须拿到同一套可执行入口——原先的 harness 只存在于
// 未入库的 .tmp 目录，clone 后拿不到，故此处固化为仓库内的验收入口。
//
// 前置：一台可访问的真 VL（默认 127.0.0.1:19471，账号 jm/local-only）。可用环境变量覆盖：
//
//	ACCEPT_VL_URL        默认 http://127.0.0.1:19471
//	ACCEPT_VL_USER       默认 jm
//	ACCEPT_VL_PASS       默认 local-only
//	ACCEPT_LINE_ENDING   lf（默认）| crlf
//
// 夹具固定 9 行（含 1 条无换行尾行，写入临时目录，不触碰生产数据）：跨午夜两行、Java 异常 5 行、
// 损坏编码一行、无换行尾行一行。crlf 模式按 Windows 的真实写法以 CRLF 落盘，并额外断言事件正文
// 不含 0x0D（行终止符不得进入正文——这正是 Windows 复验要覆盖的面向）。
//
// 用法（任一平台）：
//
//	go run ./scripts/acceptance-normalizer/
//	ACCEPT_LINE_ENDING=crlf go run ./scripts/acceptance-normalizer/
//
// 退出码：0 = 全部核对通过；1 = 前置不可用或存在失败（便于自动化判据）。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

const (
	defaultBaseURL = "http://127.0.0.1:19471"
	defaultUser    = "jm"
	defaultPass    = "local-only"
)

var (
	baseURL = defaultBaseURL
	user    = defaultUser
	pass    = defaultPass
	crlf    bool
)

// check 记录一项核对的结论；任一项失败则整体退出码非 0。
type check struct {
	name string
	ok   bool
	det  string
}

var checks []check

func record(name string, ok bool, det string) {
	checks = append(checks, check{name: name, ok: ok, det: det})
	if ok {
		fmt.Printf("[OK]   %s — %s\n", name, det)
		return
	}
	fmt.Printf("[FAIL] %s — %s\n", name, det)
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// vlQuery 用真 VL 的 LogsQL 查询接口取回事件（独立核对，不用内存断言）。
func vlQuery(q string) ([]map[string]any, error) {
	form := url.Values{"query": {q}}
	req, err := http.NewRequest(http.MethodPost, baseURL+"/select/logsql/query", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(user, pass)
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("VL 查询 %s: HTTP %d %s", q, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	dec := json.NewDecoder(resp.Body)
	var out []map[string]any
	for dec.More() {
		var row map[string]any
		if err := dec.Decode(&row); err != nil {
			break
		}
		out = append(out, row)
	}
	return out, nil
}

func main() {
	baseURL = envOr("ACCEPT_VL_URL", defaultBaseURL)
	user = envOr("ACCEPT_VL_USER", defaultUser)
	pass = envOr("ACCEPT_VL_PASS", defaultPass)
	crlf = strings.EqualFold(envOr("ACCEPT_LINE_ENDING", "lf"), "crlf")

	fmt.Printf("FR-475 验收：真 VL 核对（VL=%s，行尾=%s）\n\n", baseURL, map[bool]string{true: "CRLF", false: "LF"}[crlf])

	if _, err := vlQuery("* | count()"); err != nil {
		fmt.Println("前置失败：真 VL 不可用：", err)
		os.Exit(1)
	}
	fmt.Println("前置检查：真 VL 可访问")

	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: baseURL, Username: user, Password: pass})
	if err != nil {
		panic(err)
	}

	root, err := os.MkdirTemp("", "fr475-accept-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(root)

	logDir := filepath.Join(root, "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		panic(err)
	}
	livePath := filepath.Join(logDir, "latest.log")

	ns := fmt.Sprintf("fr475acc-%d", time.Now().UnixNano()%100000)
	day := time.Now().UTC().Format("2006-01-02")

	// 夹具：8 行 + 1 条无换行尾行。crlf 模式下除尾行外每行以 CRLF 结尾（Windows 真实写法）。
	term := "\n"
	if crlf {
		term = "\r\n"
	}
	var buf []byte
	for _, line := range []string{
		// 跨午夜：base 锚定当天，23:59:59 应回拨到前一天（猜成未来则失败）
		"[23:59:59] [Server thread/INFO]: before midnight",
		"[00:00:01] [Server thread/INFO]: after midnight",
		// Multiline：Java 异常头 + NPE + at 缩进 + Caused by + at 缩进（5 行应归并为 1 事件）
		"[00:00:02] [Server thread/ERROR]: Encountered an unexpected exception",
		"java.lang.NullPointerException: Cannot invoke \"String.length()\"",
		"\tat net.minecraft.server.MinecraftServer.tick(MinecraftServer.java:1)",
		"Caused by: java.lang.IllegalStateException: nested",
		"\tat com.example.inner(Inner.java:9)",
	} {
		buf = append(buf, []byte(line+term)...)
	}
	// 损坏编码行（非法 UTF-8 字节，模拟损坏编码）：净化后 hash 必须仍与 VL 侧正文一致
	buf = append(buf, []byte("[00:00:03] [Server thread/WARN]: corrupted ")...)
	buf = append(buf, 0xff, 0xfe)
	buf = append(buf, []byte(" bytes"+term)...)
	// 半条事件：无结尾换行，模拟写入中途——应由 Stop 的 Flush 闭合
	buf = append(buf, []byte("[00:00:04] [Server thread/INFO]: tail without newline")...)

	if err := os.WriteFile(livePath, buf, 0o600); err != nil {
		panic(err)
	}
	fmt.Printf("夹具已写入：%s（%d 字节，行尾=%s）\n\n", livePath, len(buf), map[bool]string{true: "CRLF", false: "LF"}[crlf])

	cat := catalog.New(catalog.NewMemJournal())
	m, err := ingest.New(ingest.Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []ingest.SourceConfig{{
		LogSourceID: ns, SourceGeneration: "holder-g1", Path: livePath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: ns, UTCDay: day,
	}}})
	if err != nil {
		panic(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go m.Start(ctx)
	time.Sleep(8 * time.Second)
	cancel()
	time.Sleep(500 * time.Millisecond)
	// Stop 会 Flush 未闭合尾行并做最终 persist（缺它会读到中途快照）。
	if err := m.Stop(); err != nil {
		fmt.Println("Stop 错误:", err)
	}

	// ---- 独立核对：全部以真 VL 查询为准 ----
	fmt.Println("\n=== 独立核对（真 VL 查询）===")
	q := fmt.Sprintf("log_source_id:=%q", ns)
	fmt.Printf("查询：%s\n", q)
	rows, err := vlQuery(q)
	if err != nil {
		fmt.Println("FAIL 查询失败：", err)
		os.Exit(1)
	}
	fmt.Printf("事件总数 = %d（源 9 行，其中堆栈 5 行应归并为 1）\n\n", len(rows))

	// 1) Multiline 归并
	var stack map[string]any
	for _, r := range rows {
		if msg, _ := r["_msg"].(string); strings.Contains(msg, "Encountered an unexpected exception") {
			stack = r
		}
	}
	if stack == nil {
		record("Multiline 归并", false, "未在 VL 中找到堆栈事件")
	} else {
		msg, _ := stack["_msg"].(string)
		lines := strings.Count(msg, "\n") + 1
		record("Multiline 归并", lines == 5 && strings.Contains(msg, "Caused by"),
			fmt.Sprintf("堆栈事件 %d 行（期望 5），Caused by %s", lines,
				map[bool]string{true: "保留", false: "丢失"}[strings.Contains(msg, "Caused by")]))
	}

	// 2) 跨午夜：before 必须回拨到前一天 23:59:59，after 留在当天 00:00:01
	var beforeTime, afterTime string
	for _, r := range rows {
		msg, _ := r["_msg"].(string)
		t, _ := r["_time"].(string)
		if strings.Contains(msg, "before midnight") {
			beforeTime = t
		}
		if strings.Contains(msg, "after midnight") {
			afterTime = t
		}
	}
	switch {
	case beforeTime == "" || afterTime == "":
		record("跨午夜回拨", false, fmt.Sprintf("未取到两行事件（before=%q after=%q）", beforeTime, afterTime))
	default:
		bt, berr := time.Parse(time.RFC3339Nano, beforeTime)
		at, aerr := time.Parse(time.RFC3339Nano, afterTime)
		switch {
		case berr != nil || aerr != nil:
			record("跨午夜回拨", false, fmt.Sprintf("时间解析失败：before=%v after=%v", berr, aerr))
		case !bt.Before(at):
			record("跨午夜回拨", false, fmt.Sprintf("before(%s) 未早于 after(%s)", beforeTime, afterTime))
		case bt.UTC().Format("15:04:05") != "23:59:59" || at.UTC().Format("15:04:05") != "00:00:01":
			record("跨午夜回拨", false, fmt.Sprintf("时刻不符：before=%s after=%s", beforeTime, afterTime))
		default:
			record("跨午夜回拨", true, fmt.Sprintf("before=%s（前一天）after=%s", beforeTime, afterTime))
		}
	}

	// 3) 损坏编码：用 VL 侧实际正文重算 canonical，必须与承诺值一致
	var corruptFound bool
	for _, r := range rows {
		msg, _ := r["_msg"].(string)
		if !strings.Contains(msg, "corrupted") {
			continue
		}
		corruptFound = true
		hash, _ := r["canonical_content_hash"].(string)
		level, _ := r["level"].(string)
		stream, _ := r["stream"].(string)
		rawTime, _ := r["_time"].(string)
		ts, perr := time.Parse(time.RFC3339Nano, rawTime)
		if perr != nil {
			record("损坏编码 hash 一致", false, "VL 侧 _time 解析失败："+perr.Error())
			break
		}
		recomputed := logtypes.CanonicalContentHash(ts.UTC().Format(time.RFC3339Nano), level, stream, msg)
		record("损坏编码 hash 一致", recomputed == hash,
			fmt.Sprintf("VL 正文含替换字符=%v，重算 hash %s 承诺 hash %s",
				strings.Contains(msg, "\uFFFD"), short(recomputed), short(hash)))
		break
	}
	if !corruptFound {
		record("损坏编码 hash 一致", false, "未在 VL 中找到损坏编码事件")
	}

	// 4) 半条事件在 Stop 时被 Flush 闭合
	var tailOK bool
	for _, r := range rows {
		if msg, _ := r["_msg"].(string); strings.Contains(msg, "tail without newline") {
			status, _ := r["parse_status"].(string)
			tailOK = status == "OK"
			record("半条事件闭合", tailOK, fmt.Sprintf("无换行尾行已闭合为事件，parse_status=%s", status))
			break
		}
	}
	if !tailOK {
		record("半条事件闭合", false, "无换行尾行未被 Flush 闭合为事件")
	}

	// 5) CRLF 专项：行终止符不得进入正文（Windows 复验的核心面向）
	if crlf {
		var offenders []string
		for _, r := range rows {
			msg, _ := r["_msg"].(string)
			if strings.Contains(msg, "\r") {
				offenders = append(offenders, short(msg))
			}
		}
		record("CRLF 行尾不进正文", len(offenders) == 0,
			fmt.Sprintf("%d/%d 条事件正文含 0x0D（%s）", len(offenders), len(rows), strings.Join(offenders, " | ")))
	}

	// 6) 账本就绪
	st := m.CutoverReadiness()
	record("账本就绪", st.LedgerReady && len(st.Reasons) == 0,
		fmt.Sprintf("LedgerReady=%v reasons=%v", st.LedgerReady, st.Reasons))

	// 汇总
	failed := 0
	for _, c := range checks {
		if !c.ok {
			failed++
		}
	}
	fmt.Printf("\n=== 汇总：%d/%d 项通过 ===\n", len(checks)-failed, len(checks))
	if failed > 0 {
		fmt.Println("结论：存在失败，验收不通过")
		os.Exit(1)
	}
	fmt.Println("结论：全部通过（请在受控文档中记录本次原始输出）")
}

func short(s string) string {
	if len(s) > 16 {
		return s[:16] + "…"
	}
	return s
}

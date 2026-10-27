package retention

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// 本文件是保留策略驱动器的**读数出口**：把 ArchiveStats 摊成 Prometheus 文本格式，
// 挂在 Worker 那个 127.0.0.1 回环 mux 的 `/metrics` 上（与 pprof 同一个 mux，见 apps/worker/main.go）。
//
// 为什么手写而不引 prometheus/client_golang：
//   - 本仓库此前只有**解析**侧先例（internal/worker/metrics/serverprobe.go 读 ServerProbe 的
//     /metrics），没有产出侧先例，go.mod 里也没有 prometheus 依赖。为了 8 个标量引入
//     client_golang，会连带拉进 prometheus/common、procfs 等一串依赖，附带「全局注册表 +
//     重复注册 panic」的语义——而本驱动器的生命周期是「启动时装配、进程退出才结束」，
//     注册表那套生命周期管理纯属负担。
//   - 要暴露的形态极简：纯标量，无 label、无直方图、无 summary，也没有并发更新的 collector
//     （唯一的并发点已在 ArchiveExecutor 里用 RWMutex 收口）。文本格式是稳定的公开契约
//     （version=0.0.4），逐行写 HELP/TYPE/样本即可。
//   - 手写还有一个实际好处：驱动器为 nil（保留策略未启用）时照样能输出全零序列，
//     不必为了让「注册了但没有实例」成立去编造一个空 collector。

// promTextContentType 是 Prometheus 文本格式的 content type（0.0.4 版 + 显式 UTF-8）。
//
// 带上 charset=utf-8 不是客套：HELP 文本是中文，省略 charset 时部分客户端会按 Latin-1
// 解码，运维在页面上看到的就是乱码。
const promTextContentType = "text/plain; version=0.0.4; charset=utf-8"

// EventStorePruneSource 是回收指标需要的**最小读数口径**，由 ingest.Manager 实现。
//
// 为什么用接口而不是直接引 ingest：retention 是底层包（ingest 依赖它），反向引用会成环。
// 这里只声明真正要读的字段，实现方多出来的信息（逐源明细等）不进接口。
type EventStorePruneSource interface {
	// EventStorePruneSnapshot 返回最近一轮事件段回收的读数。
	// ran 为 false 表示从未运行过（指标应输出 0 而不是伪造一次运行）。
	EventStorePruneSnapshot() (ran bool, at time.Time, segments, bytes, events int64, failures int, disabled bool)
}

// MetricsHandler 返回 `/metrics` 的处理器。
//
// d 为 nil（保留策略未启用）时**不返回 404、也不返回空 body**：照样输出全部序列、值取 0，
// 并把 jm_log_retention_driver_enabled 置 0。理由是不让抓取侧为「未启用」写特殊分支——
// 对告警规则来说「序列消失」与「序列恒为 0」是两种完全不同的判断，前者会被当成采集故障，
// 于是「策略没开」和「worker 挂了」在告警里长得一模一样。
//
// prune 为 nil 时回收相关序列输出 0 且 ran=0，理由同上：抓取侧不必区分「没装配」与「没跑」。
func MetricsHandler(d *Driver, prune EventStorePruneSource) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", promTextContentType)
		// 输出走 bufio：一次抓取是十几行小写入，逐行写 ResponseWriter 会产生多次小包。
		bw := bufio.NewWriter(w)
		// 写错误只可能来自连接已断（客户端放弃本次抓取），此时既无处上报也无从补救；
		// bufio 会把首个错误粘住，Flush 一并带出，统一丢弃。
		_ = writeMetrics(bw, d, prune)
		_ = bw.Flush()
	}
}

// writeMetrics 逐行写出全部序列（HELP/TYPE 紧挨着各自的样本，顺序固定，
// 使 `curl -s /metrics | diff` 这种最土的核对方式可用）。
func writeMetrics(w io.Writer, d *Driver, prune EventStorePruneSource) error {
	var st ArchiveStats
	enabled := int64(0)
	if d != nil {
		enabled = 1
		// Executor() 与 Stats() 都对 nil 安全，这里仍显式判空：Driver 的 exec 由构造函数装配，
		// 但零值 Driver 在测试里可能出现，读数端点不该因此 panic。
		if ex := d.Executor(); ex != nil {
			st = ex.Stats()
		}
	}
	// 零值时间不能直接取 Unix()（那是个 19 亿秒的负数），必须显式折成 0：
	// 约定的口径是「0 表示从未运行」，抓取侧据此写 `absent or == 0` 类判定。
	lastRunSeconds := int64(0)
	if !st.LastRunAt.IsZero() {
		lastRunSeconds = st.LastRunAt.Unix()
	}
	lastErrorPresent := int64(0)
	if st.LastError != "" {
		lastErrorPresent = 1
	}

	// 事件段回收读数：prune 未装配或从未运行过时全部保持 0。
	// 这里刻意不把「没跑过」编码成 -1：抓取侧统一按 0 处理，避免多一个分支。
	var (
		pruneEnabled  int64
		pruneLastRun  int64
		pruneSegments int64
		pruneBytes    int64
		pruneEvents   int64
		pruneFailures int64
	)
	if prune != nil {
		ran, at, segs, by, evs, fails, disabled := prune.EventStorePruneSnapshot()
		if !disabled {
			pruneEnabled = 1
		}
		if ran && !at.IsZero() {
			pruneLastRun = at.Unix()
			pruneSegments = segs
			pruneBytes = by
			pruneEvents = evs
			pruneFailures = int64(fails)
		}
	}

	// 只暴露「最近一次失败是否存在」，不暴露错误文本：文本放进 label 会把基数与转义问题
	// 一起引进告警链路（错误信息里普遍带命名空间与路径），而定位问题看启动日志更直接。
	samples := []struct {
		name  string
		help  string
		kind  string
		value int64
	}{
		{
			name: "jm_log_retention_driver_enabled", kind: "gauge", value: enabled,
			help: "保留策略驱动器是否启用（1 启用 / 0 未启用，未启用时其余指标恒为 0）",
		},
		{
			name: "jm_log_retention_archive_planned_total", kind: "counter", value: st.Planned,
			help: "累计进入归档计划的目标数（含未到点目标）",
		},
		{
			name: "jm_log_retention_archive_moved_total", kind: "counter", value: st.Moved,
			help: "累计搬运到冷层成功的分区数",
		},
		// 次选路径（导出归档）的三条读数。分三条而不是一条，是因为它们回答三个不同问题：
		// 「导出成功了吗」（copied）、「热层真的省下来了吗」（reclaimed）、
		// 「导出成功但没获准删的有多少」（copied_kept_original）。只看第一个数会把
		// 「归档一直在成功、只是三闸没开」误读成「盘应该省下来了」。
		{
			name: "jm_log_retention_archive_copied_total", kind: "counter", value: st.ArchiveCopied,
			help: "累计导出归档并逐文件回读校验成功的分区数（不代表热层已回收）",
		},
		{
			name: "jm_log_retention_archive_copy_reclaimed_total", kind: "counter", value: st.ArchiveCopyReclaimed,
			help: "累计导出归档成功且热层已回收的分区数（需删除三闸全开）",
		},
		{
			name: "jm_log_retention_archive_copy_kept_original_total", kind: "counter", value: st.ArchiveCopyKeptOriginal,
			help: "累计导出归档成功但保留原物的分区数（三闸未全开、删除出口未装配或回收失败）",
		},
		{
			name: "jm_log_retention_archive_rolled_back_total", kind: "counter", value: st.ArchiveRolledBack,
			help: "累计已迁移 ARCHIVED 但回收失败、权威回滚为热层权威的分区数",
		},
		{
			name: "jm_log_retention_archive_kept_original_total", kind: "counter", value: st.KeptOriginal,
			help: "累计「保留原物」的分区数（搬运/导出失败、无归档路径、删除出口未装配或删除失败、或导出成功但未获准回收）",
		},
		{
			name: "jm_log_retention_archive_discarded_total", kind: "counter", value: st.Discarded,
			help: "累计真的执行成功的显式删除分区数",
		},
		{
			name: "jm_log_retention_archive_not_due_total", kind: "counter", value: st.NotDue,
			help: "累计未到点（未达保留期且未触发磁盘水位）的分区数",
		},
		{
			name: "jm_log_retention_archive_last_run_timestamp_seconds", kind: "gauge", value: lastRunSeconds,
			help: "最近一轮归档执行的时刻（Unix 秒），0 表示从未运行",
		},
		{
			name: "jm_log_retention_archive_last_error_present", kind: "gauge", value: lastErrorPresent,
			help: "最近一次归档失败原因是否存在（1 存在 / 0 不存在）",
		},
		// ---- 事件段回收（eventstore_prune）：与搬运是两条独立链路，读不通不要并排比 ----
		//
		// 分组理由：上面 7 条是「HOT→COLD 搬运」，这里 6 条是「平台自己的暂存段回收」。
		// 后者删的是投递完成前的暂存副本，不删用户原档、也不删 VL 数据。
		{
			name: "jm_log_eventstore_prune_enabled", kind: "gauge", value: pruneEnabled,
			help: "事件段回收是否启用且已装配（1 启用 / 0 未启用或未装配）",
		},
		{
			name: "jm_log_eventstore_prune_last_run_timestamp_seconds", kind: "gauge", value: pruneLastRun,
			help: "最近一轮事件段回收的时刻（Unix 秒），0 表示从未运行",
		},
		{
			name: "jm_log_eventstore_prune_reclaimed_segments_total", kind: "counter", value: pruneSegments,
			help: "最近一轮回收掉的段数（累计口径 = 每轮相加由抓取侧负责；本序列报单轮值）",
		},
		{
			name: "jm_log_eventstore_prune_reclaimed_bytes_total", kind: "counter", value: pruneBytes,
			help: "最近一轮回收掉的字节数",
		},
		{
			name: "jm_log_eventstore_prune_reclaimed_events_total", kind: "counter", value: pruneEvents,
			help: "最近一轮回收掉的事件条数",
		},
		{
			name: "jm_log_eventstore_prune_failures", kind: "gauge", value: pruneFailures,
			help: "最近一轮回收中报错的源数（水位算不清、校准失败、写清单失败等）",
		},
	}
	for _, s := range samples {
		// 注意不要用 %d 直接拼值：一旦有人把 value 换成浮点或负值，格式会静默变形；
		// 统一走 strconv 让「数值怎么序列化」只有一处定义。
		if _, err := fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n%s %s\n",
			s.name, s.help, s.name, s.kind, s.name, strconv.FormatInt(s.value, 10)); err != nil {
			return err
		}
	}
	return nil
}

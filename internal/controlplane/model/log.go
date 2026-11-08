package model

// LogSource 日志来源类别。
type LogSource string

const (
	// LogSourceInstance 实例运行日志（采集自 Worker 转发的进程 stdout/stderr）。
	LogSourceInstance LogSource = "instance"
	// LogSourceControlPlane 平台 Control Plane 自身结构化日志。
	LogSourceControlPlane LogSource = "control_plane"
	// LogSourceWorker 平台 Worker Node 自身结构化日志（经 gRPC 上报或本地直采，预留）。
	LogSourceWorker LogSource = "worker"
)

// AllLogSources 是 LogSource 的封闭取值域（真源）。
//
// 为什么需要显式枚举：保留/归档策略要按「Legacy 集合的补集」圈定 platform 来源，而 SQL 里
// 的否定条件（NOT IN）在 SQLite 上无法走索引，只能全表扫描（2026-10-09 生产实测：109 万行
// 全扫 226ms，而等值走索引 8ms）。求补集必须知道全集，故在此集中声明；新增来源时只改这里，
// 消费方（如 service.nonLegacyLogSources）自动跟随，避免清单漂移导致新来源被保留策略漏掉。
var AllLogSources = []LogSource{
	LogSourceInstance,
	LogSourceControlPlane,
	LogSourceWorker,
}

// LogLevel 日志级别。实例日志优先取行内结构化 level=（slog/logrus 等）；
// 无结构化级别时 stdout 默认归为 info、stderr 归为 error；平台日志沿用 slog 级别。
type LogLevel string

const (
	LogLevelDebug LogLevel = "debug"
	LogLevelInfo  LogLevel = "info"
	LogLevelWarn  LogLevel = "warn"
	LogLevelError LogLevel = "error"
)

// 说明（2026-10-29）：本文件曾定义 `LogEntry`（表名 logs）——CP 本地日志表。
//
// 它已被**废弃并删除**，原因不是“换个介质”这么简单：
//   - 它把日志正文与业务数据（用户/权限/实例）混在同一个库里：备份被日志撑大，
//     VACUUM 被日志行拖住，库损坏会连累登录与实例管理；
//   - 检索是 `message LIKE '%kw%'` 全表扫（生产 109 万行实测），只能靠 14 天/512MB
//     保留期硬压住规模；
//   - 保留靠“先归档 NDJSON、再 DELETE 行”维持，而 DELETE 大表不回收空间，只能 VACUUM。
//
// 现在日志分两路：
//   - **实例日志** → Worker 的受管 VictoriaLogs（ADR-094），CP 只做授权与联邦查询；
//   - **平台（CP 自身）日志** → 自轮转文件（internal/platform/rollog），
//     面板读尾部（/api/v1/diagnostics/log），要查更早的走 grep。
//
// 保留 LogSource / LogLevel 两个枚举：它们仍被联邦查询、前端着色与采集侧使用，
// 与“是否还有那张表”无关。

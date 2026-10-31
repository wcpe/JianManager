package ingest

// HTTP 日志接收入口（FR-511）：对齐官方 VictoriaLogs 的 `/insert/jsonline` 端点，
// 让外部采集器（Vector / Fluent Bit / Filebeat / 自研 agent）以 ndjson 推送给 Worker。
//
// 设计核心（硬约束对齐）：
//   - 复用现有 pipeline、不新建存储：每条推送事件经目标 push-only 源的
//     `pipeline.Pipeline.Ingest` 进入**同一条 WAL**，继承既有攒批 / gzip / 流标签 /
//     容量门禁 / 背压 / 丢弃语义（见背压继承论证与测试）。
//   - 不开独立队列：HTTP 入口没有自己的缓冲，全部走 WAL 上限保护，不存在绕过内存上限
//     偷偷吃光内存的旁路。
//   - 监听保持回环：本 handler 挂在 `wsMux` 上，而 `wsMux` 由 `localWSAddr` 绑 127.0.0.1；
//     跨机推送走 SSH 隧道，不扩大攻击面。
//   - event_id 口径不变：`hash(log_source_id, source_generation, record_start,
//     record_end, parser_version)`（见 logtypes.EventID）。本文件只负责构造不撞车的
//     record_start/end（同源单调递增伪字节区间）。
//   - 限流：单请求 body 上限（防恶意大包）+ 并发上限（防止瞬时打爆）。

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/normalize"
)

// DefaultJSONLineOptions 是接收端点的默认限流与命名参数。
const (
	// defaultJSONLineMaxBodyBytes 单请求 body 上限 4 MiB：足以容纳数千条短日志，
	// 又能把「一个恶意大包打爆内存」的上限钉死（硬约束 5）。
	defaultJSONLineMaxBodyBytes int64 = 4 << 20
	// defaultJSONLineMaxConcurrency 同时处理的请求数上限：与采集投递共用 127.0.0.1 端口，
	// 限并发避免瞬时涌入占满 goroutine / 内存。
	defaultJSONLineMaxConcurrency = 16
	// defaultJSONLineSourceGeneration 是未显式给出 source_generation 时的兜底代次。
	// 它是「同源稳定」的值——event_id 依赖它，必须跨重启一致（不随机），否则同一批日志
	// 的 event_id 会变，破坏下游幂等。
	defaultJSONLineSourceGeneration = "http-push"
	// jsonLineFieldMsg / jsonLineFieldTime / jsonLineFieldStream 是 VL 原生字段名；
	// 同时接受「message / time / stream」友好别名（见 incomingLogLine）。
	jsonLineFieldMsg    = "_msg"
	jsonLineFieldTime   = "_time"
	jsonLineFieldStream = "_stream"
)

// JSONLineReceiverOptions 配置 HTTP 日志接收端点。
type JSONLineReceiverOptions struct {
	// MaxBodyBytes 单请求 body 上限（字节）；≤0 用默认 4 MiB。
	MaxBodyBytes int64
	// MaxConcurrency 同时处理的请求数上限；≤0 用默认 16。
	MaxConcurrency int
	// DefaultSourceGeneration 未显式给出 source_generation 时的兜底代次；空串用默认 "http-push"。
	DefaultSourceGeneration string
	// JWTSecret 校验 token 的 HS256 密钥（复用 CP 下发的 wsTokenSecret）。空串时关闭鉴权
	// （仅用于测试 / 纯本机回环可信环境）。
	JWTSecret string
}

// JSONLineReceiver 是 `/insert/jsonline` 的处理器（实现 http.Handler）。
type JSONLineReceiver struct {
	manager *Manager
	secret  string
	maxBody int64
	sem     chan struct{}
	defGen  string

	// ranges 维护每个源（key=log_source_id/source_generation）的单调伪字节游标，
	// 用于构造不撞车的 record_start/end。随机基底降低跨重启撞车概率（见设计说明）。
	ranges sync.Map // map[string]*uint64
}

// NewJSONLineReceiver 构造接收端点处理器。manager 为 ingest.Manager（其 pipes 里必须已登记
// 对应的 PUSH_ONLY 源，handler 不动态建源）。secret 为 JWT 校验密钥。
func NewJSONLineReceiver(manager *Manager, opts JSONLineReceiverOptions) *JSONLineReceiver {
	maxBody := opts.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = defaultJSONLineMaxBodyBytes
	}
	conc := opts.MaxConcurrency
	if conc <= 0 {
		conc = defaultJSONLineMaxConcurrency
	}
	defGen := opts.DefaultSourceGeneration
	if defGen == "" {
		defGen = defaultJSONLineSourceGeneration
	}
	return &JSONLineReceiver{
		manager: manager,
		secret:  opts.JWTSecret,
		maxBody: maxBody,
		sem:     make(chan struct{}, conc),
		defGen:  defGen,
	}
}

// ServeHTTP 实现 http.Handler。仅接受 POST，其余方法返回 405。
func (r *JSONLineReceiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "仅支持 POST", http.StatusMethodNotAllowed)
		return
	}
	// 鉴权：优先 query ?token=，其次 Authorization: Bearer。
	if !r.authorize(req) {
		http.Error(w, "token 无效或已过期", http.StatusUnauthorized)
		return
	}
	// 限并发：占一个信号量槽，结束释放。
	select {
	case r.sem <- struct{}{}:
		defer func() { <-r.sem }()
	default:
		http.Error(w, "并发超限，请稍后重试", http.StatusServiceUnavailable)
		return
	}

	logSourceID := strings.TrimSpace(req.URL.Query().Get("log_source_id"))
	if logSourceID == "" {
		http.Error(w, "缺少 log_source_id 查询参数", http.StatusBadRequest)
		return
	}
	generation := strings.TrimSpace(req.URL.Query().Get("source_generation"))
	if generation == "" {
		generation = r.defGen
	}
	key := logSourceID + "/" + generation

	// 先解析目标管道：未知源直接 404，不动态建源（避免绕过配置面）。
	pipe, ok := r.manager.pipeFor(key)
	if !ok {
		http.Error(w, fmt.Sprintf("未登记的日志源 %s（需在 worker.yml 的 log_sources 中配置 mode=PUSH_ONLY）", key), http.StatusNotFound)
		return
	}

	// body 上限：MaxBytesReader 在超限时让 Read 返回 ErrUnexpectedEOF，转 413。
	body := http.MaxBytesReader(w, req.Body, r.maxBody)
	raw, err := io.ReadAll(body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) || errors.Is(err, io.ErrUnexpectedEOF) {
			http.Error(w, "请求体超过大小上限", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "读取请求体失败", http.StatusBadRequest)
		return
	}

	// 先全量解析校验，再统一 Ingest：畸形 JSON / 缺字段 → 400 且**不写 WAL**（不污染已有数据）。
	events, parseErr := r.decodeLines(logSourceID, generation, raw)
	if parseErr != nil {
		http.Error(w, parseErr.Error(), http.StatusBadRequest)
		return
	}
	if len(events) == 0 {
		// 空 body（只有空白）视为成功、零条摄入，避免把「心跳空包」当错误。
		writeJSONLineOK(w, 0)
		return
	}

	// 走目标源既有 Pipeline.Ingest → 同一条 WAL，继承攒批 / gzip / 背压 / 丢弃语义。
	if err := pipe.Ingest(events); err != nil {
		// 容量暂停等由 pipeline 如实返回；这里如实上报，不静默吞掉。
		http.Error(w, "摄入失败："+err.Error(), http.StatusServiceUnavailable)
		return
	}
	writeJSONLineOK(w, len(events))
}

// authorize 校验 WS 密钥签发的 JWT（与 ws 终端同套逻辑）。secret 为空时放行（仅测试 / 可信本机）。
func (r *JSONLineReceiver) authorize(req *http.Request) bool {
	if r.secret == "" {
		return true
	}
	tokenStr := req.URL.Query().Get("token")
	if tokenStr == "" {
		if ah := req.Header.Get("Authorization"); strings.HasPrefix(ah, "Bearer ") {
			tokenStr = strings.TrimPrefix(ah, "Bearer ")
		}
	}
	if tokenStr == "" {
		return false
	}
	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
		return []byte(r.secret), nil
	})
	if err != nil || !token.Valid {
		return false
	}
	return true
}

// incomingLogLine 是单条推送 JSON 的解析形态。VL 原生字段（_msg/_time/_stream）与友好别名
// （message/time/stream）都接受；其余字段作为 Fields 透传（低基数流标签由投递侧白名单约束）。
type incomingLogLine struct {
	Msg       string            `json:"_msg"`
	MsgAlt    string            `json:"message"`
	Time      string            `json:"_time"`
	TimeAlt   string            `json:"time"`
	Stream    string            `json:"_stream"`
	StreamAlt string            `json:"stream"`
	Level     string            `json:"level"`
	Extra     map[string]string `json:"-"`
}

// decodeLines 把 ndjson 正文解析为规范事件。任意一行畸形即返回错误（不产出任何事件）。
func (r *JSONLineReceiver) decodeLines(logSourceID, generation string, raw []byte) ([]logtypes.Event, error) {
	src := logtypes.SourceIdentity{
		LogSourceID:      logSourceID,
		SourceGeneration: generation,
		ParserVersion:    normalize.ParserVersion,
	}
	cursor := r.rangeFor(logSourceID, generation)

	var events []logtypes.Event
	lineNo := 0
	for _, line := range strings.Split(string(raw), "\n") {
		lineNo++
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue // 跳过空行（含结尾换行）
		}
		var rawLine map[string]json.RawMessage
		dec := json.NewDecoder(strings.NewReader(trimmed))
		dec.UseNumber()
		if err := dec.Decode(&rawLine); err != nil {
			return nil, fmt.Errorf("第 %d 行不是合法 JSON：%w", lineNo, err)
		}
		parsed, err := parseLogLine(rawLine)
		if err != nil {
			return nil, fmt.Errorf("第 %d 行字段非法：%w", lineNo, err)
		}
		message := parsed.Msg
		if message == "" {
			message = parsed.MsgAlt
		}
		if message == "" {
			return nil, fmt.Errorf("第 %d 行缺少 message / _msg 字段", lineNo)
		}
		stream := parsed.Stream
		if stream == "" {
			stream = parsed.StreamAlt
		}
		eventTime := parsed.Time
		if eventTime == "" {
			eventTime = parsed.TimeAlt
		}
		eventTimeUTC, err := normalizeEventTime(eventTime)
		if err != nil {
			return nil, fmt.Errorf("第 %d 行时间字段非法：%w", lineNo, err)
		}
		ingestTimeUTC := time.Now().UTC().Format(time.RFC3339Nano)

		// 同源单调递增伪字节区间：半开 [start, end)，每条至少占 1 单位 ⇒ 永不重叠 ⇒ event_id 不撞车。
		start, end := nextRange(cursor, uint64(len([]byte(message))))
		rec := logtypes.RecordRange{Start: start, End: end}

		ev := logtypes.BuildEvent(src, rec, eventTimeUTC, ingestTimeUTC, parsed.Level, stream, message)
		ev.Fields = parsed.Extra
		events = append(events, ev)
	}
	return events, nil
}

// rangeFor 返回（必要时初始化）某源的单调游标指针，基底随机以降低跨重启撞车概率。
func (r *JSONLineReceiver) rangeFor(logSourceID, generation string) *uint64 {
	key := logSourceID + "/" + generation
	if v, ok := r.ranges.Load(key); ok {
		return v.(*uint64)
	}
	base := rand.Uint64() % (uint64(1) << 40) // 仅用于错开不同进程/源的起始，避免从 0 起步
	ptr := &base
	actual, _ := r.ranges.LoadOrStore(key, ptr)
	return actual.(*uint64)
}

// nextRange 以伪字节区间推进游标：每条至少占 1，[start,end) 连续不重叠。
func nextRange(cursor *uint64, msgLen uint64) (uint64, uint64) {
	consumed := msgLen + 1
	end := atomic.AddUint64(cursor, consumed)
	start := end - consumed
	return start, end
}

// parseLogLine 从原始 JSON 对象抽取已知字段，其余转入 Extra（值统一转字符串）。
func parseLogLine(raw map[string]json.RawMessage) (*incomingLogLine, error) {
	out := &incomingLogLine{Extra: map[string]string{}}
	for k, v := range raw {
		switch k {
		case jsonLineFieldMsg:
			out.Msg = stringValue(v)
		case "message":
			out.MsgAlt = stringValue(v)
		case jsonLineFieldTime:
			out.Time = stringValue(v)
		case "time":
			out.TimeAlt = stringValue(v)
		case jsonLineFieldStream:
			out.Stream = stringValue(v)
		case "stream":
			out.StreamAlt = stringValue(v)
		case "level":
			out.Level = stringValue(v)
		default:
			out.Extra[k] = stringValue(v)
		}
	}
	return out, nil
}

// stringValue 把任意 JSON 标量转成字符串；对象 / 数组保留 JSON 原文（便于透传富字段）。
func stringValue(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// 非字符串标量（数字 / 布尔）或复合值：保留原始文本。
	return strings.TrimSpace(string(raw))
}

// normalizeEventTime 解析事件时间：空值用当前 UTC；支持 RFC3339 及常见宽松格式。
func normalizeEventTime(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Now().UTC().Format(time.RFC3339Nano), nil
	}
	// 优先 RFC3339（含纳秒、带 Z）。
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC().Format(time.RFC3339Nano), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC().Format(time.RFC3339Nano), nil
	}
	// 兼容 VL 常见浮点 epoch 秒（如 1700000000.123）。
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return time.Unix(0, int64(f*1e9)).UTC().Format(time.RFC3339Nano), nil
	}
	return "", fmt.Errorf("无法解析时间 %q", s)
}

// writeJSONLineOK 返回成功响应（NDJSON 计数）。
func writeJSONLineOK(w http.ResponseWriter, n int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]int{"ingested": n})
}

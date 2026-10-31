package vlsup

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

// 连接池默认值。
//
// 为什么需要显式 Transport（此前用 net/http 的默认 Transport）：默认 Transport 的
// MaxIdleConnsPerHost 是 2，而采集侧是**多源并发投递**（pollConcurrency 默认 8，生产 60 源），
// 每源每轮一次 POST + 若干次查询。空闲连接超出 2 条就被关掉、下一次请求重新 TCP 握手，
// 等于把并发压回「2 条长连接轮流排队」。这与 2026-09-28 事故的观感一致：
// 单请求耗时里含握手 + 首字节延迟，而 VL 就在 127.0.0.1 上、握手成本纯属浪费。
const (
	// defaultMaxIdleConnsPerHost 取并发投递的源数量级（8）的 4 倍余量。
	// 取 32 而不是 8 的理由：一轮里每源还会发查询（探测/幂等过滤/缺口取证），
	// 写入与查询共用同一池，按「并发源数 × 每源在飞请求数」的上界给，宁多不多少。
	defaultMaxIdleConnsPerHost = 32
	// defaultIdleConnTimeout 远大于采集轮周期（250ms ticker、轮级十几秒），
	// 保证「上一轮用过的连接下一轮还在」，同时不让死连接长期占住 fd。
	defaultIdleConnTimeout = 90 * time.Second
)

// ErrUnauthorized 表示 VL Basic-auth 未通过（401/403）。
var ErrUnauthorized = errors.New("vlsup: unauthorized")

// DefaultStreamFields 是 `/insert/jsonline` 的 `_stream_fields` 默认取值。
//
// VictoriaLogs 的「流（stream）」是比字段更粗一级的**分区键**：同一 stream 内的行共享
// 元数据与倒排索引，所以 **stream 的基数直接决定 VL 的元数据与内存开销**。把逐行变化的
// 字段放进 stream，等于让 VL 为每一行建一个 stream —— 元数据爆炸、内存被拖死，而且这种
// 错误只在写入量上来之后才显形，事后无法靠查询侧补救。
//
// 因此这个取值是**架构裁定**（2026-10-31），不是可随手调整的调优项：
//
//	字段            基数         入流  理由
//	log_source_id   19（实测）   ✅    查询最常用维度，基数极小
//	stream          2-3          ✅    日志固有的 stdout/stderr 分流
//	level           5-10         ❌    级别随内容变化 ⇒ 同一源今天全 INFO、明天冒 WARN
//	                                   就会切出新分区（流数随内容漂移，不可预测）
//	event_id 等     无界         ❌    每行不同 ⇒ 见 forbiddenStreamFields
//
// 总流数上界 ≈ 19 × 3 ≈ 57。
//
// ⚠ level 属于「**可配置但不推荐**」那一类，而不是「忘了排除」：它的基数（5-10）小得
// 足以入流，但**会随日志内容漂移**（同一源今天全 INFO、明天冒 WARN 就切出新分区），
// 于是 stream 数缓慢增长且不可预测。它不像 event_id 那样“每行一个流”，也不会把 VL
// 拖垮，因此不列入 forbiddenStreamFields（那张表的语义是“会让 VL 崩溃”，必须与“不推荐”
// 严格分开，否则真出问题时运维无法从错误文案里分辨严重程度）。
const DefaultStreamFields = "log_source_id,stream"

// DefaultMsgField / DefaultTimeField 显式声明消息与时间字段名。
//
// 这两个值恰好是 VL 的缺省值（不传时它就按 `_msg` / `_time` 读我们投递的 JSON 行），
// 显式传的意义**不是**改变行为，而是不让行为依赖 VL 的版本默认值：默认值一旦随版本
// 变化，我们的行会被静默解释成「没有消息 / 没有时间」—— 那是静默数据损坏，不是报错。
const (
	DefaultMsgField  = "_msg"
	DefaultTimeField = "_time"
)

// forbiddenStreamFields 是「绝对禁止放进 stream」的字段名及其理由：基数无界。
//
// 这张表的语义边界很窄：**仅收「会给每行建一个新 stream」的字段**。不要把“不推荐但
// 可控”的字段放进来（典型是 level）—— 两者混在一起会让错误文案失去严重程度的分辨力。
//
// 为什么要这道守卫（而不是只写在注释里）：这个错误是**单向不可逆**的 —— 一旦写进 VL，
// stream 元数据只能靠删除分区才回收。所以在构造客户端时就拒（fail fast），让配错的人
// 在启动日志里看到中文原因，而不是等生产内存曲线爆掉再回查。
var forbiddenStreamFields = map[string]string{
	"event_id":               "每条事件唯一 ⇒ 每行一个新 stream",
	"record_start":           "随事件时间单调前进 ⇒ 每行一个新 stream",
	"record_end":             "同 record_start",
	"ingest_time_utc":        "随投递时刻前进 ⇒ 每行一个新 stream",
	"canonical_content_hash": "按内容变化 ⇒ 基数随日志内容无限增长",
	"_msg":                   "消息正文 ⇒ 基数无界",
	"_time":                  "事件时间 ⇒ 基数无界",
}

// ValidateStreamFields 校验逗号分隔的 stream 字段列表（空项与无界字段都拒绝）。
func ValidateStreamFields(spec string) error {
	for _, raw := range strings.Split(spec, ",") {
		name := strings.TrimSpace(raw)
		if name == "" {
			return fmt.Errorf("vlsup: _stream_fields 含空字段名（%q）", spec)
		}
		if reason, bad := forbiddenStreamFields[name]; bad {
			return fmt.Errorf("vlsup: _stream_fields 不得包含 %q：%s；无界基数字段入流会让 VL 为每行建 stream，元数据与内存不可控", name, reason)
		}
	}
	return nil
}

// ErrNotLoopback 表示客户端 base URL 非 localhost（生产强制 localhost 查询）。
var ErrNotLoopback = errors.New("vlsup: client base URL must be loopback")

// StatusError 表示 VL 返回非 2xx（且非认证类）响应；保留状态码供调用方判断
// 「重试是否有意义」（见 IsPermanent）。Error() 文案与既有实现逐字一致，避免影响断言。
type StatusError struct {
	Path string
	Code int
	Body string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("vlsup: %s status %d: %s", e.Path, e.Code, e.Body)
}

// IsPermanent 判定「重试无意义」的 VL 错误：认证失败，以及 4xx 语义错误
// （查询被 VL 拒绝，例如参数或时间范围不合法——重试多少次都不会变）。
//
// 用途（B1c）：采集侧的投影校验原先对任何错误都以固定 200ms 间隔重试到超时（默认 30 秒，
// 即最多约 150 次查询/源），在负载高时把 VL 与磁盘一起压垮（2026-09-28 生产事故的成因之一：
// 出现 `cannot execute query [...]` 这类 4xx 后仍被重试到超时）。传输类与 5xx 错误不在
// 此列——它们可能瞬时恢复，应继续退避重试。
func IsPermanent(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrUnauthorized) {
		return true
	}
	var se *StatusError
	if errors.As(err, &se) {
		return se.Code >= 400 && se.Code < 500
	}
	return false
}

// Client 是面向 127.0.0.1 VictoriaLogs 实例的 Basic-auth HTTP 查询助手。
type Client struct {
	baseURL  string
	username string
	password string
	hc       *http.Client
	hq       *http.Client
	// compress 开启 /insert/jsonline 请求体的 gzip 压缩（见 ClientOptions.DisableCompression）。
	compress bool
	// streamFields 是 `/insert/jsonline` 的 `_stream_fields` 取值；空串表示**不发**该参数
	// （退回本改动之前的线上形态）。取值只能来自 DefaultStreamFields 或经校验的覆盖值。
	streamFields string
	// compressionRejected 是「VL 实测拒掉了 gzip 请求体」的进程内标记。
	//
	// 只在“重试未压缩形态成功”时置位：那是能证明「问题在压缩、不在数据」的唯一形态。
	// 一旦置位，后续写入直接走未压缩形态（不再每次都白跑一趟被拒的请求）；重启复位。
	compressionRejected atomic.Bool
}

// ClientOptions 构造 Client。
type ClientOptions struct {
	// BaseURL 例如 http://127.0.0.1:19441 。测试可注入 httptest.Server URL（仍是 127.0.0.1）。
	BaseURL  string
	Username string
	Password string
	// HTTPClient 可注入；nil 时使用默认超时客户端。
	HTTPClient *http.Client
	// QueryTimeout 是查询类请求（Get/Stream）的客户端超时；0 用默认值。
	// 写入类请求（InsertJSONLines/Post）仍用短超时：写入已按批切分，单请求很小；
	// 而查询耗时随事件时间跨度增长（projection 过滤无索引，可能扫全天），
	// 共用 5s 会误杀——实测（2026-09-28 生产）校验查询反复 Client.Timeout exceeded。
	QueryTimeout time.Duration
	// AllowNonLoopback 仅测试使用：跳过 loopback 校验。生产必须保持 false。
	AllowNonLoopback bool
	// DisableCompression 关闭 /insert/jsonline 请求体的 gzip 压缩（默认开启）。
	//
	// 为什么默认开：NDJSON 投影行的重复度极高（同一批的 log_source_id /
	// projection_generation / parser_version 等字段完全重复），实测 200 × 326B 的批
	// 压到 895B（约 73×）。写入是本仓唯一的高频对外请求，省下的字节直接换成 VL 侧的
	// 解压/解析压力下降。
	//
	// 为什么要留开关：传输层优化不该成为采集失败的单点。VL 侧若不接受
	// `Content-Encoding: gzip`（历史版本或改配），运维可以用它退回不压缩形态，
	// 而不必改代码（见 InsertJSONLines 的退回逻辑）。
	DisableCompression bool
	// StreamFields 覆盖 `/insert/jsonline` 的 `_stream_fields`（逗号分隔）。
	//
	// 空 = DefaultStreamFields。生产不走这条路（取值由架构裁定）；它留给测试与诊断，
	// 例如验证「把无界字段塞进 stream 会被拒绝」。任何取值都要过 ValidateStreamFields。
	StreamFields string
	// DisableStreamFields 关闭流标签（不发 `_stream_fields`）。
	//
	// 为什么留这个开关：流标签是**新建分区键**，一旦启用，同一个 VL 里新旧数据的分区形态
	// 就不一致（旧数据全落在同一个 stream 里）。绝大多数部署要的是「新数据按源/通道分流」，
	// 但外部回环 VL 由别的系统一并写入、或需要与既有 stream 布局对齐的部署不希望改变
	// 分区形态 —— 它们必须能退回。关掉后退回本改动之前的线上形态：写入请求一个参数都不带
	// （`_msg_field` / `_time_field` 也一并省略，保持字节级一致）。
	DisableStreamFields bool
}

// NewClient 构造 localhost VL 客户端。BaseURL 可注入以便单元测试。
func NewClient(opts ClientOptions) (*Client, error) {
	raw := strings.TrimSpace(opts.BaseURL)
	if raw == "" {
		return nil, fmt.Errorf("vlsup: client baseURL is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("vlsup: parse baseURL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("vlsup: unsupported baseURL scheme %q", u.Scheme)
	}
	host := u.Hostname()
	if !opts.AllowNonLoopback && !IsLoopbackHost(host) {
		return nil, fmt.Errorf("%w: %q", ErrNotLoopback, host)
	}
	hc := opts.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Second, Transport: newTransport()}
	}
	// 查询客户端：与写入分离，避免「扫全天的校验查询」被 5s 写入超时误杀。
	hq := opts.HTTPClient
	if hq == nil {
		qt := opts.QueryTimeout
		if qt <= 0 {
			qt = 60 * time.Second
		}
		hq = &http.Client{Timeout: qt, Transport: newTransport()}
	}
	streamFields := ""
	if !opts.DisableStreamFields {
		streamFields = strings.TrimSpace(opts.StreamFields)
		if streamFields == "" {
			streamFields = DefaultStreamFields
		}
		if err := ValidateStreamFields(streamFields); err != nil {
			// 构造即拒：写错流字段的单向代价太高，不允许拖到第一次写入才爆。
			return nil, err
		}
	}
	return &Client{
		baseURL:      strings.TrimRight(raw, "/"),
		username:     opts.Username,
		password:     opts.Password,
		hc:           hc,
		hq:           hq,
		compress:     !opts.DisableCompression,
		streamFields: streamFields,
	}, nil
}

// InsertQuery 返回 `/insert/jsonline` 要带的 query 参数（单一拼装点）。
//
// 为什么不把这些参数散在 insertOnce 里逐条拼：写入参数是「VL 侧入库语义」的一部分，
// 将来 VL 再推出新参数时只需要改这一个函数，不必在压缩退回重试、错误分类等等路径上
// 各改一遍（insertOnce 有两条调用路径 —— 压缩与未压缩重试）。
//
// 不传 `_stream_fields`（DisableStreamFields）时**一个参数都不发**，包括 `_msg_field` /
// `_time_field`：退回的是与本改动之前**字节级一致**的请求，而不是「少一个参数的近似形态」。
// 这两个字段名一旦带上去，就断言了「我们的行里有 `_msg`/`_time`」；而关掉流标签的场景
// 恰恰包含外部回环 VL（行由别的系统生成），不应由我们把解析口径写死。
func (c *Client) InsertQuery() url.Values {
	q := url.Values{}
	if c == nil || c.streamFields == "" {
		return q
	}
	q.Set("_stream_fields", c.streamFields)
	q.Set("_msg_field", DefaultMsgField)
	q.Set("_time_field", DefaultTimeField)
	return q
}

// StreamFields 返回当前生效的 `_stream_fields` 取值；空串表示未启用流标签。
//
// 供启动日志使用：流标签是一个**会在 VL 里留下永久分区形态**的设置，且启用点之后的
// 数据与之前的数据 `_stream` 不同 —— 运维应当能在启动日志里看到“现在生效的是什么”，
// 而不是只能靠 curl 反推。
func (c *Client) StreamFields() string {
	if c == nil {
		return ""
	}
	return c.streamFields
}

// insertURL 返回本次写入的完整 URL（含 query；无参数时不带 "?"）。
func (c *Client) insertURL() string {
	u := c.baseURL + "/insert/jsonline"
	if q := c.InsertQuery(); len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

// newTransport 构造带连接池的 Transport（写入与查询各一只，互不影响对方的空闲计数）。
//
// 除连接池参数外一律沿用 net/http 的默认值（DialContext 超时、TLS 握手超时、HTTP/2、
// Proxy 等）：本客户端只面向 127.0.0.1 的 VL，默认值是经生产验证的，改它们只会引入
// 新的超时面。
func newTransport() *http.Transport {
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		clone := base.Clone()
		clone.MaxIdleConns = defaultMaxIdleConnsPerHost
		clone.MaxIdleConnsPerHost = defaultMaxIdleConnsPerHost
		clone.IdleConnTimeout = defaultIdleConnTimeout
		return clone
	}
	// 理论上不可达（DefaultTransport 恒为 *http.Transport）；退化为手搭等价配置。
	return &http.Transport{
		MaxIdleConns:        defaultMaxIdleConnsPerHost,
		MaxIdleConnsPerHost: defaultMaxIdleConnsPerHost,
		IdleConnTimeout:     defaultIdleConnTimeout,
	}
}

// BaseURL 返回注入的 base URL。
func (c *Client) BaseURL() string { return c.baseURL }

// IsLoopbackHost 报告 host 是否为回环地址（127.0.0.1 / localhost / ::1）。
func IsLoopbackHost(host string) bool {
	h := strings.TrimSpace(host)
	if h == "" {
		return false
	}
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// Health 请求 /health；成功（2xx）返回 nil，未授权返回 ErrUnauthorized。
func (c *Client) Health(ctx context.Context) error {
	_, err := c.Get(ctx, "/health", nil)
	return err
}

// Get 以 Basic-auth GET baseURL+path，可选 query。响应体非 2xx 时返回错误。
// 401/403 映射为 ErrUnauthorized。
func (c *Client) Get(ctx context.Context, path string, query url.Values) ([]byte, error) {
	if c == nil {
		return nil, fmt.Errorf("vlsup: nil client")
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("vlsup: build request: %w", err)
	}
	if c.username != "" || c.password != "" {
		token := base64.StdEncoding.EncodeToString([]byte(c.username + ":" + c.password))
		req.Header.Set("Authorization", "Basic "+token)
	}
	resp, err := c.hq.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vlsup: request %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w: %s status %d", ErrUnauthorized, path, resp.StatusCode)
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return nil, &StatusError{Path: path, Code: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	return body, nil
}

// Stream 以 Basic-auth GET 请求并把成功响应流交给消费方。
// 查询接口返回 NDJSON，不能先把完整响应读入内存；调用方负责在 consume 中设置
// 行数、字节数和取消边界。非 2xx 响应仍只读取有界错误正文。
func (c *Client) Stream(ctx context.Context, path string, query url.Values, consume func(io.Reader) error) error {
	_, err := c.streamWithBudget(ctx, path, query, 0, consume)
	return err
}

// StreamBudget 在读入响应前限制额度，并在成功、解析错误及 HTTP 错误路径保留实际读入字节。
// 零额度不发请求，避免剩余零被重新解释为默认预算；原 Stream 仍保持既有语义。
func (c *Client) StreamBudget(ctx context.Context, path string, query url.Values, maxBytes uint64, consume func(io.Reader) error) (uint64, error) {
	if maxBytes == 0 {
		return 0, fmt.Errorf("vlsup: stream byte budget exhausted")
	}
	return c.streamWithBudget(ctx, path, query, maxBytes, consume)
}

// streamByteReader 将预读也计入实际消费，不以已解析的行数冒充网络读取量。
type streamByteReader struct {
	reader io.Reader
	ctx    context.Context
	read   uint64
}

func (r *streamByteReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.reader.Read(p)
	r.read += uint64(n)
	return n, err
}

func (c *Client) streamWithBudget(ctx context.Context, path string, query url.Values, maxBytes uint64, consume func(io.Reader) error) (uint64, error) {
	if c == nil {
		return 0, fmt.Errorf("vlsup: nil client")
	}
	if consume == nil {
		return 0, fmt.Errorf("vlsup: nil stream consumer")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, fmt.Errorf("vlsup: build request: %w", err)
	}
	if c.username != "" || c.password != "" {
		token := base64.StdEncoding.EncodeToString([]byte(c.username + ":" + c.password))
		req.Header.Set("Authorization", "Basic "+token)
	}
	httpClient := c.hq
	if maxBytes > 0 {
		// 自动重定向会在消费回调之外预读旧正文；预算入口必须把 3xx 当有界错误处理。
		bounded := *c.hq
		bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		httpClient = &bounded
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("vlsup: request %s: %w", path, err)
	}
	defer resp.Body.Close()
	var body io.Reader = resp.Body
	if maxBytes > 0 {
		limit := maxBytes
		if limit > uint64(1<<63-1) {
			limit = uint64(1<<63 - 1)
		}
		body = io.LimitReader(body, int64(limit))
	}
	counter := &streamByteReader{reader: body, ctx: ctx}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 错误描述也占查询成本；小预算不能为了文案先额外读入 1MiB。
		text, _ := io.ReadAll(io.LimitReader(counter, 1<<20))
		if err := ctx.Err(); err != nil {
			return counter.read, err
		}
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return counter.read, fmt.Errorf("%w: %s status %d", ErrUnauthorized, path, resp.StatusCode)
		default:
			return counter.read, &StatusError{Path: path, Code: resp.StatusCode, Body: strings.TrimSpace(string(text))}
		}
	}
	err = consume(counter)
	if cancelled := ctx.Err(); cancelled != nil {
		err = cancelled
	}
	return counter.read, err
}

// InsertJSONLines 将规范事件以 NDJSON 投递到 Worker 本地 VL。
// 调用方仍需单独记录请求状态：HTTP 2xx 是 VL **接收**的确认（写入账本的唯一依据），
// 不是逐记录耐久性证明。
//
// 请求体默认 gzip 压缩（`Content-Encoding: gzip`）：VL v1.52.0 实测接受该形态并把数据
// 正常入库（用 curl 单独验过：200 且 `/select/logsql/query` 可查回，原文 65090B → 线上 895B）。
// 开关见 ClientOptions.DisableCompression。
//
// **退回不压缩**：若压缩体被 VL 以“看不懂的请求体”类状态码（400 / 415）拒掉，本函数
// 会改用未压缩形态**重试一次**，并根据重试结果判定责任归谁：
//   - 重试成功 ⇒ 确实是压缩兼容性问题 ⇒ 本进程内永久退回不压缩（打 WARN）；
//   - 重试也失败 ⇒ 数据本身有问题（比如工端发来了一行非法 JSON）⇒ **不动**压缩状态，
//     原样返回该错误，不让一次偶发的脏数据把压缩永久关掉。
//
// 为什么需要这个退回：传输层优化不该成为采集失败的单点。VL 侧解压支持一旦因改版/改配
// 消失，采集应该继续跑（以未压缩形态），而不是全线停摆。
// 重试是否安全：非 2xx 意味着 VL 未接收该批；即使有部分接收，重发幂等由 event_id 承担。
func (c *Client) InsertJSONLines(ctx context.Context, lines []byte) (int, error) {
	if c == nil {
		return 0, fmt.Errorf("vlsup: nil client")
	}
	body, compressed := lines, false
	if c.compressionEnabled() && len(lines) > 0 {
		if encoded, err := gzipPayload(lines); err == nil {
			body, compressed = encoded, true
		}
		// 压缩失败不是投递失败：退回不压缩形态（传输层优化不得成为采集失败的单点）。
	}
	status, err := c.insertOnce(ctx, body, compressed)
	if err == nil || !compressed || !isEncodingRejected(status) {
		return status, err
	}
	plainStatus, plainErr := c.insertOnce(ctx, lines, false)
	if plainErr == nil {
		c.compressionRejected.Store(true)
		slog.Warn("VL 不接受 gzip 压缩的写入请求体，本进程内已退回不压缩形态",
			"path", "/insert/jsonline", "gzipStatus", status,
			"hint", "如需再次开启，重启 Worker 或检查 VL 版本/压缩支持")
	}
	return plainStatus, plainErr
}

// compressionEnabled 返回当前是否应当压缩：开关打开且尚未被 VL 实测拒绝。
func (c *Client) compressionEnabled() bool {
	return c.compress && !c.compressionRejected.Load()
}

// isEncodingRejected 判定“压缩体被拒”的候选状态码。
//
// 400 是 VL 对不可解析请求体的通用回应（含“这堆字节不是合法 NDJSON” —— 压缩体未解就会
// 长这样）；415 是标准的 Unsupported Media Type。两者都会诱使调用方把“压了一个能解释的错”
// 误当作“数据有问题”去判定，所以必须先排除。
func isEncodingRejected(status int) bool {
	return status == http.StatusBadRequest || status == http.StatusUnsupportedMediaType
}

// insertOnce 发一次写入请求。compressed 为 true 时声明 Content-Encoding: gzip。
func (c *Client) insertOnce(ctx context.Context, body []byte, compressed bool) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.insertURL(), bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("vlsup: build insert request: %w", err)
	}
	req.Header.Set("Content-Type", "application/stream+json")
	if compressed {
		req.Header.Set("Content-Encoding", "gzip")
	}
	if c.username != "" || c.password != "" {
		token := base64.StdEncoding.EncodeToString([]byte(c.username + ":" + c.password))
		req.Header.Set("Authorization", "Basic "+token)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, fmt.Errorf("vlsup: insert request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return resp.StatusCode, fmt.Errorf("%w: insert status %d", ErrUnauthorized, resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("vlsup: insert status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return resp.StatusCode, nil
}

// gzipPayload 返回 lines 的 gzip 压缩形态（NDJSON 重复字段多，压率极高）。
func gzipPayload(lines []byte) ([]byte, error) {
	var buf bytes.Buffer
	// 预分配：压缩后通常不到原文的 1/10，按 1/8 给初始容量，避免多次扩容。
	buf.Grow(len(lines)/8 + 64)
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(lines); err != nil {
		_ = zw.Close()
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Post calls a localhost management endpoint with Basic auth and returns a
// bounded response. Partition endpoints in VictoriaLogs v1.52.0 require POST.
func (c *Client) Post(ctx context.Context, path string, query url.Values) ([]byte, error) {
	if c == nil {
		return nil, fmt.Errorf("vlsup: nil client")
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return nil, fmt.Errorf("vlsup: build POST request: %w", err)
	}
	if c.username != "" || c.password != "" {
		token := base64.StdEncoding.EncodeToString([]byte(c.username + ":" + c.password))
		req.Header.Set("Authorization", "Basic "+token)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vlsup: POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20+1))
	if readErr != nil {
		return nil, readErr
	}
	if len(body) > 4<<20 {
		return nil, fmt.Errorf("vlsup: POST %s response exceeds limit", path)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("%w: %s status %d", ErrUnauthorized, path, resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("vlsup: POST %s status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

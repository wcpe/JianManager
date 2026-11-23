package vlsup

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// 流标签（`_stream_fields`）接入回归。
//
// 背景：VictoriaLogs 的 stream 是**分区键**，同一 stream 内的行共享元数据与索引。
// 改前我们的 `/insert/jsonline` 请求 URL 上一个 query 参数都没有 ⇒ VL 完全不分局，
// `_stream` 只有一个（实测 `/select/logsql/field_values?field=_stream` 返回空）。
//
// 本文件的四条用例分工：
//
//  1. TestInsertJSONLinesSendsStreamFieldsOnURL —— 线上形态：URL 真的带了三个参数；
//  2. TestInsertURLKeepsDefaultStreamFieldsExact —— 默认取值就是架构裁定的那两个字段（变异锚点）；
//  3. TestInsertJSONLinesStreamFieldsDisabledOmitsAllParams —— 开关真的能退回“一个参数都不带”；
//  4. TestNewClientRejectsUnboundedStreamFields —— 无界基数字段被构造即拒。
//
// 转红方式（变异自检）：把 DefaultStreamFields 改成包含 event_id（例如
// "log_source_id,stream,event_id"）⇒ 第 2 条断言红、第 1 条因 NewClient 报错也红。

// streamProbe 只观测写入请求的 URL 形态（路径与 query），不关心请求体。
//
// 刻意与 insertProbe 分开：那个夹具关心压缩，这个关心分区键；合在一起会让
// “某个断言红了到底是谁的锅”变得模糊。
type streamProbe struct {
	mu       sync.Mutex
	path     string
	rawQuery string
	query    url.Values
	fullURL  string
	calls    int
}

func (p *streamProbe) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	p.mu.Lock()
	p.path = r.URL.Path
	p.rawQuery = r.URL.RawQuery
	p.query = r.URL.Query()
	p.fullURL = r.URL.String()
	p.calls++
	p.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

// 线上形态：必须带 `_stream_fields=log_source_id,stream`，以及显式的 `_msg_field` / `_time_field`。
func TestInsertJSONLinesSendsStreamFieldsOnURL(t *testing.T) {
	probe := &streamProbe{}
	srv := httptest.NewServer(probe)
	defer srv.Close()

	client, err := NewClient(ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)

	status, err := client.InsertJSONLines(context.Background(), ndjsonBatch(2))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)

	probe.mu.Lock()
	defer probe.mu.Unlock()
	require.Equal(t, 1, probe.calls)
	require.Equal(t, "/insert/jsonline", probe.path, "路径不能因为加参数而变")

	// 关键断言：解码后的取值。
	require.Equal(t, "log_source_id,stream", probe.query.Get("_stream_fields"),
		"写入请求必须声明流标签；缺失时 VL 会把所有行归到同一个 stream")
	require.Equal(t, "_msg", probe.query.Get("_msg_field"))
	require.Equal(t, "_time", probe.query.Get("_time_field"))

	// 形态断言：这三个参数**真的在 URL 上**（而不是只存在于某个中间变量）。
	// 用 RawQuery 而不是 query，是为了证明它真的可作为 URL 一部分被 VL 读到。
	require.Contains(t, probe.rawQuery, "_stream_fields=")
	require.Contains(t, probe.fullURL, "/insert/jsonline?")

	// 反向断言：绝不允许把无界字段带上线。
	for _, bad := range []string{"event_id", "record_start", "record_end", "ingest_time_utc"} {
		require.NotContains(t, probe.query.Get("_stream_fields"), bad,
			"无界基数字段 %q 不得出现在 _stream_fields 中", bad)
	}

	t.Logf("写入 URL 实测：%s", probe.fullURL)
}

// 默认取值就是架构裁定的那两个字段，且总流数上界可推导（19 × 3 ≈ 57）。
//
// 这条用例是**变异锚点**：改 DefaultStreamFields 为任何别的取值都会红。
func TestInsertURLKeepsDefaultStreamFieldsExact(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client, err := NewClient(ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)

	require.Equal(t, "log_source_id,stream", DefaultStreamFields)
	q := client.InsertQuery()
	require.Equal(t, "log_source_id,stream", q.Get("_stream_fields"))

	// 每个入流字段都必须是**小基数**、且不在禁用表里。
	fields := strings.Split(q.Get("_stream_fields"), ",")
	require.Len(t, fields, 2, "改字段数会改变总流数上界（当前 ≈ 19×3）；改之前请先回答基数问题")
	for _, f := range fields {
		require.Contains(t, []string{"log_source_id", "stream"}, f,
			"入流字段 %q 未列入架构裁定", f)
		require.NotContains(t, forbiddenStreamFields, f, "入流字段 %q 在禁用表里（基数无界）", f)
	}

	// level 是刻意的“不入流”字段：它会随内容变化，放进去会让同一源不断切出新分区。
	require.NotContains(t, fields, "level",
		"level 不得入流：同一源今天全 INFO、明天冒 WARN 就会切出新分区")

	require.Equal(t, srv.URL+"/insert/jsonline?_msg_field=_msg&_stream_fields=log_source_id%2Cstream&_time_field=_time",
		client.insertURL(), "完整写入 URL 形态（参数按 key 排序；逗号经 QueryEscape 成 %%2C）")
	t.Logf("完整写入 URL：%s", client.insertURL())
}

// 开关退回：DisableStreamFields ⇒ 请求 URL **一个参数都不带**，与本改动之前的线上形态逐字一致。
//
// 这里连 `_msg_field` / `_time_field` 也要求不发：关掉流标签的场景包含外部回环 VL
// （行由别的系统生成），不应由我们把解析口径写死到一个别人不保证写了的字段名上。
func TestInsertJSONLinesStreamFieldsDisabledOmitsAllParams(t *testing.T) {
	probe := &streamProbe{}
	srv := httptest.NewServer(probe)
	defer srv.Close()

	client, err := NewClient(ClientOptions{BaseURL: srv.URL, DisableStreamFields: true})
	require.NoError(t, err)

	status, err := client.InsertJSONLines(context.Background(), ndjsonBatch(2))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)

	probe.mu.Lock()
	defer probe.mu.Unlock()
	require.Equal(t, "/insert/jsonline", probe.path)
	require.Empty(t, probe.rawQuery, "关掉流标签时不得带任何 query 参数（退回改动前的字节级形态）")
	require.Empty(t, client.InsertQuery())
	require.Equal(t, srv.URL+"/insert/jsonline", client.insertURL())
}

// 显式覆盖：参数值来自构造期配置而不是硬编码，否则“可关掉/可调整”只是幻觉。
func TestInsertStreamFieldsOverrideReachesURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client, err := NewClient(ClientOptions{BaseURL: srv.URL, StreamFields: "log_source_id"})
	require.NoError(t, err)
	require.Equal(t, "log_source_id", client.InsertQuery().Get("_stream_fields"))
	require.Contains(t, client.insertURL(), "_stream_fields=log_source_id&")
}

// 无界基数字段入流 ⇒ **构造即拒**（fail fast），错误文案要能直接贴给运维看。
//
// 为什么必须拒而不是告警：这个错误单向不可逆——写进 VL 的 stream 元数据只能靠删分区回收，
// 而且症状（内存/元数据膨胀）要等写入量上来才显形，事后无法靠查询侧补救。
func TestNewClientRejectsUnboundedStreamFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	for _, bad := range []string{
		"event_id",
		"log_source_id,event_id",
		"record_start",
		"record_end",
		"ingest_time_utc",
		"canonical_content_hash",
		"_msg",
		"_time",
		"log_source_id,,stream", // 空字段名同样是错配，不静默跳过
	} {
		_, err := NewClient(ClientOptions{BaseURL: srv.URL, StreamFields: bad})
		require.Error(t, err, "StreamFields=%q 必须被拒绝", bad)
		require.Contains(t, err.Error(), "_stream_fields", "错误文案要点名配置项：%v", err)
	}

	// 具体到 event_id：文案必须说出“为什么不行”，否则运维只会想着绕过去。
	_, err := NewClient(ClientOptions{BaseURL: srv.URL, StreamFields: "log_source_id,event_id"})
	require.ErrorContains(t, err, "event_id")
	require.ErrorContains(t, err, "每行一个新 stream")

	// 合法取值仍然通过（否则上面的“全拒”可能只是某些别的错）。
	_, err = NewClient(ClientOptions{BaseURL: srv.URL, StreamFields: "log_source_id,stream,level"})
	require.NoError(t, err, "level 是小基数字段：不推荐入流（会随内容漂移），但不是“绝对禁止”级别")

	// DisableStreamFields 时不再校验取值（反正一个参数都不发）。
	_, err = NewClient(ClientOptions{BaseURL: srv.URL, DisableStreamFields: true, StreamFields: "event_id"})
	require.NoError(t, err)
}

// Supervisor 层的开关透传：写入客户端一律经 ClientFor 得到（采集侧写路由的终点），
// 所以配置开关必须能从 Options 走到实际发出请求的 Client 上。
//
// 本用例**不注入** HealthClient：那会绕过被测的默认构造路径，让“透传”变成自证。
// ClientFor 只构造客户端不发请求，所以不需要真的把 VL 跑起来。
//
// 反例（变异）：把 Options.DisableStreamFields 从默认 newClient 的 ClientOptions 里删掉
// ⇒ 本条第二段断言红。
func TestSupervisorForwardsStreamFieldsSwitchToClient(t *testing.T) {
	bin, sum := writeFakeBinary(t)
	base := Options{
		BinaryPath: bin, AssetSHA256: sum, DataRoot: t.TempDir(),
		AuthUsername: "jm", AuthPassword: "local-only",
		Factory: &fakeFactory{}, Sink: &RecordingSink{},
	}

	s, err := New(base)
	require.NoError(t, err)
	on, err := s.ClientFor(NamespaceHot)
	require.NoError(t, err)
	require.Equal(t, DefaultStreamFields, on.InsertQuery().Get("_stream_fields"),
		"默认路径必须让写入客户端带上流标签")

	base.DisableStreamFields = true
	sOff, err := New(base)
	require.NoError(t, err)
	off, err := sOff.ClientFor(NamespaceHot)
	require.NoError(t, err)
	require.Empty(t, off.InsertQuery(), "Options.DisableStreamFields 必须透传到实际写入客户端")
}

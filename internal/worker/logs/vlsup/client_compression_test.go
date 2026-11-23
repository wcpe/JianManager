package vlsup

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// insertProbe 是一个只观测 `/insert/jsonline` 请求形态的夹具：记录是否声明了
// `Content-Encoding: gzip` 以及**实际收到**的请求体字节数（与解压后字节数）。
type insertProbe struct {
	mu             sync.Mutex
	encoding       string
	wireBytes      int
	decodedBytes   int
	decodedPayload string
	decodeErr      error
}

func (p *insertProbe) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/insert/jsonline" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	raw, err := io.ReadAll(r.Body)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.encoding = r.Header.Get("Content-Encoding")
	p.wireBytes = len(raw)
	if err != nil {
		p.decodeErr = err
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	decoded := raw
	if p.encoding == "gzip" {
		zr, gzErr := gzip.NewReader(bytes.NewReader(raw))
		if gzErr != nil {
			p.decodeErr = gzErr
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		decoded, p.decodeErr = io.ReadAll(zr)
		_ = zr.Close()
	}
	p.decodedBytes = len(decoded)
	p.decodedPayload = string(decoded)
	w.WriteHeader(http.StatusOK)
}

// ndjsonBatch 造一批**真实形状**的投影行：同一批里 log_source_id /
// projection_generation / parser_version 逐行重复，这就是压缩能挤出空间的来源。
func ndjsonBatch(n int) []byte {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString(`{"_time":"2026-10-30T11:30:00Z","_msg":"stream line `)
		b.WriteString(strings.Repeat("x", 200))
		b.WriteString(`","event_id":"evt-`)
		b.WriteString(strings.Repeat("0", 4))
		b.WriteString(`","log_source_id":"inst:220/file","source_generation":"gen-1",`)
		b.WriteString(`"parser_version":"worker-log-parser/v1","projection_generation":"projection-7",`)
		b.WriteString(`"canonical_content_hash":"deadbeef","level":"INFO","stream":"stdout"}`)
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

// 回归（变异可证伪）：写入请求体默认 gzip 压缩，且 **VL 侧解出来的内容与输入逐字节相等**。
//
// 反例：把 `compress` 置回 false（或用 DisableCompression 开关）⇒ 断言 `Content-Encoding`
// 与请求体体积的那两行立刻红（见 TestInsertJSONLinesCompressionDisabledKeepsWireFormat）。
func TestInsertJSONLinesCompressesBodyWithGzip(t *testing.T) {
	probe := &insertProbe{}
	srv := httptest.NewServer(probe)
	defer srv.Close()

	client, err := NewClient(ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)

	payload := ndjsonBatch(200)
	status, err := client.InsertJSONLines(context.Background(), payload)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)

	probe.mu.Lock()
	defer probe.mu.Unlock()
	require.NoError(t, probe.decodeErr)
	require.Equal(t, "gzip", probe.encoding, "默认必须声明 Content-Encoding: gzip")
	require.Equal(t, string(payload), probe.decodedPayload, "解压后必须与投递内容逐字节相等")
	require.Greater(t, probe.decodedBytes, probe.wireBytes, "压缩后线上字节必须**小于**原文")
	// 这条断言是「压缩真的生效」的体量证据：重复字段极多的 NDJSON 批至少能压到一半以下。
	require.Less(t, probe.wireBytes, len(payload)/2,
		"NDJSON 批的压缩率必须真实生效（实测约 %d→%d 字节）", len(payload), probe.wireBytes)
	t.Logf("压缩实测：原文 %d 字节 → 线上 %d 字节（%.1f×）",
		len(payload), probe.wireBytes, float64(len(payload))/float64(probe.wireBytes))
}

// 开关退回：DisableCompression ⇒ 不发 gzip、请求体与输入逐字节相等。
//
// 它是上一条用例的**反向对照**：两条一起跑，才能证明「压缩与否」真的是开关控制的，
// 而不是夹具恰好怎么摆。
func TestInsertJSONLinesCompressionDisabledKeepsWireFormat(t *testing.T) {
	probe := &insertProbe{}
	srv := httptest.NewServer(probe)
	defer srv.Close()

	client, err := NewClient(ClientOptions{BaseURL: srv.URL, DisableCompression: true})
	require.NoError(t, err)

	payload := ndjsonBatch(200)
	status, err := client.InsertJSONLines(context.Background(), payload)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)

	probe.mu.Lock()
	defer probe.mu.Unlock()
	require.Empty(t, probe.encoding, "关掉压缩时不得声明 Content-Encoding")
	require.Equal(t, len(payload), probe.wireBytes, "关掉压缩时线上字节必须等于原文")
	require.Equal(t, string(payload), probe.decodedPayload)
}

// 空批不压缩也不报错：调用方偶尔会投递空切片（如全部被幂等过滤掉），
// 此时压缩一个空体毫无意义，也不能因为「没压缩」就把请求头标错。
func TestInsertJSONLinesSkipsCompressionForEmptyPayload(t *testing.T) {
	var gotEncoding string
	var gotLen int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotEncoding = r.Header.Get("Content-Encoding")
		gotLen = len(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client, err := NewClient(ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)
	status, err := client.InsertJSONLines(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Empty(t, gotEncoding)
	require.Zero(t, gotLen)
}

// 连接池：写入与查询两只 client 都必须挂上**带池**的 Transport，
// 且池容量按「并发投递的源数量级」给到 32（默认 Transport 只有 2，等于把并发压回排队）。
//
// 反例（变异）：把 `Transport: newTransport()` 删掉 ⇒ http.Client 退回 nil Transport
// 即 http.DefaultTransport（MaxIdleConnsPerHost=2）⇒ 本用例立刻红。
func TestClientUsesConnectionPooledTransport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client, err := NewClient(ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)

	for name, hc := range map[string]*http.Client{"write": client.hc, "query": client.hq} {
		transport, ok := hc.Transport.(*http.Transport)
		require.Truef(t, ok, "%s 客户端必须使用自建 Transport（当前是 %T）", name, hc.Transport)
		require.Equalf(t, defaultMaxIdleConnsPerHost, transport.MaxIdleConnsPerHost,
			"%s 客户端的 MaxIdleConnsPerHost 必须按并发源数量级配置", name)
		require.Equalf(t, defaultMaxIdleConnsPerHost, transport.MaxIdleConns, "%s 的 MaxIdleConns", name)
		require.Equalf(t, defaultIdleConnTimeout, transport.IdleConnTimeout, "%s 的 IdleConnTimeout", name)
	}
	// 两只客户端不得共用同一个 *http.Transport：否则写入的连接池名额会被查询占住。
	require.NotSame(t, client.hc.Transport, client.hq.Transport,
		"写入与查询必须各持一只 Transport（否则空闲连接计数互相挤占）")

	// 连接复用是真效果而不是配置摆设：连续两次请求必须落在同一条 TCP 连接上。
	var conns []string
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conns = append(conns, r.RemoteAddr)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv2.Close()
	pooled, err := NewClient(ClientOptions{BaseURL: srv2.URL})
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		require.NoError(t, pooled.Health(context.Background()))
	}
	require.Len(t, conns, 2)
	require.Equal(t, conns[0], conns[1], "第二次请求必须复用第一条空闲连接")
}

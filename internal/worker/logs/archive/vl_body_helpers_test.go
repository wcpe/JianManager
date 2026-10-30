package archive

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"strings"
)

// vlInsertBody 返回一次 VL 写入请求的**解压后**正文。
//
// 为什么夹具必须走它（2026-10-30）：vlsup 客户端默认对 `/insert/jsonline` 请求体做 gzip
// 并声明 `Content-Encoding: gzip`。这些夹具同时扮演两个角色：把我们写进去的字节存下来，
// 再原样回放给 `/select/logsql/query` 来模拟“物理行确实可见”。若不解压，回放出去的就是
// 压缩字节、客户端解不开 ⇒「重水化后可见」类断言全部静默失效。
func vlInsertBody(r *http.Request) []byte {
	if r == nil || r.Body == nil {
		return nil
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(r.Header.Get("Content-Encoding")), "gzip") {
		return raw
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil
	}
	defer func() { _ = zr.Close() }()
	decoded, _ := io.ReadAll(zr)
	return decoded
}

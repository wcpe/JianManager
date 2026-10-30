package ingest

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"strings"
)

// vlInsertRequestBody 返回一次 VL 写入请求的**解压后**正文。
//
// 为什么夹具必须走它（2026-10-30）：vlsup 客户端默认对 `/insert/jsonline` 请求体做 gzip 并
// 声明 `Content-Encoding: gzip`（生产 VL v1.52.0 实测接受，见 vlsup 的压缩回归）。夹具若仍
// 直接 `io.ReadAll(r.Body)`，拿到的是压缩字节：
//   - 按行解析的夹具 ⇒ 行数恒为 0；
//   - 把 body 回放给 `/select/logsql/query` 的夹具 ⇒ 查询返回压缩字节、客户端解不开；
//   - 按子串判定代次/事件 id 的夹具 ⇒ 判定恒不命中。
//
// 危险的是这些不是**变红**，而是变成毫无判别力的“恒空格”，会静默拿掉整套断言的含量。
func vlInsertRequestBody(r *http.Request) ([]byte, error) {
	if r == nil || r.Body == nil {
		return nil, nil
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(strings.TrimSpace(r.Header.Get("Content-Encoding")), "gzip") {
		return raw, nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer func() { _ = zr.Close() }()
	return io.ReadAll(zr)
}

// vlRewindInsertBody 把已读到的（解压后）正文重新挂回请求体，供后续 handler 再读。
// 同时清掉 Content-Encoding：夹具层看到的已是解压后的字节，不能让下游二次解压。
func vlRewindInsertBody(r *http.Request, body []byte) {
	if r == nil {
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.Header.Del("Content-Encoding")
}

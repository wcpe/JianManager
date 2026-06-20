package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// webdavBackend 通过标准 HTTP（PUT/GET/DELETE + MKCOL 建目录）对接 WebDAV 服务。
// Endpoint 为 WebDAV 基地址（如 https://dav.example.com/backups）。
type webdavBackend struct {
	base   string // 规整后的基地址，无尾随「/」
	user   string
	pass   string
	client *http.Client
}

func newWebDAVBackend(cfg Config) (Backend, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/")
	if base == "" {
		return nil, fmt.Errorf("WebDAV 缺少 endpoint")
	}
	return &webdavBackend{base: base, user: cfg.AccessKey, pass: cfg.SecretKey, client: defaultHTTPClient()}, nil
}

// url 拼接基地址与对象键。
func (w *webdavBackend) url(key string) string {
	return w.base + "/" + strings.TrimLeft(key, "/")
}

func (w *webdavBackend) auth(req *http.Request) {
	if w.user != "" || w.pass != "" {
		req.SetBasicAuth(w.user, w.pass)
	}
}

// Upload 先尽力为对象键的各级父目录建 MKCOL（已存在的目录返回非 2xx 被忽略），再 PUT 内容。
func (w *webdavBackend) Upload(ctx context.Context, key string, r io.Reader, size int64) error {
	w.ensureDirs(ctx, key)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, w.url(key), r)
	if err != nil {
		return err
	}
	req.ContentLength = size
	w.auth(req)
	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer drainClose(resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("WebDAV 上传失败: HTTP %d", resp.StatusCode)
	}
	return nil
}

func (w *webdavBackend) Download(ctx context.Context, key string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.url(key), nil)
	if err != nil {
		return nil, err
	}
	w.auth(req)
	resp, err := w.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		drainClose(resp.Body)
		return nil, fmt.Errorf("WebDAV 下载失败: HTTP %d", resp.StatusCode)
	}
	return resp.Body, nil
}

func (w *webdavBackend) Delete(ctx context.Context, key string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, w.url(key), nil)
	if err != nil {
		return err
	}
	w.auth(req)
	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer drainClose(resp.Body)
	// 404 视为已删除（幂等）。
	if resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("WebDAV 删除失败: HTTP %d", resp.StatusCode)
	}
	return nil
}

// ensureDirs 为 key 的各级父目录尽力建 MKCOL；失败不阻断（多数服务器对已存在目录返回 405）。
func (w *webdavBackend) ensureDirs(ctx context.Context, key string) {
	segs := strings.Split(strings.Trim(key, "/"), "/")
	if len(segs) <= 1 {
		return
	}
	cur := ""
	for _, s := range segs[:len(segs)-1] {
		if s == "" {
			continue
		}
		cur += s + "/"
		req, err := http.NewRequestWithContext(ctx, "MKCOL", w.url(cur), nil)
		if err != nil {
			continue
		}
		w.auth(req)
		if resp, derr := w.client.Do(req); derr == nil {
			drainClose(resp.Body)
		}
	}
}

// drainClose 读尽并关闭响应体，便于连接复用。
func drainClose(rc io.ReadCloser) {
	if rc == nil {
		return
	}
	_, _ = io.Copy(io.Discard, rc)
	_ = rc.Close()
}

// bytesReader 便于测试时把内容包成 ReadCloser。
func bytesReader(b []byte) io.ReadCloser { return io.NopCloser(bytes.NewReader(b)) }

package storage

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// s3Backend 通过 AWS Signature V4 对接 S3 兼容对象存储（AWS S3 / MinIO / 阿里 OSS 等）。
// 仅依赖标准库实现签名与 path-style 寻址，不引入 SDK，便于在受限网络下构建。
type s3Backend struct {
	endpoint  string // 主机[:端口]，无 scheme
	scheme    string // http | https
	bucket    string
	region    string
	accessKey string
	secretKey string
	client    *http.Client
}

func newS3Backend(cfg Config) (Backend, error) {
	if strings.TrimSpace(cfg.Bucket) == "" {
		return nil, fmt.Errorf("S3 缺少 bucket")
	}
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return nil, fmt.Errorf("S3 缺少 endpoint")
	}
	scheme := "https"
	if !cfg.UseSSL {
		scheme = "http"
	}
	// endpoint 容许带 scheme，统一剥离。
	ep := strings.TrimSpace(cfg.Endpoint)
	ep = strings.TrimPrefix(strings.TrimPrefix(ep, "https://"), "http://")
	ep = strings.TrimRight(ep, "/")
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}
	return &s3Backend{
		endpoint:  ep,
		scheme:    scheme,
		bucket:    cfg.Bucket,
		region:    region,
		accessKey: cfg.AccessKey,
		secretKey: cfg.SecretKey,
		client:    defaultHTTPClient(),
	}, nil
}

// objURL 构造 path-style 对象 URL：<scheme>://<endpoint>/<bucket>/<key>。
func (s *s3Backend) objURL(key string) string {
	return fmt.Sprintf("%s://%s/%s/%s", s.scheme, s.endpoint, s.bucket, s3EscapeKey(key))
}

func (s *s3Backend) Upload(ctx context.Context, key string, r io.Reader, size int64) error {
	// SigV4 需对 payload 取哈希；为保持流式且避免缓冲大文件，使用 UNSIGNED-PAYLOAD。
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, s.objURL(key), r)
	if err != nil {
		return err
	}
	req.ContentLength = size
	if err := s.sign(req, unsignedPayload); err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer drainClose(resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("S3 上传失败: HTTP %d", resp.StatusCode)
	}
	return nil
}

func (s *s3Backend) Download(ctx context.Context, key string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.objURL(key), nil)
	if err != nil {
		return nil, err
	}
	if err := s.sign(req, emptyPayloadHash); err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		drainClose(resp.Body)
		return nil, fmt.Errorf("S3 下载失败: HTTP %d", resp.StatusCode)
	}
	return resp.Body, nil
}

func (s *s3Backend) Delete(ctx context.Context, key string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, s.objURL(key), nil)
	if err != nil {
		return err
	}
	if err := s.sign(req, emptyPayloadHash); err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer drainClose(resp.Body)
	// S3 DELETE 对不存在对象返回 204（幂等），无需特判 404。
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("S3 删除失败: HTTP %d", resp.StatusCode)
	}
	return nil
}

const (
	// unsignedPayload 让 S3 跳过对请求体的哈希校验，支持流式上传不缓冲。
	unsignedPayload = "UNSIGNED-PAYLOAD"
	// emptyPayloadHash 是空请求体的 SHA256（GET/DELETE 用）。
	emptyPayloadHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

// sign 按 AWS Signature V4 给请求加 Authorization 头。payloadHash 为请求体哈希或 UNSIGNED-PAYLOAD。
func (s *s3Backend) sign(req *http.Request, payloadHash string) error {
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	req.Header.Set("Host", req.URL.Host)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)

	// 规范请求：method\ncanonicalURI\ncanonicalQuery\ncanonicalHeaders\nsignedHeaders\npayloadHash
	canonicalURI := req.URL.EscapedPath()
	if canonicalURI == "" {
		canonicalURI = "/"
	}
	canonicalQuery := canonicalizeQuery(req.URL.Query())
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	canonicalHeaders := fmt.Sprintf("host:%s\nx-amz-content-sha256:%s\nx-amz-date:%s\n",
		req.URL.Host, payloadHash, amzDate)
	canonicalRequest := strings.Join([]string{
		req.Method, canonicalURI, canonicalQuery, canonicalHeaders, signedHeaders, payloadHash,
	}, "\n")

	scope := strings.Join([]string{dateStamp, s.region, "s3", "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256", amzDate, scope, hashHex([]byte(canonicalRequest)),
	}, "\n")

	signingKey := s.deriveSigningKey(dateStamp)
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	auth := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		s.accessKey, scope, signedHeaders, signature)
	req.Header.Set("Authorization", auth)
	return nil
}

func (s *s3Backend) deriveSigningKey(dateStamp string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+s.secretKey), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(s.region))
	kService := hmacSHA256(kRegion, []byte("s3"))
	return hmacSHA256(kService, []byte("aws4_request"))
}

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

func hashHex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// s3EscapeKey 对对象键按 S3 规则转义（保留「/」分隔层级）。
func s3EscapeKey(key string) string {
	segs := strings.Split(key, "/")
	for i, s := range segs {
		segs[i] = awsURIEncode(s)
	}
	return strings.Join(segs, "/")
}

// canonicalizeQuery 生成 SigV4 规范查询串（按 key 排序、AWS 风格编码）。
func canonicalizeQuery(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	// 简单插入排序，避免引入 sort 仅为少量参数（备份场景查询通常为空）。
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	var b strings.Builder
	first := true
	for _, k := range keys {
		for _, v := range q[k] {
			if !first {
				b.WriteByte('&')
			}
			b.WriteString(awsURIEncode(k))
			b.WriteByte('=')
			b.WriteString(awsURIEncode(v))
			first = false
		}
	}
	return b.String()
}

// awsURIEncode 按 AWS SigV4 要求做百分号编码（不编码 A-Za-z0-9-_.~）。
func awsURIEncode(s string) string {
	const unreserved = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.~"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if strings.IndexByte(unreserved, c) >= 0 {
			b.WriteByte(c)
		} else {
			b.WriteString(fmt.Sprintf("%%%02X", c))
		}
	}
	return b.String()
}

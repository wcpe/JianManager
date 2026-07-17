package blobstore

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// S3Config S3 兼容对象存储适配器配置（AWS S3 / MinIO / rustfs 等）。
type S3Config struct {
	// Endpoint 主机[:端口]，容带 scheme（自动剥离）。
	Endpoint string
	// Bucket 桶名（必填）。
	Bucket string
	// Region SigV4 region，缺省 us-east-1。
	Region string
	// Prefix 对象键前缀（可空）；真实对象键 = <Prefix>/<key>。
	Prefix string
	// AccessKey / SecretKey 明文凭证（由 service 层从可逆加密列解密注入）。
	AccessKey string
	SecretKey string
	// UseSSL 走 https；false 走 http（rustfs/MinIO 内网常态）。
	UseSSL bool
	// HTTPClient 出站 client，nil 时用带超时的默认 client。
	HTTPClient *http.Client
}

// s3Store S3 兼容对象存储适配器：纯标准库 SigV4（header 签名）+ query 预签名，
// path-style 寻址。参见 ADR-073 决策 3（CP 侧独立实现，不 import worker 备份域）。
type s3Store struct {
	endpoint  string // host[:port]，无 scheme
	scheme    string // http | https
	bucket    string
	region    string
	prefix    string
	accessKey string
	secretKey string
	client    *http.Client
	// now 可注入时钟，供确定性签名测试；生产恒 time.Now。
	now func() time.Time
}

// NewS3 创建 S3 适配器。bucket/endpoint 必填。
func NewS3(cfg S3Config) (Store, error) {
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
	ep := strings.TrimSpace(cfg.Endpoint)
	ep = strings.TrimPrefix(strings.TrimPrefix(ep, "https://"), "http://")
	ep = strings.TrimRight(ep, "/")
	region := strings.TrimSpace(cfg.Region)
	if region == "" {
		region = "us-east-1"
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	return &s3Store{
		endpoint:  ep,
		scheme:    scheme,
		bucket:    cfg.Bucket,
		region:    region,
		prefix:    strings.Trim(strings.TrimSpace(cfg.Prefix), "/"),
		accessKey: cfg.AccessKey,
		secretKey: cfg.SecretKey,
		client:    client,
		now:       time.Now,
	}, nil
}

func (s *s3Store) Kind() string { return KindS3 }

// fullKey 拼渠道前缀后的真实对象键。
func (s *s3Store) fullKey(key string) string {
	key = strings.TrimLeft(key, "/")
	if s.prefix == "" {
		return key
	}
	return s.prefix + "/" + key
}

// objURL 构造 path-style 对象 URL：<scheme>://<endpoint>/<bucket>/<fullKey>。
func (s *s3Store) objURL(key string) string {
	return fmt.Sprintf("%s://%s/%s/%s", s.scheme, s.endpoint, s.bucket, s3EscapeKey(s.fullKey(key)))
}

func (s *s3Store) PutFile(ctx context.Context, key, srcPath string, size int64) error {
	f, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("打开待上传文件失败: %w", err)
	}
	defer f.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, s.objURL(key), f)
	if err != nil {
		return err
	}
	req.ContentLength = size
	// SigV4 需对 payload 取哈希；为流式上传不缓冲大文件，用 UNSIGNED-PAYLOAD（同 worker 备份域策略）。
	signV4(req, unsignedPayload, s.accessKey, s.secretKey, s.region, s.now().UTC())
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("S3 上传失败: %w", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("S3 上传失败: HTTP %d", resp.StatusCode)
	}
	return nil
}

func (s *s3Store) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.objURL(key), nil)
	if err != nil {
		return nil, err
	}
	signV4(req, emptyPayloadHash, s.accessKey, s.secretKey, s.region, s.now().UTC())
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("S3 下载失败: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		drainClose(resp.Body)
		return nil, ErrBlobNotFound
	}
	if resp.StatusCode/100 != 2 {
		drainClose(resp.Body)
		return nil, fmt.Errorf("S3 下载失败: HTTP %d", resp.StatusCode)
	}
	return resp.Body, nil
}

func (s *s3Store) Stat(ctx context.Context, key string) (*ObjectInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, s.objURL(key), nil)
	if err != nil {
		return nil, err
	}
	signV4(req, emptyPayloadHash, s.accessKey, s.secretKey, s.region, s.now().UTC())
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("S3 HEAD 失败: %w", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrBlobNotFound
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("S3 HEAD 失败: HTTP %d", resp.StatusCode)
	}
	info := &ObjectInfo{Key: key, Size: resp.ContentLength}
	if t, perr := http.ParseTime(resp.Header.Get("Last-Modified")); perr == nil {
		info.ModTime = t
	}
	return info, nil
}

// Delete 幂等删除。S3 DELETE 对不存在对象返回 204，无需特判 404。
func (s *s3Store) Delete(ctx context.Context, key string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, s.objURL(key), nil)
	if err != nil {
		return err
	}
	signV4(req, emptyPayloadHash, s.accessKey, s.secretKey, s.region, s.now().UTC())
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("S3 删除失败: %w", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("S3 删除失败: HTTP %d", resp.StatusCode)
	}
	return nil
}

// listBucketResult ListObjectsV2 响应（只取所需字段）。
type listBucketResult struct {
	Contents []struct {
		Key          string `xml:"Key"`
		Size         int64  `xml:"Size"`
		LastModified string `xml:"LastModified"`
	} `xml:"Contents"`
}

// List 经 ListObjectsV2 枚举 prefix 下对象；返回键剥掉渠道 Prefix（还原 CAS 键视角）。
func (s *s3Store) List(ctx context.Context, prefix string, limit int) ([]ObjectInfo, error) {
	if limit <= 0 {
		limit = 1000
	}
	q := url.Values{}
	q.Set("list-type", "2")
	q.Set("max-keys", strconv.Itoa(limit))
	q.Set("prefix", s.fullKey(prefix))
	listURL := fmt.Sprintf("%s://%s/%s?%s", s.scheme, s.endpoint, s.bucket, q.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
	if err != nil {
		return nil, err
	}
	signV4(req, emptyPayloadHash, s.accessKey, s.secretKey, s.region, s.now().UTC())
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("S3 列举失败: %w", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("S3 列举失败: HTTP %d", resp.StatusCode)
	}
	var result listBucketResult
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&result); err != nil {
		return nil, fmt.Errorf("解析 S3 列举响应失败: %w", err)
	}
	out := make([]ObjectInfo, 0, len(result.Contents))
	for _, c := range result.Contents {
		key := c.Key
		if s.prefix != "" {
			key = strings.TrimPrefix(strings.TrimPrefix(key, s.prefix), "/")
		}
		info := ObjectInfo{Key: key, Size: c.Size}
		if t, perr := time.Parse(time.RFC3339, c.LastModified); perr == nil {
			info.ModTime = t
		}
		out = append(out, info)
	}
	return out, nil
}

// Presign 生成对象的 SigV4 query 预签名 GET URL（302 分发用，见 ADR-073 决策 1）。
func (s *s3Store) Presign(key string, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	canonicalPath := "/" + s.bucket + "/" + s3EscapeKey(s.fullKey(key))
	return presignGetURL(s.scheme, s.endpoint, canonicalPath, s.accessKey, s.secretKey, s.region, s.now().UTC(), ttl), nil
}

const (
	// unsignedPayload 让 S3 跳过对请求体的哈希校验，支持流式上传不缓冲。
	unsignedPayload = "UNSIGNED-PAYLOAD"
	// emptyPayloadHash 空请求体的 SHA256（GET/HEAD/DELETE 用）。
	emptyPayloadHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

// signV4 按 AWS Signature V4 给请求加 Authorization 头（header 签名）。
// 签名头固定 host;x-amz-content-sha256;x-amz-date（对拍向量：AWS 官方 GET Bucket 示例）。
func signV4(req *http.Request, payloadHash, accessKey, secretKey, region string, now time.Time) {
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	req.Header.Set("Host", req.URL.Host)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)

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

	scope := strings.Join([]string{dateStamp, region, "s3", "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256", amzDate, scope, hashHex([]byte(canonicalRequest)),
	}, "\n")

	signature := hex.EncodeToString(hmacSHA256(deriveSigningKey(secretKey, dateStamp, region), []byte(stringToSign)))
	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		accessKey, scope, signedHeaders, signature))
}

// presignGetURL 生成 SigV4 query 参数预签名 GET URL。
// canonicalPath 为已转义的规范路径（path-style：/<bucket>/<escapedKey>）。
// 签名头仅 host、payload 恒 UNSIGNED-PAYLOAD（对拍向量：AWS 官方预签名示例）。
func presignGetURL(scheme, host, canonicalPath, accessKey, secretKey, region string, now time.Time, ttl time.Duration) string {
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	scope := strings.Join([]string{dateStamp, region, "s3", "aws4_request"}, "/")

	params := [][2]string{
		{"X-Amz-Algorithm", "AWS4-HMAC-SHA256"},
		{"X-Amz-Credential", accessKey + "/" + scope},
		{"X-Amz-Date", amzDate},
		{"X-Amz-Expires", strconv.Itoa(int(ttl / time.Second))},
		{"X-Amz-SignedHeaders", "host"},
	}
	var qb strings.Builder
	for i, kv := range params {
		if i > 0 {
			qb.WriteByte('&')
		}
		qb.WriteString(awsURIEncode(kv[0]))
		qb.WriteByte('=')
		qb.WriteString(awsURIEncode(kv[1]))
	}
	canonicalQuery := qb.String()

	canonicalRequest := strings.Join([]string{
		http.MethodGet,
		canonicalPath,
		canonicalQuery,
		"host:" + host + "\n",
		"host",
		unsignedPayload,
	}, "\n")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256", amzDate, scope, hashHex([]byte(canonicalRequest)),
	}, "\n")
	signature := hex.EncodeToString(hmacSHA256(deriveSigningKey(secretKey, dateStamp, region), []byte(stringToSign)))

	return fmt.Sprintf("%s://%s%s?%s&X-Amz-Signature=%s", scheme, host, canonicalPath, canonicalQuery, signature)
}

func deriveSigningKey(secretKey, dateStamp, region string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secretKey), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(region))
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
	for i, seg := range segs {
		segs[i] = awsURIEncode(seg)
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
	sort.Strings(keys)
	var b strings.Builder
	first := true
	for _, k := range keys {
		vals := append([]string(nil), q[k]...)
		sort.Strings(vals)
		for _, v := range vals {
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
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// drainClose 读尽并关闭响应体，保 keep-alive 连接复用。
func drainClose(rc io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(rc, 1<<20))
	_ = rc.Close()
}

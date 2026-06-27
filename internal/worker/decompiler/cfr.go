// Package decompiler 提供 Worker 侧反编译能力：解析/缓存 CFR 反编译器 jar，
// 并经实例/系统 JDK 受控调起 CFR 把 class/jar 反编译为 Java 源码。
//
// 全程只读 + 超时 + 体积上限 + 受控 exec（CFR 静态分析字节码，不加载/运行目标代码、
// 不写工作目录、不联网执行）。参见 ADR-018。
package decompiler

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// CFR 版本与内容指纹（构建期常量，升级 CFR 时同步改这三项）。
// SHA-256 pin 用于校验「按需下载」的内容可信（不信任传输通道，只信内容指纹，
// 与 ADR-022 客户端 manifest 信任模型同构）。
const (
	// CFRVersion 是本 Worker 约定的 CFR 版本。
	CFRVersion = "0.152"
	// cfrSHA256 是 cfr-0.152.jar（Maven Central）的 SHA-256 十六进制指纹。
	cfrSHA256 = "f686e8f3ded377d7bc87d216a90e9e9512df4156e75b06c655a16648ae8765b2"
	// cfrDownloadURL 是 CFR jar 的 Maven Central 下载地址（仅作分发，内容靠 pin 校验）。
	cfrDownloadURL = "https://repo1.maven.org/maven2/org/benf/cfr/" + CFRVersion + "/cfr-" + CFRVersion + ".jar"
)

// EmbeddedJarFunc 返回内嵌 CFR jar 字节（缺失返回 nil）。
// 由调用方注入（worker embed 包），避免本包反向依赖。
type EmbeddedJarFunc func() []byte

// Provider 负责解析出一个可用的 CFR jar 路径并缓存结果。
// 解析优先级（ADR-018 决策 3）：
//  1. 显式配置路径（configPath）；
//  2. 内嵌 jar（embedded，写到缓存目录复用）；
//  3. 数据根缓存（cacheDir/cfr-<ver>.jar）；
//  4. 按需下载（download URL + sha256 pin 校验）。
//
// Provider 可被多 goroutine 并发调用 Resolve；首次解析加锁串行，之后走缓存。
type Provider struct {
	// configPath 是运维显式指定的 CFR jar 路径（最高优先级，可空）。
	configPath string
	// cacheDir 是数据根下的 CFR jar 缓存目录（如 <root>/cache/tools）。
	cacheDir string
	// embedded 返回内嵌 jar 字节（可为 nil 表示未内嵌）。
	embedded EmbeddedJarFunc
	// allowDownload 为 false 时禁用按需下载（仅用配置/内嵌/缓存）。
	allowDownload bool
	// httpClient 用于按需下载（可注入，便于测试）。
	httpClient *http.Client
	// downloadURL 是 CFR jar 下载地址（默认 Maven Central，可注入便于测试）。
	downloadURL string

	mu       sync.Mutex
	resolved string // 已解析出的可用 jar 路径（命中后缓存）
}

// Config 构造 Provider 的参数。
type Config struct {
	// ConfigPath 显式 CFR jar 路径（可空）。
	ConfigPath string
	// CacheDir 数据根下的缓存目录（CFR jar 落地于此，可空则仅用配置/内嵌）。
	CacheDir string
	// Embedded 返回内嵌 jar 字节（可空）。
	Embedded EmbeddedJarFunc
	// AllowDownload 是否允许从 Maven Central 按需下载（默认应开启）。
	AllowDownload bool
	// HTTPClient 按需下载 CFR 所用出站 client（经进程级代理，FR-174/ADR-037）。
	// 为 nil 时回退一个 60s 超时的默认 client（向后兼容；测试也可经此注入 httptest client）。
	HTTPClient *http.Client
}

// NewProvider 创建 CFR jar 解析器。
func NewProvider(cfg Config) *Provider {
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	} else if client.Timeout == 0 {
		// 工厂 client 默认不设整体超时；为 CFR 下载补一个上限（不改原 client）。
		c := *client
		c.Timeout = 60 * time.Second
		client = &c
	}
	return &Provider{
		configPath:    cfg.ConfigPath,
		cacheDir:      cfg.CacheDir,
		embedded:      cfg.Embedded,
		allowDownload: cfg.AllowDownload,
		httpClient:    client,
		downloadURL:   cfrDownloadURL,
	}
}

// cachedJarPath 返回数据根缓存中 CFR jar 的预期路径（cacheDir 为空时返回空串）。
func (p *Provider) cachedJarPath() string {
	if p.cacheDir == "" {
		return ""
	}
	return filepath.Join(p.cacheDir, "cfr-"+CFRVersion+".jar")
}

// Resolve 解析出一个可用的 CFR jar 路径（按优先级），命中后缓存结果。
// 全部来源都不可用时返回错误，调用方据此把反编译降级为「无 CFR」错误。
func (p *Provider) Resolve() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.resolved != "" {
		if fileExists(p.resolved) {
			return p.resolved, nil
		}
		p.resolved = "" // 缓存路径已不存在（被清理），重新解析
	}

	// 1. 显式配置路径。
	if p.configPath != "" {
		if !fileExists(p.configPath) {
			return "", fmt.Errorf("配置的 CFR jar 不存在: %s", p.configPath)
		}
		p.resolved = p.configPath
		return p.resolved, nil
	}

	cached := p.cachedJarPath()

	// 3. 数据根缓存命中（放在内嵌写盘之前判，避免重复写）。
	if cached != "" && fileExists(cached) {
		p.resolved = cached
		return p.resolved, nil
	}

	// 2. 内嵌 jar：写到缓存目录复用（无缓存目录则写临时目录）。
	if p.embedded != nil {
		if jar := p.embedded(); len(jar) > 0 {
			dest := cached
			if dest == "" {
				dest = filepath.Join(os.TempDir(), "jianmanager-cfr-"+CFRVersion+".jar")
			}
			if err := writeFileAtomic(dest, jar); err != nil {
				return "", fmt.Errorf("落地内嵌 CFR jar 失败: %w", err)
			}
			p.resolved = dest
			return p.resolved, nil
		}
	}

	// 4. 按需下载（sha256 pin 校验后落缓存）。
	if p.allowDownload && cached != "" {
		if _, err := p.downloadWithPin(cached, cfrSHA256); err != nil {
			return "", err
		}
		p.resolved = cached
		return p.resolved, nil
	}

	return "", fmt.Errorf("无可用 CFR 反编译器（未配置/未内嵌/无缓存%s）",
		map[bool]string{true: "，且未启用下载", false: ""}[!p.allowDownload])
}

// downloadWithPin 从 downloadURL 拉取 CFR jar，校验 SHA-256 pin（wantHex）后原子落地到 dest。
// 内容指纹不匹配则丢弃并报错（不信任传输通道，只信内容）。返回 true 表示已成功落地。
func (p *Provider) downloadWithPin(dest, wantHex string) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return false, fmt.Errorf("创建 CFR 缓存目录失败: %w", err)
	}
	resp, err := p.httpClient.Get(p.downloadURL)
	if err != nil {
		return false, fmt.Errorf("下载 CFR 失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("下载 CFR 失败: HTTP %d", resp.StatusCode)
	}
	// 限制下载体积上限（CFR ~2MB，给 16MiB 余量防异常巨响应）。
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024*1024))
	if err != nil {
		return false, fmt.Errorf("读取 CFR 响应失败: %w", err)
	}
	if err := verifySHA256(body, wantHex); err != nil {
		return false, fmt.Errorf("CFR 内容校验失败（拒绝落地）: %w", err)
	}
	if err := writeFileAtomic(dest, body); err != nil {
		return false, fmt.Errorf("落地 CFR jar 失败: %w", err)
	}
	return true, nil
}

// verifySHA256 校验数据的 SHA-256 是否匹配期望十六进制指纹。
func verifySHA256(data []byte, wantHex string) error {
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if got != wantHex {
		return fmt.Errorf("SHA-256 不匹配: 期望 %s, 实际 %s", wantHex, got)
	}
	return nil
}

// writeFileAtomic 原子写文件：先写临时文件再 rename，避免并发/中断产生半截 jar。
func writeFileAtomic(dest string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".cfr-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // rename 成功后此 remove 无副作用
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, dest)
}

// fileExists 判断路径是否为存在的常规文件。
func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

package jdk

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
)

// jdkMirrorBase 返回指定 vendor 的下载基址：CP 下发的 override 优先（来自平台设置
// jdk.mirror.<vendor>，使运行时配置真生效），其次环境变量，最后回退官方默认。
// 实现 FR-033「下载源可配，默认 Adoptium」与 FR-063 平台设置真生效。
func jdkMirrorBase(vendor, override string) string {
	if v := strings.TrimSpace(override); v != "" {
		return v
	}
	switch strings.ToLower(vendor) {
	case "temurin", "adoptium":
		return envOr("JIANMANAGER_JDK_TEMURIN_BASE", "https://api.adoptium.net")
	case "corretto", "amazon":
		return envOr("JIANMANAGER_JDK_CORRETTO_BASE", "https://corretto.aws")
	case "zulu", "azul":
		return envOr("JIANMANAGER_JDK_ZULU_BASE", "https://api.azul.com")
	}
	return ""
}

// envOr 返回环境变量值（去空白），为空时返回默认值。
func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// buildDownloadURL 根据 vendor/major/arch/平台返回归档下载 URL。
// 下载基址优先用 mirrorBase（CP 从平台设置下发），其次 JIANMANAGER_JDK_<VENDOR>_BASE，
// 最后回退官方源（Temurin 默认 Adoptium）；未支持的 vendor 返回明确错误，提示用 POST /jdks 手动登记。
// client 用于 Zulu 这类需先查元数据 API 的来源（经进程级出站代理，FR-174/ADR-037）；
// client 为 nil 时回退 http.DefaultClient。
func buildDownloadURL(client *http.Client, vendor string, major int, arch, mirrorBase string) (string, error) {
	switch strings.ToLower(vendor) {
	case "temurin", "adoptium":
		return temurinURL(jdkMirrorBase(vendor, mirrorBase), major, arch), nil
	case "corretto", "amazon":
		return correttoURL(jdkMirrorBase(vendor, mirrorBase), major, arch), nil
	case "zulu", "azul":
		return zuluURL(client, jdkMirrorBase(vendor, mirrorBase), major, arch)
	default:
		return "", fmt.Errorf("unsupported vendor: %s (supported: Temurin, Corretto, Zulu)", vendor)
	}
}

func temurinURL(base string, major int, arch string) string {
	osName := "linux"
	if runtime.GOOS == "windows" {
		osName = "windows"
	} else if runtime.GOOS == "darwin" {
		osName = "mac"
	}
	// Adoptium Temurin LTS 通用归档（带完整 JRE+JDK）。
	return fmt.Sprintf("%s/v3/binary/latest/%d/ga/%s/%s/jdk/hotspot/normal/eclipse?project=jdk",
		base, major, osName, arch)
}

func correttoURL(base string, major int, arch string) string {
	osName := "linux"
	ext := "tar.gz"
	if runtime.GOOS == "windows" {
		osName = "windows"
		ext = "zip"
	} else if runtime.GOOS == "darwin" {
		osName = "macos"
		ext = "tar.gz"
	}
	return fmt.Sprintf("%s/downloads/latest/amazon-corretto-%d-%s-%s-jdk.%s",
		base, major, arch, osName, ext)
}

// zuluURL queries the Azul metadata API for the latest Zulu JDK download URL.
// client 经进程级出站代理（FR-174/ADR-037）；为 nil 时回退 http.DefaultClient。
func zuluURL(client *http.Client, base string, major int, arch string) (string, error) {
	osName := "linux"
	ext := "tar.gz"
	if runtime.GOOS == "windows" {
		osName = "windows"
		ext = "zip"
	} else if runtime.GOOS == "darwin" {
		osName = "macos"
	}
	apiURL := fmt.Sprintf("%s/metadata/v1/zulu/packages?java_version=%d&os=%s&arch=%s&archive_type=%s&latest=true&release_type=ga",
		base, major, osName, arch, ext)
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Get(apiURL)
	if err != nil {
		return "", fmt.Errorf("zulu metadata API failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("zulu metadata API returned HTTP %d", resp.StatusCode)
	}
	var pkgs []struct {
		DownloadURL string `json:"download_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&pkgs); err != nil {
		return "", fmt.Errorf("zulu metadata API parse failed: %w", err)
	}
	if len(pkgs) == 0 || pkgs[0].DownloadURL == "" {
		return "", fmt.Errorf("zulu metadata API returned no packages for Java %d %s", major, arch)
	}
	return pkgs[0].DownloadURL, nil
}

// downloadAndExtractWithProgress 下载归档到临时文件并解压到 destDir，期间经 report 回调
// 上报下载百分比与阶段日志（FR-183，见 ADR-040；report 可为 nil）。
// 有 Content-Length 时按字节计算 0~100 的真实百分比；无则停在 0、靠阶段日志补充。
func downloadAndExtractWithProgress(ctx context.Context, client *http.Client, url, destDir string, report jdkProgress) error {
	return downloadAndExtractStallParams(ctx, client, url, destDir, report, downloadStallTimeout, stallCheckInterval)
}

// DownloadAndExtract 下载归档到临时文件并按后缀解压到 destDir（导出薄封装，FR-299）。
// 供非 JDK 运行时安装器（internal/worker/runtime）复用与 JDK 完全一致的下载语义：
// 停滞看门狗判卡死（FR-290）、网络类失败追加可操作引导（FR-279）、zip/tar.gz 防路径逃逸。
// report 可为 nil；stall/interval <= 0 时用包默认看门狗阈值（测试可注入小值）。
func DownloadAndExtract(ctx context.Context, client *http.Client, url, destDir string, report Progress, stall, interval time.Duration) error {
	if stall <= 0 {
		stall = downloadStallTimeout
	}
	if interval <= 0 {
		interval = stallCheckInterval
	}
	return downloadAndExtractStallParams(ctx, client, url, destDir, jdkProgress(report), stall, interval)
}

// downloadAndExtractStallParams 同上，但停滞看门狗阈值参数化（便于测试以小值注入，FR-290）。
func downloadAndExtractStallParams(ctx context.Context, client *http.Client, url, destDir string, report jdkProgress, stall, interval time.Duration) error {
	if client == nil {
		client = &http.Client{} // 无总超时；卡死由 stall 看门狗判定（FIX-4）
	}
	if report == nil {
		report = func(int, string) {}
	}

	// 用可取消 context + stall 看门狗替代「总超时」：慢但在进展的大归档不被拖死，
	// 仅连续无字节进展（卡死/源不可达）时中断（FIX-4，原 15min 总超时会把进展中的大下载也掐断）。
	// 子 context 继承外部取消（FR-227 强制停止）与 stall 看门狗本地取消（FIX-4），任一触发即中断请求。
	parent := ctx // 保留外部 ctx：区分「看门狗停滞掐断」与「用户强停」两种取消（FR-290）
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("构造下载请求失败: %w", err)
	}
	pw := &progressWriter{report: report} // total 待响应头到达后填
	watchdogDone := make(chan struct{})
	// 看门狗掐断前先置停滞标记：失败时据此产出「下载停滞」专属错误并归网络类，
	// 而非裸 "context canceled"（真机复现：经代理下载流中途被掐，卡 48% 且错误无引导，FR-290）。
	var stalled atomic.Bool
	go watchStall(func() { stalled.Store(true); cancel() }, pw, stall, interval, watchdogDone)
	defer close(watchdogDone)

	resp, err := client.Do(req)
	if err != nil {
		// 网络类失败（TLS 超时 / DNS / 连接被拒 / 卡死取消）追加可操作引导（FR-279）。
		return annotateDownloadError(fmt.Errorf("下载失败: %w", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("下载返回 HTTP %d", resp.StatusCode)
	}

	// 通过 Content-Type 与后缀选择解压方式。
	name := filepath.Base(resp.Request.URL.Path)
	if name == "" || name == "/" {
		name = "jdk.bin"
	}
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		if i := strings.Index(cd, "filename="); i >= 0 {
			fname := cd[i+len("filename="):]
			fname = strings.Trim(fname, "\";'")
			if fname != "" {
				name = fname
			}
		}
	}

	tmp, err := os.CreateTemp("", "jdk-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	// 按 Content-Length 计算下载百分比；定频上报（每 +3% 或每 4MB 报一次），避免日志刷屏。
	pw.total = resp.ContentLength // 响应头已到，io.Copy 前填总长（看门狗只读 written 原子，无并发冲突）
	if _, err := io.Copy(tmp, io.TeeReader(resp.Body, pw)); err != nil {
		tmp.Close()
		// 三分叉（FR-290）：停滞掐断 → 专属说明+网络类引导；用户强停 → 原样不误标；
		// 其余传输中断（连接被重置等） → 归网络类引导。
		switch {
		case stalled.Load():
			// 停滞由看门狗确定判定，不依赖 isNetworkError 探测（底层是 context canceled），
			// 直接确定性追加 FR-279 引导。
			return fmt.Errorf("下载停滞已中断：连续 %v 无字节进展，疑似下载流被掐断/代理断流: %w%s", stall, err, networkHint)
		case parent.Err() != nil:
			return fmt.Errorf("写入临时文件失败: %w", err)
		default:
			return annotateDownloadError(fmt.Errorf("写入临时文件失败: %w", err))
		}
	}
	tmp.Close()
	report(100, "下载完成，开始解压")

	return extract(tmpPath, name, destDir)
}

// jdkProgress 下载进度回调（percent 0~100，line 可选阶段日志）。
type jdkProgress func(percent int, line string)

// progressWriter 包一层 io.Writer 统计已下载字节并按节流上报百分比（FR-183）。
// written 用原子计数，供下载 stall 看门狗并发读取（FIX-4）。
type progressWriter struct {
	total      int64 // Content-Length，<=0 表示未知
	written    atomic.Int64
	lastReport int   // 上次上报的百分比
	lastBytes  int64 // 上次上报时的累计字节
	report     jdkProgress
}

func (w *progressWriter) Write(p []byte) (int, error) {
	n := len(p)
	written := w.written.Add(int64(n))
	if w.total > 0 {
		percent := int(written * 100 / w.total)
		if percent > 100 {
			percent = 100
		}
		// 每 +3% 报一次，避免回调过密。
		if percent >= w.lastReport+3 || percent == 100 {
			w.lastReport = percent
			w.report(percent, fmt.Sprintf("下载中 %d%%", percent))
		}
	} else if written-w.lastBytes >= 4<<20 {
		// 无 Content-Length：每 4MB 报一次累计量，百分比保持 0。
		w.lastBytes = written
		w.report(0, fmt.Sprintf("已下载 %d MB", written>>20))
	}
	return n, nil
}

// Written 返回累计已下载字节（原子读，供 stall 看门狗）。
func (w *progressWriter) Written() int64 { return w.written.Load() }

// 下载 stall 看门狗参数（FIX-4）：用「无字节进展时长」而非「总时长」判定卡死——
// 慢但在进展的大归档不中断、无总上限；仅连续 downloadStallTimeout 收不到任何字节才中断。
const (
	downloadStallTimeout = 90 * time.Second
	stallCheckInterval   = 5 * time.Second
)

// watchStall 看门狗：每 interval 检查 pw 字节是否进展，连续 stall 无进展则 cancel；done 关闭即退出。
// 阈值参数化便于测试以小值注入。
func watchStall(cancel context.CancelFunc, pw *progressWriter, stall, interval time.Duration, done <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var last int64
	var idle time.Duration
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			cur := pw.Written()
			if cur > last {
				last, idle = cur, 0
				continue
			}
			idle += interval
			if idle >= stall {
				cancel()
				return
			}
		}
	}
}

// extract 根据文件名后缀分发到对应解压流程。
func extract(archivePath, suggestedName, destDir string) error {
	lower := strings.ToLower(suggestedName)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return unzip(archivePath, destDir)
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return untarGz(archivePath, destDir)
	default:
		// 二进制 / 其它格式：暂不支持，提示用户手动解压。
		return fmt.Errorf("不支持的归档格式: %s", suggestedName)
	}
}

func unzip(archivePath, destDir string) error {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("打开 zip 失败: %w", err)
	}
	defer r.Close()

	for _, f := range r.File {
		// 阻止 zip slip
		target, err := sanitizeArchivePath(destDir, f.Name)
		if err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			return err
		}
		in, err := f.Open()
		if err != nil {
			out.Close()
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			in.Close()
			out.Close()
			return err
		}
		in.Close()
		out.Close()
	}
	return nil
}

func untarGz(archivePath, destDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("打开 gzip 失败: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		target, err := sanitizeArchivePath(destDir, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA: //nolint:staticcheck // 保留旧 tar 常规文件标记兼容，避免遗漏历史归档。
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(hdr.Mode)&0o777)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		case tar.TypeSymlink:
			// Node 官方 tar.gz 的 bin/npm、bin/corepack 是相对符号链接，必须保留
			// （FR-299 缺陷：此前静默丢弃致 corepack/npm 不可用）。仅允许**相对**且
			// 解析后不逃出解压根的链接目标（防 symlink 逃逸）。
			if filepath.IsAbs(hdr.Linkname) {
				return fmt.Errorf("归档含绝对符号链接目标: %s -> %s", hdr.Name, hdr.Linkname)
			}
			resolved := filepath.Join(filepath.Dir(target), hdr.Linkname)
			if rel, err := filepath.Rel(destDir, resolved); err != nil || strings.HasPrefix(rel, "..") {
				return fmt.Errorf("归档含逃逸符号链接: %s -> %s", hdr.Name, hdr.Linkname)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target) // 覆盖旧链接/文件
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return fmt.Errorf("创建符号链接失败 %s: %w", hdr.Name, err)
			}
		}
	}
	return nil
}

// sanitizeArchivePath 防止 zip slip / tar slip：要求 name 不逃出 destDir。
func sanitizeArchivePath(destDir, name string) (string, error) {
	clean := filepath.Clean(name)
	if strings.HasPrefix(clean, "..") || strings.Contains(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("归档包含非法路径: %s", name)
	}
	target := filepath.Join(destDir, clean)
	// 二次校验：Join 后必须仍在 destDir 内
	rel, err := filepath.Rel(destDir, target)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("归档包含逃逸路径: %s", name)
	}
	return target, nil
}

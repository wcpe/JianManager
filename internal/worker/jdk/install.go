package jdk

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

// downloadAndExtract 下载归档到临时文件，按平台后缀解压到 destDir。
// client 经进程级出站代理（FR-174/ADR-037）；为 nil 时回退一个 15min 超时的默认 client。
func downloadAndExtract(client *http.Client, url, destDir string) error {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Minute}
	}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("下载失败: %w", err)
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

	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		return fmt.Errorf("写入临时文件失败: %w", err)
	}
	tmp.Close()

	return extract(tmpPath, name, destDir)
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
		case tar.TypeReg, tar.TypeRegA:
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

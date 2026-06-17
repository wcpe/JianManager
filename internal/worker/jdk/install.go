package jdk

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// buildDownloadURL 根据 vendor/major/arch/平台返回官方归档下载 URL。
// 当前仅实现 Temurin：Adoptium API 静态归档。
// 其他 vendor 返回明确错误，提示使用 POST /jdks 手动登记。
func buildDownloadURL(vendor string, major int, arch string) (string, error) {
	switch strings.ToLower(vendor) {
	case "temurin", "adoptium":
		return temurinURL(major, arch), nil
	case "zulu", "azul":
		// Azul 提供 https://api.azul.com/metadata/v1/zulu/packages?...
		// 本期留口，由运维手动登记；后续可扩展。
		return "", fmt.Errorf("Zulu 一键安装尚未实现，请手动下载并使用 POST /jdks 登记")
	default:
		return "", fmt.Errorf("不支持的 vendor: %s（当前仅支持 Temurin）", vendor)
	}
}

func temurinURL(major int, arch string) string {
	osName := "linux"
	if runtime.GOOS == "windows" {
		osName = "windows"
	} else if runtime.GOOS == "darwin" {
		osName = "mac"
	}
	// Adoptium Temurin LTS 通用归档（带完整 JRE+JDK）。
	return fmt.Sprintf("https://api.adoptium.net/v3/binary/latest/%d/ga/%s/%s/jdk/hotspot/normal/eclipse?project=jdk",
		major, osName, arch)
}

// downloadAndExtract 下载归档到临时文件，按平台后缀解压到 destDir。
func downloadAndExtract(url, destDir string) error {
	client := &http.Client{Timeout: 15 * time.Minute}
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

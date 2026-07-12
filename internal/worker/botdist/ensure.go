// Package botdist 实现 bot-worker dist 的自愈下发（FR-308，见 ADR-070 修订 ADR-006）：
// Worker 启动后经既有 CP gRPC 通道拉取内嵌归档，物化到 <dataroot>/opt/bot-worker/，
// bot 能力不再依赖「工作目录恰好有 bot-worker/dist」的手工拷贝。运行时依赖
// （mineflayer 等）不随归档分发，指向 FR-307 托管全局包目录（node_modules 链接 + NODE_PATH）。
package botdist

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/wcpe/JianManager/proto/workerpb"
)

// Options 自愈参数。
type Options struct {
	// CPAddr Control Plane gRPC 地址（与注册/心跳同源）。
	CPAddr string
	// NodeUUID / NodeSecret 节点身份（CP 侧与重注册同源校验）。
	NodeUUID   string
	NodeSecret string
	// Dir 物化目标目录（<dataroot>/opt/bot-worker）。
	Dir string
	// GlobalNodeModules node_modules 链接目标（FR-307 托管全局包的 node_modules 布局目录）。
	GlobalNodeModules string
	// client 测试注入的 gRPC 客户端；nil 时按 CPAddr 自建连接。
	client workerpb.WorkerServiceClient
}

// localManifestName 物化目录内记录归档指纹的清单文件名。
const localManifestName = ".jm-manifest.json"

type localManifest struct {
	SHA256  string `json:"sha256"`
	Version string `json:"version"`
}

// Ensure 自愈 bot-worker dist：与 CP 内嵌归档比对指纹，不一致则拉取并原子换新，
// 每次都补齐 node_modules 链接。返回入口脚本绝对路径（<Dir>/index.js）。
// CP 不可达或未内嵌时回退本地已有物化副本（有则用、无则报错），不阻断 Worker 启动。
func Ensure(ctx context.Context, opts Options) (string, error) {
	entry := filepath.Join(opts.Dir, "index.js")
	known := readLocalSHA(opts.Dir)
	if !fileExists(entry) {
		known = "" // 清单在但脚本没了：视为本地无，强制重拉
	}

	resp, err := fetchArchive(ctx, opts, known)
	switch {
	case err != nil || !resp.Success:
		reason := ""
		if err != nil {
			reason = err.Error()
		} else {
			reason = resp.Error
		}
		if fileExists(entry) {
			slog.Warn("bot-worker 归档拉取未成，沿用本地已物化副本", "reason", reason)
			if lerr := ensureNodeModulesLink(opts.Dir, opts.GlobalNodeModules); lerr != nil {
				slog.Warn("bot-worker node_modules 链接补齐失败", "error", lerr)
			}
			return entry, nil
		}
		return "", fmt.Errorf("bot-worker dist 不可用（本地无副本且拉取失败：%s）", reason)
	case resp.Sha256 == known && known != "":
		// 指纹一致：沿用本地，仅补链接。
	case len(resp.Archive) == 0:
		return "", fmt.Errorf("CP 报告新指纹 %.12s… 但未回归档字节（协议异常）", resp.Sha256)
	default:
		sum := sha256.Sum256(resp.Archive)
		if got := hex.EncodeToString(sum[:]); got != resp.Sha256 {
			return "", fmt.Errorf("bot-worker 归档校验失败：期望 %.12s… 实得 %.12s…", resp.Sha256, got)
		}
		if err := swapIn(opts.Dir, resp.Archive, localManifest{SHA256: resp.Sha256, Version: resp.Version}); err != nil {
			return "", fmt.Errorf("物化 bot-worker dist 失败: %w", err)
		}
		slog.Info("bot-worker dist 已更新", "version", resp.Version, "sha256", resp.Sha256[:12], "dir", opts.Dir)
	}

	if err := ensureNodeModulesLink(opts.Dir, opts.GlobalNodeModules); err != nil {
		// 链接失败不致命：ESM 解析会缺依赖，但 CheckDeps 会在 spawn 前给可操作报错。
		slog.Warn("bot-worker node_modules 链接创建失败", "error", err)
	}
	if !fileExists(entry) {
		return "", fmt.Errorf("归档物化后缺少入口脚本 %s（归档内容异常）", entry)
	}
	return entry, nil
}

// fetchArchive 经既有 CP gRPC 通道拉取归档（注册/心跳同通道，NAT 节点天然可达）。
func fetchArchive(ctx context.Context, opts Options, known string) (*workerpb.FetchBotWorkerArchiveResponse, error) {
	client := opts.client
	if client == nil {
		conn, err := grpc.NewClient(opts.CPAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return nil, err
		}
		defer conn.Close()
		client = workerpb.NewWorkerServiceClient(conn)
	}
	fctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return client.FetchBotWorkerArchive(fctx, &workerpb.FetchBotWorkerArchiveRequest{
		NodeUuid:    opts.NodeUUID,
		NodeSecret:  opts.NodeSecret,
		KnownSha256: known,
	})
}

func readLocalSHA(dir string) string {
	raw, err := os.ReadFile(filepath.Join(dir, localManifestName))
	if err != nil {
		return ""
	}
	var m localManifest
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	return m.SHA256
}

// swapIn 把归档解压到临时目录后原子换入 dir（旧目录先挪 .old 再删，失败可回滚现场）。
func swapIn(dir string, archive []byte, m localManifest) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	tmp := dir + ".tmp"
	old := dir + ".old"
	_ = os.RemoveAll(tmp)
	_ = os.RemoveAll(old)
	if err := extractTarGz(archive, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	raw, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(tmp, localManifestName), raw, 0o644); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	if fileExists(dir) || dirExists(dir) {
		if err := os.Rename(dir, old); err != nil {
			_ = os.RemoveAll(tmp)
			return fmt.Errorf("挪开旧目录失败（bot-worker 是否在运行占用？）: %w", err)
		}
	}
	if err := os.Rename(tmp, dir); err != nil {
		// 换入失败：把旧目录挪回去，现场不破坏。
		_ = os.Rename(old, dir)
		_ = os.RemoveAll(tmp)
		return err
	}
	_ = os.RemoveAll(old)
	return nil
}

// extractTarGz 解压 embed-botworker 产出的归档（仅常规文件），拒绝路径穿越。
func extractTarGz(archive []byte, dst string) error {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue // 归档由 embed-botworker 生成，只含常规文件；其余一律忽略
		}
		name := filepath.FromSlash(hdr.Name)
		if filepath.IsAbs(name) || strings.Contains(hdr.Name, "..") {
			return fmt.Errorf("归档含非法路径: %q", hdr.Name)
		}
		p := filepath.Join(dst, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, tr); err != nil { //nolint:gosec // 归档为 CP 构建期产物且已过 sha256 校验
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
}

// ensureNodeModulesLink 在 dist 同级补齐 node_modules 链接 → FR-307 托管全局包目录，
// 使 ESM 的 import 从 <dir>/index.js 就近解析到全局装的 mineflayer（NODE_PATH 只救 CJS）。
// Windows 用 junction（mklink /J，无需特权），其余平台 symlink。
func ensureNodeModulesLink(dir, target string) error {
	if target == "" {
		return nil
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return fmt.Errorf("创建链接目标失败: %w", err)
	}
	link := filepath.Join(dir, "node_modules")
	if fi, err := os.Lstat(link); err == nil {
		// 就位检测双通道：symlink 走 EvalSymlinks；Windows junction 不被 EvalSymlinks
		// 解析（Go 视 mount point 非 symlink），用 Readlink 直读 reparse 目标比对。
		if cur, rerr := os.Readlink(link); rerr == nil && samePath(cur, target) {
			return nil
		}
		if resolved, rerr := filepath.EvalSymlinks(link); rerr == nil {
			want, _ := filepath.EvalSymlinks(target)
			if resolved == want {
				return nil // 已就位
			}
		}
		// 指向不对/残留实体：symlink、junction、空目录都能被 Remove 掉；
		// 非空真目录删不掉——那是用户手工放的依赖，不覆写，保留并沿用。
		if err := os.Remove(link); err != nil {
			if fi.IsDir() {
				slog.Warn("bot-worker/node_modules 是非空真目录，保留不覆写", "path", link)
				return nil
			}
			return err
		}
	}
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS != "windows" {
			return err
		}
		// Windows 非开发者模式 symlink 需特权：降级 junction（目录联接，无需特权）。
		out, jerr := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
		if jerr != nil {
			return fmt.Errorf("创建 junction 失败: %v（%s）", jerr, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// samePath 归一后比较两个路径是否同一位置（Windows 大小写不敏感）。
func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

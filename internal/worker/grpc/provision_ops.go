package grpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/worker/artifactcache"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// DownloadCore 下载服务端核心 jar 到实例工作目录（FR-034 一键开服 + FR-178 节点制品缓存）。
// 实例须已注册（CreateInstance），据其工作目录落地；可选 sha256 校验，不符则删除并报错。
//
// 节点制品缓存（FR-178，仅服务端核心 jar）：
//  1. sha256 非空且缓存命中 → 直接从缓存秒拷到工作目录（免网络），touch lastUsed。
//  2. 未命中 → 走 downloadFile 下载（边下边算 sha256 校验），落地后存入缓存 + 写 meta。
//  3. sha256 为空（少数源无校验）→ 不缓存，按现状下载（缓存键必须是 sha256，无键不缓存）。
func (s *Server) DownloadCore(ctx context.Context, req *workerpb.DownloadCoreRequest) (*workerpb.DownloadCoreResponse, error) {
	inst, exists := s.manager.GetInstance(req.InstanceUuid)
	if !exists {
		return &workerpb.DownloadCoreResponse{Success: false, Error: fmt.Sprintf("实例 %s 未注册", req.InstanceUuid)}, nil
	}

	dest := strings.TrimSpace(req.DestFilename)
	if dest == "" {
		dest = "server.jar"
	}
	// dest 仅作为工作目录下的文件名，禁止路径分隔符与穿越。
	if strings.ContainsAny(dest, `/\`) || strings.Contains(dest, "..") {
		return &workerpb.DownloadCoreResponse{Success: false, Error: "非法的目标文件名"}, nil
	}
	target := filepath.Join(inst.WorkDir, dest)

	want := strings.ToLower(strings.TrimSpace(req.Sha256))

	// 1) 缓存命中（仅当有 sha256 键且启用缓存）：秒拷免网络。
	if s.cache != nil && want != "" {
		if err := os.MkdirAll(inst.WorkDir, 0o755); err != nil {
			return &workerpb.DownloadCoreResponse{Success: false, Error: fmt.Sprintf("创建工作目录失败: %v", err)}, nil
		}
		if hit, err := s.cache.GetTo(want, target); err == nil && hit {
			if st, statErr := os.Stat(target); statErr == nil {
				return &workerpb.DownloadCoreResponse{Success: true, Size: st.Size()}, nil
			}
		}
	}

	// 2) 未命中：下载并（有 sha256 时）校验。
	size, sum, err := downloadFile(ctx, s.outboundClient(), req.DownloadUrl, target)
	if err != nil {
		return &workerpb.DownloadCoreResponse{Success: false, Error: err.Error()}, nil
	}
	if want != "" && want != sum {
		_ = os.Remove(target)
		return &workerpb.DownloadCoreResponse{Success: false, Error: fmt.Sprintf("核心 sha256 校验不符：期望 %s 实得 %s", want, sum)}, nil
	}

	// 3) 存入缓存（仅当有 sha256 键且启用缓存；存入失败不影响本次建实例）。
	if s.cache != nil && sum != "" {
		meta := artifactcache.Meta{
			Name:      dest,
			Type:      "core",
			SourceURL: req.DownloadUrl,
			Size:      size,
		}
		if err := s.cache.Put(sum, target, meta); err != nil {
			slog.Warn("存入节点制品缓存失败（不影响本次建实例）", "sha256", sum, "error", err)
		}
	}

	return &workerpb.DownloadCoreResponse{Success: true, Size: size}, nil
}

// serverProbeJarName 是 ServerProbe 探针 jar 在实例 plugins 目录下的固定文件名。
const serverProbeJarName = "ServerProbe.jar"

// DeployServerProbe 将 ServerProbe 探针 jar 与 config.yml 写入实例 plugins 目录（FR-010 建服自动部署）。
// jar 为空（CP 未捆绑探针）时仅写 config，便于运维后续手动放入 jar 即按分配端口开启 /metrics；实例须已注册。
func (s *Server) DeployServerProbe(_ context.Context, req *workerpb.DeployServerProbeRequest) (*workerpb.DeployServerProbeResponse, error) {
	inst, exists := s.manager.GetInstance(req.InstanceUuid)
	if !exists {
		return &workerpb.DeployServerProbeResponse{Success: false, Error: fmt.Sprintf("实例 %s 未注册", req.InstanceUuid)}, nil
	}
	pluginsDir := filepath.Join(inst.WorkDir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		return &workerpb.DeployServerProbeResponse{Success: false, Error: fmt.Sprintf("创建 plugins 目录失败: %v", err)}, nil
	}
	if len(req.Jar) > 0 {
		if err := os.WriteFile(filepath.Join(pluginsDir, serverProbeJarName), req.Jar, 0o644); err != nil {
			return &workerpb.DeployServerProbeResponse{Success: false, Error: fmt.Sprintf("写入探针 jar 失败: %v", err)}, nil
		}
	}
	if cfg := req.ConfigYaml; cfg != "" {
		cfgDir := filepath.Join(pluginsDir, "ServerProbe")
		if err := os.MkdirAll(cfgDir, 0o755); err != nil {
			return &workerpb.DeployServerProbeResponse{Success: false, Error: fmt.Sprintf("创建探针配置目录失败: %v", err)}, nil
		}
		if err := os.WriteFile(filepath.Join(cfgDir, "config.yml"), []byte(cfg), 0o644); err != nil {
			return &workerpb.DeployServerProbeResponse{Success: false, Error: fmt.Sprintf("写入探针配置失败: %v", err)}, nil
		}
	}
	return &workerpb.DeployServerProbeResponse{Success: true}, nil
}

// downloadFile 流式下载 url 到 destPath，边写边算 sha256，返回字节数与 hex 小写摘要。
// client 经进程级出站代理（FR-174/ADR-037）；为 nil 时回退一个 15min 超时的默认 client。
func downloadFile(ctx context.Context, client *http.Client, url, destPath string) (int64, string, error) {
	if strings.TrimSpace(url) == "" {
		return 0, "", fmt.Errorf("下载地址为空")
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return 0, "", fmt.Errorf("创建目录失败: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, "", err
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Minute}
	} else if client.Timeout == 0 {
		// 工厂 client 默认不设整体超时；为大 jar 下载补一个上限（不改原 client）。
		c := *client
		c.Timeout = 15 * time.Minute
		client = &c
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return 0, "", fmt.Errorf("下载核心失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, "", fmt.Errorf("下载核心返回 HTTP %d", resp.StatusCode)
	}

	f, err := os.Create(destPath)
	if err != nil {
		return 0, "", fmt.Errorf("创建文件失败: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), resp.Body)
	if err != nil {
		return 0, "", fmt.Errorf("写入核心失败: %w", err)
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

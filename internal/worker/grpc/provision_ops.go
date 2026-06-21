package grpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wcpe/JianManager/proto/workerpb"
)

// DownloadCore 下载服务端核心 jar 到实例工作目录（FR-034 一键开服）。
// 实例须已注册（CreateInstance），据其工作目录落地；可选 sha256 校验，不符则删除并报错。
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

	size, sum, err := downloadFile(ctx, req.DownloadUrl, target)
	if err != nil {
		return &workerpb.DownloadCoreResponse{Success: false, Error: err.Error()}, nil
	}
	if want := strings.ToLower(strings.TrimSpace(req.Sha256)); want != "" && want != sum {
		_ = os.Remove(target)
		return &workerpb.DownloadCoreResponse{Success: false, Error: fmt.Sprintf("核心 sha256 校验不符：期望 %s 实得 %s", want, sum)}, nil
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
func downloadFile(ctx context.Context, url, destPath string) (int64, string, error) {
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
	client := &http.Client{Timeout: 15 * time.Minute}
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

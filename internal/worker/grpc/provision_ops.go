package grpc

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/worker/artifactcache"
	"github.com/wcpe/JianManager/internal/worker/process"
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
	if !safePlainFilename(dest) {
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

// forgeInstallerRunner 执行 Forge installer；测试会替换为假 runner，避免真实启动 Java。
type forgeInstallerRunner func(ctx context.Context, javaBin, installerPath, workDir string, env []string) ([]byte, error)

var runForgeInstaller forgeInstallerRunner = defaultRunForgeInstaller

// InstallForgeServer 在实例工作目录安装 Forge 服务端，并把 SpongeForge universal jar 部署到 mods/。
// 该 RPC 只暴露 FR-046 所需固定目录结构，不提供任意相对路径写入能力。
func (s *Server) InstallForgeServer(ctx context.Context, req *workerpb.InstallForgeServerRequest) (*workerpb.InstallForgeServerResponse, error) {
	inst, exists := s.manager.GetInstance(req.InstanceUuid)
	if !exists {
		return &workerpb.InstallForgeServerResponse{Success: false, Error: fmt.Sprintf("实例 %s 未注册", req.InstanceUuid)}, nil
	}
	if strings.TrimSpace(req.ForgeInstallerUrl) == "" {
		return &workerpb.InstallForgeServerResponse{Success: false, Error: "Forge installer 下载地址为空"}, nil
	}
	if strings.TrimSpace(req.SpongeforgeUrl) == "" {
		return &workerpb.InstallForgeServerResponse{Success: false, Error: "SpongeForge 下载地址为空"}, nil
	}

	modName := strings.TrimSpace(req.SpongeforgeFilename)
	if modName == "" {
		modName = "SpongeForge.jar"
	}
	if !safePlainFilename(modName) {
		return &workerpb.InstallForgeServerResponse{Success: false, Error: "非法的 SpongeForge 文件名"}, nil
	}
	launchJar := strings.TrimSpace(req.LaunchJar)
	if launchJar != "" && !safePlainFilename(launchJar) {
		return &workerpb.InstallForgeServerResponse{Success: false, Error: "非法的 Forge 启动 jar 文件名"}, nil
	}

	installerPath := filepath.Join(inst.WorkDir, ".jianmanager", "forge-installer.jar")
	if _, _, err := downloadAndVerify(ctx, s.outboundClient(), req.ForgeInstallerUrl, req.ForgeInstallerSha256, installerPath, "Forge installer"); err != nil {
		return &workerpb.InstallForgeServerResponse{Success: false, Error: err.Error()}, nil
	}

	installCtx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	javaBin := javaExecutable(inst.JDKBinPath, inst.JDKPath)
	env := process.ComposeEnv(os.Environ(), process.CommandSpec{JavaHome: inst.JDKPath, JDKBinPath: inst.JDKBinPath})
	out, err := runForgeInstaller(installCtx, javaBin, installerPath, inst.WorkDir, env)
	if err != nil {
		return &workerpb.InstallForgeServerResponse{Success: false, Error: fmt.Sprintf("Forge installer 执行失败: %v%s", err, installerOutputSuffix(out))}, nil
	}
	if launchJar == "" {
		launchJar = discoverForgeLaunchJar(inst.WorkDir)
	}
	if launchJar == "" {
		return &workerpb.InstallForgeServerResponse{Success: false, Error: "Forge installer 未生成可识别的启动 jar"}, nil
	}
	if _, err := os.Stat(filepath.Join(inst.WorkDir, launchJar)); err != nil {
		if discovered := discoverForgeLaunchJar(inst.WorkDir); discovered != "" {
			launchJar = discovered
		} else {
			return &workerpb.InstallForgeServerResponse{Success: false, Error: fmt.Sprintf("Forge 启动 jar 不存在: %s", launchJar)}, nil
		}
	}

	modPath := filepath.Join(inst.WorkDir, "mods", modName)
	size, _, err := downloadAndVerify(ctx, s.outboundClient(), req.SpongeforgeUrl, req.SpongeforgeSha256, modPath, "SpongeForge")
	if err != nil {
		return &workerpb.InstallForgeServerResponse{Success: false, Error: err.Error()}, nil
	}
	return &workerpb.InstallForgeServerResponse{Success: true, LaunchJar: launchJar, SpongeforgeSize: size}, nil
}

func defaultRunForgeInstaller(ctx context.Context, javaBin, installerPath, workDir string, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, javaBin, "-jar", installerPath, "--installServer")
	cmd.Dir = workDir
	cmd.Env = env
	return cmd.CombinedOutput()
}

func javaExecutable(jdkBinPath, javaHome string) string {
	name := "java"
	if runtime.GOOS == "windows" {
		name = "java.exe"
	}
	bin := strings.TrimSpace(jdkBinPath)
	if bin == "" && strings.TrimSpace(javaHome) != "" {
		bin = filepath.Join(javaHome, "bin")
	}
	if bin == "" {
		return name
	}
	return filepath.Join(bin, name)
}

func installerOutputSuffix(out []byte) string {
	out = bytes.TrimSpace(out)
	if len(out) == 0 {
		return ""
	}
	const max = 1024
	if len(out) > max {
		out = out[len(out)-max:]
	}
	return "，输出摘要: " + string(out)
}

func discoverForgeLaunchJar(workDir string) string {
	entries, err := os.ReadDir(workDir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.Type().IsRegular() && strings.HasPrefix(name, "forge-") && strings.HasSuffix(name, "-server.jar") {
			return name
		}
	}
	return ""
}

func safePlainFilename(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, `/\\`) || strings.Contains(name, "..") {
		return false
	}
	return filepath.Base(name) == name
}

const (
	probeMaxZipEntries    = 512
	probeMaxZipEntryBytes = 64 << 20
	probeMaxZipTotalBytes = 256 << 20
)

func deployServerProbeLibrariesZip(workDir string, data []byte) error {
	if len(data) > probeMaxZipTotalBytes {
		return fmt.Errorf("依赖缓存 zip 过大: %d", len(data))
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("读取 zip 失败: %w", err)
	}
	if len(zr.File) > probeMaxZipEntries {
		return fmt.Errorf("依赖缓存条目过多: %d", len(zr.File))
	}
	root := filepath.Join(workDir, "libraries")
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("解析 libraries 目录失败: %w", err)
	}
	var total int64
	for _, entry := range zr.File {
		dest, ok, err := probeLibraryZipDest(rootAbs, entry.Name)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		mode := entry.FileInfo().Mode()
		if mode.IsDir() {
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return fmt.Errorf("创建依赖目录失败: %w", err)
			}
			continue
		}
		if !mode.IsRegular() {
			return fmt.Errorf("依赖缓存含非法条目类型: %s", entry.Name)
		}
		if entry.UncompressedSize64 > probeMaxZipEntryBytes {
			return fmt.Errorf("依赖缓存单文件过大: %s", entry.Name)
		}
		if total+int64(entry.UncompressedSize64) > probeMaxZipTotalBytes {
			return fmt.Errorf("依赖缓存解压后过大: %d", total+int64(entry.UncompressedSize64))
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("创建依赖目录失败: %w", err)
		}
		n, err := writeProbeLibraryZipEntry(entry, dest)
		if err != nil {
			return err
		}
		total += n
		if total > probeMaxZipTotalBytes {
			return fmt.Errorf("依赖缓存解压后过大: %d", total)
		}
	}
	return nil
}

func probeLibraryZipDest(rootAbs, name string) (string, bool, error) {
	if strings.TrimSpace(name) == "" || strings.Contains(name, "\\") || strings.Contains(name, ":") || path.IsAbs(name) {
		return "", false, fmt.Errorf("依赖缓存含非法路径: %s", name)
	}
	clean := path.Clean(name)
	if clean != strings.TrimSuffix(name, "/") {
		return "", false, fmt.Errorf("依赖缓存含非法路径: %s", name)
	}
	if clean == "libraries" {
		return rootAbs, false, nil
	}
	if !strings.HasPrefix(clean, "libraries/") {
		return "", false, fmt.Errorf("依赖缓存条目必须位于 libraries/ 下: %s", name)
	}
	rel := strings.TrimPrefix(clean, "libraries/")
	dest := filepath.Join(rootAbs, filepath.FromSlash(rel))
	destAbs, err := filepath.Abs(dest)
	if err != nil {
		return "", false, fmt.Errorf("解析依赖缓存路径失败: %w", err)
	}
	if destAbs != rootAbs && !strings.HasPrefix(destAbs, rootAbs+string(os.PathSeparator)) {
		return "", false, fmt.Errorf("依赖缓存路径逃逸: %s", name)
	}
	return destAbs, true, nil
}

func writeProbeLibraryZipEntry(entry *zip.File, dest string) (int64, error) {
	r, err := entry.Open()
	if err != nil {
		return 0, fmt.Errorf("读取依赖缓存条目失败: %w", err)
	}
	defer r.Close()
	tmp := dest + ".tmp"
	defer os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, fmt.Errorf("创建依赖缓存文件失败: %w", err)
	}
	limited := &io.LimitedReader{R: r, N: probeMaxZipEntryBytes + 1}
	n, copyErr := io.Copy(f, limited)
	closeErr := f.Close()
	if copyErr != nil {
		return 0, fmt.Errorf("写入依赖缓存文件失败: %w", copyErr)
	}
	if closeErr != nil {
		return 0, fmt.Errorf("关闭依赖缓存文件失败: %w", closeErr)
	}
	if n > probeMaxZipEntryBytes {
		return 0, fmt.Errorf("依赖缓存单文件过大: %s", entry.Name)
	}
	_ = os.Remove(dest)
	if err := os.Rename(tmp, dest); err != nil {
		return 0, fmt.Errorf("替换依赖缓存文件失败: %w", err)
	}
	return n, nil
}

func downloadAndVerify(ctx context.Context, client *http.Client, url, sha, destPath, label string) (int64, string, error) {
	size, sum, err := downloadFile(ctx, client, url, destPath)
	if err != nil {
		return 0, "", fmt.Errorf("下载 %s 失败: %w", label, err)
	}
	want := strings.ToLower(strings.TrimSpace(sha))
	if want != "" && want != sum {
		_ = os.Remove(destPath)
		return 0, "", fmt.Errorf("%s sha256 校验不符：期望 %s 实得 %s", label, want, sum)
	}
	return size, sum, nil
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
	if len(req.LibrariesZip) > 0 {
		if err := deployServerProbeLibrariesZip(inst.WorkDir, req.LibrariesZip); err != nil {
			return &workerpb.DeployServerProbeResponse{Success: false, Error: fmt.Sprintf("写入探针依赖缓存失败: %v", err)}, nil
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

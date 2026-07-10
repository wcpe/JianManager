package grpc

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
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
		if launchJar == "" {
			launchJar = discoverModernForgeLaunchJar(inst.WorkDir)
		}
	}
	if launchJar == "" {
		return &workerpb.InstallForgeServerResponse{Success: false, Error: "Forge installer 未生成可识别的启动 jar"}, nil
	}
	if _, err := os.Stat(filepath.Join(inst.WorkDir, launchJar)); err != nil {
		if discovered := discoverForgeLaunchJar(inst.WorkDir); discovered != "" {
			launchJar = discovered
		} else if modernForgeLayoutReady(inst.WorkDir, launchJar) {
			// 现代 Forge 从 @args 文件启动，根目录无需存在 forge-*-server.jar。
		} else if discovered := discoverModernForgeLaunchJar(inst.WorkDir); discovered != "" {
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

func discoverModernForgeLaunchJar(workDir string) string {
	root := filepath.Join(workDir, "libraries", "net", "minecraftforge", "forge")
	versions, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	for _, version := range versions {
		if !version.IsDir() {
			continue
		}
		launchJar := "forge-" + version.Name() + "-server.jar"
		if modernForgeLayoutReady(workDir, launchJar) {
			return launchJar
		}
	}
	return ""
}

func modernForgeLayoutReady(workDir, launchJar string) bool {
	version := forgeVersionFromLaunchJar(launchJar)
	if version == "" {
		return false
	}
	base := filepath.Join(workDir, "libraries", "net", "minecraftforge", "forge", version)
	required := []string{
		filepath.Join(workDir, "user_jvm_args.txt"),
		filepath.Join(workDir, "forge-"+version+"-shim.jar"),
		filepath.Join(base, "forge-"+version+"-server.jar"),
	}
	for _, file := range required {
		if !regularFileExists(file) {
			return false
		}
	}
	return regularFileExists(filepath.Join(base, "win_args.txt")) || regularFileExists(filepath.Join(base, "unix_args.txt"))
}

func forgeVersionFromLaunchJar(launchJar string) string {
	name := strings.TrimSpace(launchJar)
	if !strings.HasPrefix(name, "forge-") || !strings.HasSuffix(name, "-server.jar") {
		return ""
	}
	version := strings.TrimSuffix(strings.TrimPrefix(name, "forge-"), "-server.jar")
	if version == "" || strings.ContainsAny(version, `/\\`) || strings.Contains(version, "..") {
		return ""
	}
	return version
}

func regularFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
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
	rootAbs, err := filepath.Abs(workDir)
	if err != nil {
		return fmt.Errorf("解析实例工作目录失败: %w", err)
	}
	var total int64
	var locked []string
	for _, entry := range zr.File {
		dest, ok, err := probeCacheZipDest(rootAbs, entry.Name)
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
		n, wasLocked, err := writeProbeLibraryZipEntry(entry, dest)
		if err != nil {
			return err
		}
		if wasLocked {
			locked = append(locked, entry.Name)
		}
		total += n
		if total > probeMaxZipTotalBytes {
			return fmt.Errorf("依赖缓存解压后过大: %d", total)
		}
	}
	// FR-068 在线更新：运行中实例锁定的 classpath 依赖无法覆盖，已降级跳过（旧版留存，下次重启后再更新生效）。
	// 只要 jar+config 与其余条目写成功即视为成功，不因文件锁使整个更新请求失败。
	if len(locked) > 0 {
		slog.Warn("部分探针依赖被运行中实例占用，本次跳过覆盖（旧版留存，下次重启后再更新生效）",
			"workDir", workDir, "count", len(locked), "entries", locked)
	}
	return nil
}

func probeCacheZipDest(workDirAbs, name string) (string, bool, error) {
	if strings.TrimSpace(name) == "" || strings.Contains(name, "\\") || strings.Contains(name, ":") || path.IsAbs(name) {
		return "", false, fmt.Errorf("依赖缓存含非法路径: %s", name)
	}
	clean := path.Clean(name)
	if clean != strings.TrimSuffix(name, "/") {
		return "", false, fmt.Errorf("依赖缓存含非法路径: %s", name)
	}
	if clean == "libraries" || clean == "assets" {
		return filepath.Join(workDirAbs, filepath.FromSlash(clean)), false, nil
	}
	if !strings.HasPrefix(clean, "libraries/") && !strings.HasPrefix(clean, "assets/") {
		return "", false, fmt.Errorf("依赖缓存条目必须位于 libraries/ 或 assets/ 下: %s", name)
	}
	dest := filepath.Join(workDirAbs, filepath.FromSlash(clean))
	destAbs, err := filepath.Abs(dest)
	if err != nil {
		return "", false, fmt.Errorf("解析依赖缓存路径失败: %w", err)
	}
	if destAbs != workDirAbs && !strings.HasPrefix(destAbs, workDirAbs+string(os.PathSeparator)) {
		return "", false, fmt.Errorf("依赖缓存路径逃逸: %s", name)
	}
	return destAbs, true, nil
}

// writeProbeLibraryZipEntry 将单个依赖条目落地到 dest，返回（写入/逻辑字节数, 是否因文件被锁跳过, error）。
//
// FR-068 运行中在线更新探针：JVM 独占锁定 classpath 依赖 jar，Windows 覆盖会失败（Access denied）。
// 两级绕锁——① 目标已存在且大小+CRC32 与条目一致（依赖版本极少变，绝大多数条目命中）→ 跳过写入、不触碰被锁文件；
// ② 内容确有变化但目标被锁 → 降级为「跳过并由调用方告警」（旧版留存，下次重启后再更新生效），返回 locked=true，
// 不使整个更新请求失败——只要 jar+config 与其余条目写成功即视为成功。
func writeProbeLibraryZipEntry(entry *zip.File, dest string) (int64, bool, error) {
	if probeLibraryEntryUnchanged(dest, entry) {
		return int64(entry.UncompressedSize64), false, nil
	}
	r, err := entry.Open()
	if err != nil {
		return 0, false, fmt.Errorf("读取依赖缓存条目失败: %w", err)
	}
	defer r.Close()
	tmp := dest + ".tmp"
	defer os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, false, fmt.Errorf("创建依赖缓存文件失败: %w", err)
	}
	limited := &io.LimitedReader{R: r, N: probeMaxZipEntryBytes + 1}
	n, copyErr := io.Copy(f, limited)
	closeErr := f.Close()
	if copyErr != nil {
		return 0, false, fmt.Errorf("写入依赖缓存文件失败: %w", copyErr)
	}
	if closeErr != nil {
		return 0, false, fmt.Errorf("关闭依赖缓存文件失败: %w", closeErr)
	}
	if n > probeMaxZipEntryBytes {
		return 0, false, fmt.Errorf("依赖缓存单文件过大: %s", entry.Name)
	}
	_ = os.Remove(dest)
	if err := os.Rename(tmp, dest); err != nil {
		if isFileLockedErr(err) {
			return 0, true, nil
		}
		return 0, false, fmt.Errorf("替换依赖缓存文件失败: %w", err)
	}
	return n, false, nil
}

// probeLibraryEntryUnchanged 判断 dest 已存在且内容与 zip 条目完全一致（大小 + CRC32）。
// 一致则跳过覆盖——依赖版本极少变，绝大多数条目命中，从而不触碰被运行中 JVM 锁定的 classpath jar（FR-068）。
func probeLibraryEntryUnchanged(dest string, entry *zip.File) bool {
	st, err := os.Stat(dest)
	if err != nil || !st.Mode().IsRegular() {
		return false
	}
	if uint64(st.Size()) != entry.UncompressedSize64 {
		return false
	}
	f, err := os.Open(dest)
	if err != nil {
		return false
	}
	defer f.Close()
	h := crc32.NewIEEE()
	if _, err := io.Copy(h, f); err != nil {
		return false
	}
	return h.Sum32() == entry.CRC32
}

// isFileLockedErr 判断 err 是否为「目标被其他进程独占占用」导致的写入/替换失败。
// 运行中实例的 JVM 独占 classpath 依赖 jar，Windows 覆盖表现为 Access denied（ERROR_ACCESS_DENIED）
// 或 Sharing violation（ERROR_SHARING_VIOLATION=32）。Unix 一般无此类独占锁，仅兜 EACCES/EPERM。
func isFileLockedErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrPermission) {
		return true
	}
	if runtime.GOOS == "windows" {
		var errno syscall.Errno
		if errors.As(err, &errno) {
			const errorSharingViolation = syscall.Errno(32)
			return errno == errorSharingViolation
		}
	}
	return false
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
func (s *Server) DeployServerProbe(ctx context.Context, req *workerpb.DeployServerProbeRequest) (*workerpb.DeployServerProbeResponse, error) {
	inst, exists := s.manager.GetInstance(req.InstanceUuid)
	if !exists {
		return &workerpb.DeployServerProbeResponse{Success: false, Error: fmt.Sprintf("实例 %s 未注册", req.InstanceUuid)}, nil
	}
	if len(req.Jar) > 0 {
		if err := s.prepareServerProbeDependencies(ctx, inst.WorkDir, req.Jar); err != nil {
			return &workerpb.DeployServerProbeResponse{Success: false, Error: fmt.Sprintf("预置探针依赖失败: %v", err)}, nil
		}
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

const (
	defaultProbeLibraryDir        = "libraries"
	probeMetadataMaxBytes         = 64 * 1024
	probeDependencyMaxBytes int64 = 64 * 1024 * 1024
)

type probeRuntimeMeta struct {
	fileLibs                string
	repoCentral             string
	repoTabooLib            string
	tabooLibVersion         string
	kotlinVersion           string
	kotlinCoroutinesVersion string
	modules                 []string
}

type mavenDependency struct {
	group    string
	artifact string
	version  string
	repo     string
}

func (s *Server) prepareServerProbeDependencies(ctx context.Context, workDir string, jar []byte) error {
	meta, ok, err := readProbeRuntimeMeta(jar)
	if err != nil || !ok {
		return err
	}
	cacheDir, err := probeLibraryDir(workDir, meta.fileLibs)
	if err != nil {
		return err
	}
	for _, dep := range meta.dependencies() {
		if err := s.ensureMavenDependency(ctx, cacheDir, dep); err != nil {
			return err
		}
	}
	return nil
}

func readProbeRuntimeMeta(jar []byte) (probeRuntimeMeta, bool, error) {
	zr, err := zip.NewReader(bytes.NewReader(jar), int64(len(jar)))
	if err != nil {
		return probeRuntimeMeta{}, false, nil
	}
	envText, ok, err := readZipText(zr, "META-INF/taboolib/env.properties")
	if err != nil || !ok {
		return probeRuntimeMeta{}, ok, err
	}
	versionText, ok, err := readZipText(zr, "META-INF/taboolib/version.properties")
	if err != nil || !ok {
		return probeRuntimeMeta{}, ok, err
	}
	env := parseProbeProperties(envText)
	version := parseProbeProperties(versionText)
	meta := probeRuntimeMeta{
		fileLibs:                envValue(env, "file-libs", defaultProbeLibraryDir),
		repoCentral:             strings.TrimSpace(env["repo-central"]),
		repoTabooLib:            strings.TrimSpace(env["repo-taboolib"]),
		tabooLibVersion:         strings.TrimSpace(version["taboolib"]),
		kotlinVersion:           strings.TrimSpace(version["kotlin"]),
		kotlinCoroutinesVersion: strings.TrimSpace(version["kotlin-coroutines"]),
		modules:                 splitProbeModules(env["module"]),
	}
	return meta, true, nil
}

func readZipText(zr *zip.Reader, name string) (string, bool, error) {
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", true, err
		}
		defer rc.Close()
		b, err := io.ReadAll(io.LimitReader(rc, probeMetadataMaxBytes+1))
		if err != nil {
			return "", true, err
		}
		if len(b) > probeMetadataMaxBytes {
			return "", true, fmt.Errorf("探针元数据 %s 超出大小上限", name)
		}
		return string(b), true, nil
	}
	return "", false, nil
}

func parseProbeProperties(text string) map[string]string {
	props := make(map[string]string)
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			key, val, ok = strings.Cut(line, ":")
		}
		if ok {
			props[strings.TrimSpace(key)] = strings.TrimSpace(val)
		}
	}
	return props
}

func envValue(props map[string]string, key string, fallback string) string {
	if val := strings.TrimSpace(props[key]); val != "" {
		return val
	}
	return fallback
}

func splitProbeModules(raw string) []string {
	var out []string
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

func probeLibraryDir(workDir string, fileLibs string) (string, error) {
	fileLibs = strings.TrimSpace(fileLibs)
	if fileLibs == "" {
		fileLibs = defaultProbeLibraryDir
	}
	if filepath.IsAbs(fileLibs) {
		return "", fmt.Errorf("探针依赖缓存目录必须是实例内相对路径: %s", fileLibs)
	}
	clean := filepath.Clean(fileLibs)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("探针依赖缓存目录越界: %s", fileLibs)
	}
	return filepath.Join(workDir, clean), nil
}

func (m probeRuntimeMeta) dependencies() []mavenDependency {
	var deps []mavenDependency
	if isEnabledVersion(m.kotlinVersion) {
		deps = append(deps,
			mavenDependency{
				group:    "org.jetbrains.kotlin",
				artifact: "kotlin-stdlib",
				version:  m.kotlinVersion,
				repo:     m.repoCentral,
			},
			mavenDependency{
				group:    "org.jetbrains.kotlin",
				artifact: "kotlin-stdlib-jdk8",
				version:  m.kotlinVersion,
				repo:     m.repoCentral,
			},
		)
	}
	if isEnabledVersion(m.kotlinCoroutinesVersion) {
		deps = append(deps, mavenDependency{
			group:    "org.jetbrains.kotlinx",
			artifact: "kotlinx-coroutines-core-jvm",
			version:  m.kotlinCoroutinesVersion,
			repo:     m.repoCentral,
		})
	}
	if !isEnabledVersion(m.tabooLibVersion) {
		return deps
	}
	for _, module := range m.modules {
		deps = append(deps, mavenDependency{
			group:    "io.izzel.taboolib",
			artifact: module,
			version:  m.tabooLibVersion,
			repo:     m.repoTabooLib,
		})
	}
	return deps
}

func isEnabledVersion(version string) bool {
	version = strings.TrimSpace(version)
	return version != "" && version != "null" && version != "skip"
}

func (s *Server) ensureMavenDependency(ctx context.Context, cacheDir string, dep mavenDependency) error {
	if err := dep.validate(); err != nil {
		return err
	}
	for _, ext := range []string{"pom", "jar"} {
		target := filepath.Join(cacheDir, dep.localPath(ext))
		if hasNonEmptyFile(target) {
			continue
		}
		source := dep.remoteURL(ext)
		if err := downloadProbeDependency(ctx, s.outboundClient(), source, target); err != nil {
			return fmt.Errorf("%s:%s:%s %s 下载失败: %w", dep.group, dep.artifact, dep.version, ext, err)
		}
	}
	return nil
}

func (d mavenDependency) validate() error {
	for _, part := range []string{d.group, d.artifact, d.version} {
		if part == "" || strings.ContainsAny(part, `/\`) || strings.Contains(part, "..") {
			return fmt.Errorf("非法 Maven 坐标: %s:%s:%s", d.group, d.artifact, d.version)
		}
	}
	if strings.TrimSpace(d.repo) == "" {
		return fmt.Errorf("依赖 %s:%s:%s 缺少仓库地址", d.group, d.artifact, d.version)
	}
	return nil
}

func (d mavenDependency) localPath(ext string) string {
	groupPath := strings.ReplaceAll(d.group, ".", string(filepath.Separator))
	name := fmt.Sprintf("%s-%s.%s", d.artifact, d.version, ext)
	return filepath.Join(groupPath, d.artifact, d.version, name)
}

func (d mavenDependency) remoteURL(ext string) string {
	segments := append(strings.Split(d.group, "."), d.artifact, d.version)
	segments = append(segments, fmt.Sprintf("%s-%s.%s", d.artifact, d.version, ext))
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.TrimRight(d.repo, "/") + "/" + strings.Join(segments, "/")
}

func hasNonEmptyFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Size() > 0
}

func downloadProbeDependency(ctx context.Context, client *http.Client, sourceURL string, target string) error {
	client = probeDependencyHTTPClient(client)
	resp, err := requestProbeDependency(ctx, client, sourceURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return writeProbeDependency(resp, target)
}

func probeDependencyHTTPClient(client *http.Client) *http.Client {
	if client == nil {
		return &http.Client{Timeout: 5 * time.Minute}
	}
	if client.Timeout == 0 {
		c := *client
		c.Timeout = 5 * time.Minute
		return &c
	}
	return client
}

func requestProbeDependency(ctx context.Context, client *http.Client, sourceURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > probeDependencyMaxBytes {
		resp.Body.Close()
		return nil, fmt.Errorf("文件超过大小上限")
	}
	return resp, nil
}

func writeProbeDependency(resp *http.Response, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	tmp := target + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(f, io.LimitReader(resp.Body, probeDependencyMaxBytes+1))
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}
	if n > probeDependencyMaxBytes {
		_ = os.Remove(tmp)
		return fmt.Errorf("文件超过大小上限")
	}
	return os.Rename(tmp, target)
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

	// 中途失败（限速/超时/连接中断）或不完整下载都不得留下半成品 jar，
	// 否则会被当成有效核心落地（真机曾出现 ~1.3MB 截断 jar 建成实例）。
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(destPath)
		return 0, "", fmt.Errorf("写入核心失败: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(destPath)
		return 0, "", fmt.Errorf("关闭核心文件失败: %w", closeErr)
	}
	if resp.ContentLength >= 0 && n != resp.ContentLength {
		_ = os.Remove(destPath)
		return 0, "", fmt.Errorf("核心下载不完整：期望 %d 字节实得 %d 字节", resp.ContentLength, n)
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

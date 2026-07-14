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

// DownloadCore 下载服务端核心 jar 到实例工作目录（FR-034 一键开服 + FR-178/330 节点核心缓存）。
// 实例须已注册（CreateInstance），据其工作目录落地；可选 sha256 校验，不符则删除并报错。
//
// 节点核心缓存（FR-178 sha256 键 + FR-330 组合键，仅服务端核心 jar）：
//  1. 命中 → 从缓存秒拷到工作目录（免网络）。键取 sha256（已知时）或 core|mcVersion|build
//     组合键（Sponge Maven 等无 sha 源）；命中时全量校验内容，损坏即作废条目回退下载。
//  2. 未命中 → 同缓存键并发单飞（FR-330）：领队走 downloadFile 下载（边下边算 sha256 校验），
//     落地后存入缓存 + 写 meta；其余等待领队完成后从缓存取，同核心并发搭建只下载一次。
//  3. 无任何缓存键（sha256 为空且组合键成分不全，如 BungeeCord latest）→ 每次下载，
//     仍按算出的 sha256 存入缓存（面板可见可清理），但因无键不可复用。
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
	coreKey := coreCacheKey(req.CoreType, req.McVersion, req.Build)

	// 1) 缓存命中：sha256 键优先，组合键兜底。GetTo 内部全量校验，损坏条目作废按未命中走。
	if size, hit := s.fetchCoreFromCache(req.InstanceUuid, want, coreKey, target); hit {
		return &workerpb.DownloadCoreResponse{Success: true, Size: size, CacheHit: true}, nil
	}

	// 2) 未命中：同缓存键并发单飞（FR-330）。无键（latest 等不可冻结源）直接下载。
	flightKey := want
	if flightKey == "" {
		flightKey = coreKey
	}
	if s.cache == nil || flightKey == "" {
		size, err := s.downloadCoreToTarget(ctx, req, target, dest, want, coreKey)
		if err != nil {
			return &workerpb.DownloadCoreResponse{Success: false, Error: err.Error()}, nil
		}
		return &workerpb.DownloadCoreResponse{Success: true, Size: size}, nil
	}

	fl, leader := s.joinCoreFlight(flightKey)
	if leader {
		size, err := s.downloadCoreToTarget(ctx, req, target, dest, want, coreKey)
		s.finishCoreFlight(flightKey, fl, err)
		if err != nil {
			return &workerpb.DownloadCoreResponse{Success: false, Error: err.Error()}, nil
		}
		return &workerpb.DownloadCoreResponse{Success: true, Size: size}, nil
	}

	// 跟随者：等领队完成后从缓存取（领队 Put 先于 finish，成功即可命中）。
	select {
	case <-fl.done:
	case <-ctx.Done():
		return &workerpb.DownloadCoreResponse{Success: false, Error: fmt.Sprintf("等待同核心下载被取消: %v", ctx.Err())}, nil
	}
	if fl.err == nil {
		if size, hit := s.fetchCoreFromCache(req.InstanceUuid, want, coreKey, target); hit {
			return &workerpb.DownloadCoreResponse{Success: true, Size: size, CacheHit: true}, nil
		}
	}
	// 领队失败或缓存取失败（Put 失败/条目被并发清理）：退化为自己下载，保证本实例交付。
	size, err := s.downloadCoreToTarget(ctx, req, target, dest, want, coreKey)
	if err != nil {
		return &workerpb.DownloadCoreResponse{Success: false, Error: err.Error()}, nil
	}
	return &workerpb.DownloadCoreResponse{Success: true, Size: size}, nil
}

// coreCacheKey 组合缓存键（FR-330）：无 sha256 的下载源按 `core|mcVersion|build` 定位缓存。
// 三元组必须都具体（CP 已把 latest/未指定构建解析为具体构建再下发）；缺任一
// （如 BungeeCord latest 无构建号）返回空 = 不参与组合键缓存，避免冻结 latest 语义。
func coreCacheKey(coreType, mcVersion string, build int32) string {
	ct := strings.ToLower(strings.TrimSpace(coreType))
	mv := strings.TrimSpace(mcVersion)
	if ct == "" || mv == "" || strings.EqualFold(mv, "latest") || build <= 0 {
		return ""
	}
	return fmt.Sprintf("%s|%s|%d", ct, mv, build)
}

// fetchCoreFromCache 尝试从节点核心缓存交付到 target：sha256 键优先，组合键兜底反查。
// GetTo 命中时全量校验内容（FR-330），损坏条目已被作废，返回未命中让调用方回退下载。
func (s *Server) fetchCoreFromCache(instanceUUID, want, coreKey, target string) (int64, bool) {
	if s.cache == nil {
		return 0, false
	}
	sha := want
	if sha == "" && coreKey != "" {
		if k, ok := s.cache.LookupCoreKey(coreKey); ok {
			sha = k
		}
	}
	if sha == "" {
		return 0, false
	}
	hit, err := s.cache.GetTo(sha, target)
	if err != nil || !hit {
		return 0, false
	}
	st, statErr := os.Stat(target)
	if statErr != nil {
		return 0, false
	}
	slog.Info("核心下载：缓存命中秒拷", "instance", instanceUUID, "sha256", sha[:12], "size", st.Size())
	return st.Size(), true
}

// downloadCoreToTarget 远程下载核心到 target 并（有 sha256 时）校验，成功后存入节点缓存。
// 开始/结果都留日志（FR-319）：慢源下载被 CP 取消/中断时，此前 worker 全程零痕迹不可追查。
func (s *Server) downloadCoreToTarget(ctx context.Context, req *workerpb.DownloadCoreRequest, target, dest, want, coreKey string) (int64, error) {
	slog.Info("核心下载开始", "instance", req.InstanceUuid, "url", req.DownloadUrl)
	start := time.Now()
	size, sum, err := downloadFile(ctx, s.outboundClient(), req.DownloadUrl, target)
	if err != nil {
		slog.Warn("核心下载失败", "instance", req.InstanceUuid, "url", req.DownloadUrl,
			"elapsed", time.Since(start).Round(time.Second), "error", err)
		return 0, err
	}
	slog.Info("核心下载完成", "instance", req.InstanceUuid, "size", size,
		"elapsed", time.Since(start).Round(time.Second))
	if want != "" && want != sum {
		_ = os.Remove(target)
		return 0, fmt.Errorf("核心 sha256 校验不符：期望 %s 实得 %s", want, sum)
	}

	// 存入缓存（键 = 实得 sha256，组合键写入 meta 供无 sha 源反查；存入失败不影响本次建实例）。
	if s.cache != nil && sum != "" {
		meta := artifactcache.Meta{
			Name:      dest,
			Type:      "core",
			SourceURL: req.DownloadUrl,
			Size:      size,
			CoreKey:   coreKey,
		}
		// 有核心元信息时用更可读的名字/版本（面板展示，FR-330）：如 paper-1.21.8 / 1.21.8-263。
		if ct := strings.ToLower(strings.TrimSpace(req.CoreType)); ct != "" && strings.TrimSpace(req.McVersion) != "" {
			meta.Name = ct + "-" + strings.TrimSpace(req.McVersion)
			if req.Build > 0 {
				meta.Version = fmt.Sprintf("%s-%d", strings.TrimSpace(req.McVersion), req.Build)
			}
		}
		if err := s.cache.Put(sum, target, meta); err != nil {
			slog.Warn("存入节点制品缓存失败（不影响本次建实例）", "sha256", sum, "error", err)
		}
	}
	return size, nil
}

// coreFlight 一次进行中的核心下载（FR-330 并发单飞）：领队完成（成功 Put 入缓存或失败）后
// close(done)，跟随者据 err 决定从缓存取还是退化自下。
type coreFlight struct {
	done chan struct{}
	err  error
}

// joinCoreFlight 加入（或创建）缓存键对应的下载单飞；返回是否为领队。
func (s *Server) joinCoreFlight(key string) (*coreFlight, bool) {
	s.coreFlightMu.Lock()
	defer s.coreFlightMu.Unlock()
	if s.coreFlights == nil {
		s.coreFlights = make(map[string]*coreFlight)
	}
	if fl, ok := s.coreFlights[key]; ok {
		return fl, false
	}
	fl := &coreFlight{done: make(chan struct{})}
	s.coreFlights[key] = fl
	return fl, true
}

// finishCoreFlight 领队完成：先摘登记（后续新请求另起单飞），再置结果并唤醒跟随者。
func (s *Server) finishCoreFlight(key string, fl *coreFlight, err error) {
	s.coreFlightMu.Lock()
	delete(s.coreFlights, key)
	s.coreFlightMu.Unlock()
	fl.err = err
	close(fl.done)
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

	// 先写 .part 临时文件、完整校验后原子 rename 换入：目标路径要么没有、要么是完整文件。
	// 此前直接写目标路径，慢源下载的几分钟窗口内躺着半截 jar——异步搭建（FR-319）期间
	// 用户点启动即得 `Invalid or corrupt jarfile`（真机复现，2.7MB/43MB 时被 java 吃到）。
	tmpPath := destPath + ".part"
	f, err := os.Create(tmpPath)
	if err != nil {
		return 0, "", fmt.Errorf("创建文件失败: %w", err)
	}

	// 中途失败（限速/超时/连接中断）或不完整下载都不得留下半成品 jar，
	// 否则会被当成有效核心落地（真机曾出现 ~1.3MB 截断 jar 建成实例）。
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(tmpPath)
		return 0, "", fmt.Errorf("写入核心失败: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		return 0, "", fmt.Errorf("关闭核心文件失败: %w", closeErr)
	}
	if resp.ContentLength >= 0 && n != resp.ContentLength {
		_ = os.Remove(tmpPath)
		return 0, "", fmt.Errorf("核心下载不完整：期望 %d 字节实得 %d 字节", resp.ContentLength, n)
	}
	if err := os.Rename(tmpPath, destPath); err != nil {
		_ = os.Remove(tmpPath)
		return 0, "", fmt.Errorf("落位核心文件失败: %w", err)
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

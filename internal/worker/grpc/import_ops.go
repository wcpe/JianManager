package grpc

import (
	"archive/zip"
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/wcpe/JianManager/proto/workerpb"
)

// 导入现有服务器（FR-302，见 ADR-XXXX）：InspectServerDir 只读探测现成服务器目录，
// ImportServerDir 按模式就地接管（no-op）或整体搬进托管区（同盘 rename 优先，
// 跨盘拷贝 + 数量/字节校验 + 清源）。

// importJarScanMaxDepth jar 候选扫描深度上限：根目录（1）+ 一级子目录（2）。
// 更深（libraries/ 内的依赖 jar 等）几乎不可能是核心 jar，扫进来只添噪音。
const importJarScanMaxDepth = 2

// InspectServerDir 只读探测节点上某现成服务器目录（FR-302）。
// 守卫：路径必须绝对/存在/目录，且不得指向托管区之内（防重复收编已受管实例）
// 或托管区祖先（防后续搬迁自吞）。
func (s *Server) InspectServerDir(_ context.Context, req *workerpb.InspectServerDirRequest) (*workerpb.InspectServerDirResponse, error) {
	clean, guardErr := s.guardImportPath(req.Path)
	if guardErr != "" {
		return &workerpb.InspectServerDirResponse{Success: false, Error: guardErr}, nil
	}

	resp := &workerpb.InspectServerDirResponse{Success: true}
	resp.Jars = scanJarCandidates(clean)
	for _, dir := range scanJDKCandidateDirs(clean) {
		if cand, ok := s.probeImportJDK(dir); ok {
			resp.Jdks = append(resp.Jdks, cand)
		}
	}
	port, found := parseServerPort(clean)
	resp.ServerPort = int32(port)
	resp.PropsFound = found
	resp.EulaAccepted = parseEulaAccepted(clean)
	return resp, nil
}

// ImportServerDir 导入现成目录（FR-302）：in_place 模式 no-op 回原路径；
// migrate 模式把目录整体搬进托管区 var/servers/<target_slug>。
func (s *Server) ImportServerDir(_ context.Context, req *workerpb.ImportServerDirRequest) (*workerpb.ImportServerDirResponse, error) {
	clean, guardErr := s.guardImportPath(req.Path)
	if guardErr != "" {
		return &workerpb.ImportServerDirResponse{Success: false, Error: guardErr}, nil
	}

	switch req.Mode {
	case "in_place":
		// 就地接管：目录一个字节不动，工作目录即原目录（托管区外例外，见 ADR-XXXX）。
		return &workerpb.ImportServerDirResponse{Success: true, WorkDir: clean, Moved: false}, nil
	case "migrate":
		if s.root == nil {
			return &workerpb.ImportServerDirResponse{Success: false, Error: "本节点无数据根，无法界定托管区"}, nil
		}
		if !validImportSlug(req.TargetSlug) {
			return &workerpb.ImportServerDirResponse{Success: false, Error: fmt.Sprintf("非法的目标目录名: %q", req.TargetSlug)}, nil
		}
		target := filepath.Join(s.root.ServersDir(), req.TargetSlug)
		if _, err := os.Stat(target); err == nil {
			return &workerpb.ImportServerDirResponse{Success: false, Error: fmt.Sprintf("目标目录已存在: %s", target)}, nil
		}
		if err := os.MkdirAll(s.root.ServersDir(), 0o755); err != nil {
			return &workerpb.ImportServerDirResponse{Success: false, Error: fmt.Sprintf("创建托管区失败: %v", err)}, nil
		}
		renamed, err := moveDirWithFallback(clean, target, os.Rename)
		if err != nil {
			return &workerpb.ImportServerDirResponse{Success: false, Error: err.Error()}, nil
		}
		slog.Info("导入搬迁完成", "src", clean, "target", target, "renamed", renamed)
		return &workerpb.ImportServerDirResponse{Success: true, WorkDir: target, Moved: true}, nil
	default:
		return &workerpb.ImportServerDirResponse{Success: false, Error: fmt.Sprintf("未知导入模式: %q（in_place | migrate）", req.Mode)}, nil
	}
}

// guardImportPath 校验导入根路径，返回规范化绝对路径；不合法时返回中文原因。
// 拒绝托管区之内（已有实例目录，防重复收编）与托管区祖先（含数据根，防搬迁自吞）。
func (s *Server) guardImportPath(path string) (clean, errMsg string) {
	clean = filepath.Clean(strings.TrimSpace(path))
	if clean == "" || clean == "." {
		return "", "路径不能为空"
	}
	if !filepath.IsAbs(clean) {
		return "", "必须是绝对路径"
	}
	st, err := os.Stat(clean)
	if err != nil {
		return "", fmt.Sprintf("无法访问: %v", err)
	}
	if !st.IsDir() {
		return "", "不是目录"
	}
	if s.root != nil {
		servers := s.root.ServersDir()
		if clean == filepath.Clean(servers) || underDir(servers, clean) {
			return "", fmt.Sprintf("路径位于托管区 (%s) 内，已受管目录不可重复导入", servers)
		}
		if underDir(clean, servers) {
			return "", fmt.Sprintf("路径是托管区 (%s) 的上层目录，不可导入", servers)
		}
	}
	return clean, ""
}

// probeImportJDK 探测某子目录是否为可登记 JDK。优先注入的替身（importJDKProbe，测试用），
// 否则经 jdkMgr.Probe（内部 detectAt：跑 bin/java 读版本）；目录本身不是 JDK home 时
// 再向下探一级（归档解压外层多包一层的常见布局，与 Install 的探测一致）。
func (s *Server) probeImportJDK(dir string) (*workerpb.ImportJdkCandidate, bool) {
	if s.importJDKProbe != nil {
		return s.importJDKProbe(dir)
	}
	if s.jdkMgr == nil {
		return nil, false
	}
	homes := []string{dir}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				homes = append(homes, filepath.Join(dir, e.Name()))
			}
		}
	}
	for _, home := range homes {
		info, err := s.jdkMgr.Probe(home)
		if err != nil {
			continue
		}
		return &workerpb.ImportJdkCandidate{
			Path:         info.Path,
			Vendor:       info.Vendor,
			Version:      info.Version,
			MajorVersion: int32(info.MajorVersion),
			Arch:         info.Arch,
		}, true
	}
	return nil, false
}

// scanJarCandidates 扫描导入根下的 jar 候选（深度≤2），已知核心名排前、
// 同级根目录先于子目录、再按名稳定排序；每个候选嗅探 MANIFEST Main-Class 作提示。
func scanJarCandidates(root string) []*workerpb.ImportJarCandidate {
	type cand struct {
		rel   string
		depth int
		size  int64
	}
	var found []cand
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	appendJar := func(rel string, depth int, info os.FileInfo) {
		found = append(found, cand{rel: filepath.ToSlash(rel), depth: depth, size: info.Size()})
	}
	for _, e := range entries {
		if !e.IsDir() {
			if strings.EqualFold(filepath.Ext(e.Name()), ".jar") {
				if info, err := e.Info(); err == nil {
					appendJar(e.Name(), 1, info)
				}
			}
			continue
		}
		subEntries, err := os.ReadDir(filepath.Join(root, e.Name()))
		if err != nil {
			continue
		}
		for _, sub := range subEntries {
			if sub.IsDir() || !strings.EqualFold(filepath.Ext(sub.Name()), ".jar") {
				continue
			}
			if info, err := sub.Info(); err == nil {
				appendJar(filepath.Join(e.Name(), sub.Name()), importJarScanMaxDepth, info)
			}
		}
	}

	sort.SliceStable(found, func(i, j int) bool {
		ri, rj := jarNameRank(found[i].rel), jarNameRank(found[j].rel)
		if ri != rj {
			return ri < rj
		}
		if found[i].depth != found[j].depth {
			return found[i].depth < found[j].depth
		}
		return found[i].rel < found[j].rel
	})

	out := make([]*workerpb.ImportJarCandidate, 0, len(found))
	for _, c := range found {
		out = append(out, &workerpb.ImportJarCandidate{
			Path:          c.rel,
			Size:          c.size,
			MainClassHint: sniffMainClass(filepath.Join(root, filepath.FromSlash(c.rel))),
		})
	}
	return out
}

// jarNameRank 已知服务端核心名排序权重：越小越靠前；未知名 jar 落最后一档。
func jarNameRank(rel string) int {
	name := strings.ToLower(filepath.Base(filepath.FromSlash(rel)))
	switch {
	case name == "server.jar":
		return 0
	case strings.HasPrefix(name, "paper-"):
		return 1
	case strings.HasPrefix(name, "purpur-"):
		return 2
	case strings.HasPrefix(name, "spigot-"):
		return 3
	case strings.HasPrefix(name, "fabric-server"):
		return 4
	case strings.HasPrefix(name, "forge-"):
		return 5
	default:
		return 100
	}
}

// sniffMainClass 读 jar 的 META-INF/MANIFEST.MF Main-Class 作排序/展示提示。
// shaded jar 可能误标（见 spec 风险），仅作 hint、最终 jar 由用户单选拍板；失败静默返回空。
func sniffMainClass(jarPath string) string {
	zr, err := zip.OpenReader(jarPath)
	if err != nil {
		return ""
	}
	defer func() { _ = zr.Close() }()
	for _, f := range zr.File {
		if f.Name != "META-INF/MANIFEST.MF" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return ""
		}
		defer func() { _ = rc.Close() }()
		sc := bufio.NewScanner(io.LimitReader(rc, 64*1024))
		for sc.Scan() {
			line := strings.TrimRight(sc.Text(), "\r")
			if v, ok := strings.CutPrefix(line, "Main-Class:"); ok {
				return strings.TrimSpace(v)
			}
		}
		return ""
	}
	return ""
}

// scanJDKCandidateDirs 返回导入根下疑似内嵌 JDK 的一级子目录：
// jre* / jdk* / runtime / java（大小写不敏感）。是否真 JDK 由探测（bin/java）确认。
func scanJDKCandidateDirs(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := strings.ToLower(e.Name())
		if strings.HasPrefix(name, "jre") || strings.HasPrefix(name, "jdk") || name == "runtime" || name == "java" {
			out = append(out, filepath.Join(root, e.Name()))
		}
	}
	return out
}

// parseServerPort 读 server.properties 的 server-port。返回 (端口, 文件是否存在且可解析)。
func parseServerPort(root string) (int, bool) {
	f, err := os.Open(filepath.Join(root, "server.properties"))
	if err != nil {
		return 0, false
	}
	defer func() { _ = f.Close() }()
	port := 0
	sc := bufio.NewScanner(io.LimitReader(f, 1024*1024))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if v, ok := strings.CutPrefix(line, "server-port="); ok {
			if p, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && p > 0 && p <= 65535 {
				port = p
			}
		}
	}
	return port, true
}

// parseEulaAccepted 读 eula.txt 是否 eula=true（大小写不敏感）。
func parseEulaAccepted(root string) bool {
	f, err := os.Open(filepath.Join(root, "eula.txt"))
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(io.LimitReader(f, 64*1024))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if v, ok := strings.CutPrefix(line, "eula="); ok && strings.EqualFold(strings.TrimSpace(v), "true") {
			return true
		}
	}
	return false
}

// validImportSlug 校验 migrate 目标目录名：CP 按实例名生成的 <slug>-<shortid>，
// 仅允许 [a-z0-9-]，杜绝路径分隔/穿越逃出托管区。
func validImportSlug(slug string) bool {
	if slug == "" || len(slug) > 64 {
		return false
	}
	for _, r := range slug {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

// moveDirWithFallback 把 src 整体搬到 dst：先试 rename（同盘 O(1) 原子）；失败（跨盘等）
// 回退递归拷贝 → 文件数/字节数双校验 → 校验通过后清源。校验不一致或拷贝失败时删除
// 半成品目标、源目录保持原样（宁可失败保源，不产出半截搬迁，见 ADR-XXXX）。
// rename 可注入（测试强制走拷贝回退路径）。返回是否经 rename 完成。
func moveDirWithFallback(src, dst string, rename func(oldpath, newpath string) error) (bool, error) {
	if err := rename(src, dst); err == nil {
		return true, nil
	}
	srcFiles, srcBytes, err := copyTree(src, dst)
	if err != nil {
		_ = os.RemoveAll(dst)
		return false, fmt.Errorf("跨盘拷贝失败（源目录未动）: %w", err)
	}
	dstFiles, dstBytes, err := countTree(dst)
	if err != nil {
		_ = os.RemoveAll(dst)
		return false, fmt.Errorf("校验目标目录失败（源目录未动）: %w", err)
	}
	if dstFiles != srcFiles || dstBytes != srcBytes {
		_ = os.RemoveAll(dst)
		return false, fmt.Errorf("拷贝校验不一致（源 %d 文件/%d 字节，目标 %d 文件/%d 字节），已回滚目标、源目录未动",
			srcFiles, srcBytes, dstFiles, dstBytes)
	}
	if err := os.RemoveAll(src); err != nil {
		// 目标已完整，仅清源失败：不回滚（避免二次风险），报错让上层引导手工清理。
		return false, fmt.Errorf("目录已完整拷贝到 %s，但清理源目录失败: %w", dst, err)
	}
	return false, nil
}

// copyTree 递归拷贝目录，返回拷贝的文件数与字节数。
// 仅支持目录与常规文件；符号链接等特殊文件直接报错（拷贝语义不可靠，rename 路径不受限）。
func copyTree(src, dst string) (files int64, bytes int64, err error) {
	err = filepath.WalkDir(src, func(path string, d os.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		rel, rerr := filepath.Rel(src, path)
		if rerr != nil {
			return rerr
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("包含不支持的特殊文件（如符号链接）: %s", path)
		}
		n, cerr := copyImportFile(path, target)
		if cerr != nil {
			return cerr
		}
		files++
		bytes += n
		return nil
	})
	return files, bytes, err
}

// copyImportFile 拷贝单个文件并返回字节数（搬迁校验需要计量，故不复用 clone 的 copyFile）。
func copyImportFile(src, dst string) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return n, err
}

// countTree 统计目录内常规文件数与字节数（拷贝校验用）。
func countTree(root string) (files int64, bytes int64, err error) {
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		files++
		bytes += info.Size()
		return nil
	})
	return files, bytes, err
}

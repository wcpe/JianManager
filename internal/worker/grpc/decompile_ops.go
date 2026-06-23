package grpc

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/wcpe/JianManager/internal/worker/decompiler"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// maxDecompileInputBytes 是反编译输入 class/jar 的字节上限（ADR-018 决策 4）。
// 超限拒绝，避免巨 jar 拖垮节点内存/耗时。
const maxDecompileInputBytes = 16 * 1024 * 1024

// SetDecompiler 注入 CFR 反编译器解析器，由 Worker 主进程启动时设置。
// 为 nil 表示本节点未启用反编译能力，DecompileClass 返回明确降级错误（不 panic）。
func (s *Server) SetDecompiler(p *decompiler.Provider) { s.decompiler = p }

// DecompileClass 经实例/系统 JDK 跑 CFR 反编译工作目录内 class/jar（或归档内某 class）为 Java 源码。
// 全程只读 + 超时 + 体积上限 + 失败降级（ADR-018）：CFR 静态分析字节码，不加载/运行目标代码。
// 失败/降级返回 success=false + error（结构化），不抛错（除实例不存在/路径越界这类前置校验）。
func (s *Server) DecompileClass(ctx context.Context, req *workerpb.DecompileClassRequest) (*workerpb.DecompileClassResponse, error) {
	inst, exists := s.manager.GetInstance(req.InstanceUuid)
	if !exists {
		return nil, fmt.Errorf("实例 %s 不存在", req.InstanceUuid)
	}
	if s.decompiler == nil {
		return &workerpb.DecompileClassResponse{Success: false, Error: "本节点未启用反编译能力"}, nil
	}

	targetPath := filepath.Join(inst.WorkDir, req.Path)
	if err := validatePath(inst.WorkDir, targetPath); err != nil {
		return nil, err
	}

	// 解析 java 可执行：实例绑定 JDK 优先，否则系统候选 JDK，再否则 PATH。
	javaBin := decompiler.ResolveJavaBin(inst.JDKPath, inst.JDKBinPath, s.systemJDKRoots())
	if javaBin == "" {
		return &workerpb.DecompileClassResponse{Success: false, Error: "无可用 JDK，反编译降级"}, nil
	}

	cfrJar, err := s.decompiler.Resolve()
	if err != nil {
		return &workerpb.DecompileClassResponse{Success: false, Error: fmt.Sprintf("CFR 反编译器不可用: %v", err)}, nil
	}

	// 准备反编译目标：按 path 扩展名 + entry 决定。
	target, jarEntry, cleanup, perr := s.prepareDecompileTarget(targetPath, req.Path, req.Entry)
	if cleanup != nil {
		defer cleanup()
	}
	if perr != nil {
		return &workerpb.DecompileClassResponse{Success: false, Error: perr.Error()}, nil
	}

	res, rerr := decompiler.Run(ctx, decompiler.Options{
		JavaBin:  javaBin,
		CFRJar:   cfrJar,
		Target:   target,
		JarEntry: jarEntry,
	})
	if rerr != nil {
		return &workerpb.DecompileClassResponse{Success: false, Error: rerr.Error()}, nil
	}
	return &workerpb.DecompileClassResponse{
		Success:    true,
		Source:     res.Source,
		Truncated:  res.Truncated,
		Decompiler: res.Decompiler,
	}, nil
}

// prepareDecompileTarget 据目标路径与可选 jar entry 决定交给 CFR 的目标：
//   - path 为 .class：直接用该文件（忽略 entry）；
//   - path 为 .jar 且 entry 非空：把 jar 内该 class 抽到临时文件，target=临时文件，jarEntry 留空；
//   - path 为 .jar 且 entry 空：target=jar 路径，整 jar 反编译（jarEntry 留空，CFR 全量）。
//
// 返回 cleanup（删除临时文件，可为 nil）。所有输入经体积上限校验。
func (s *Server) prepareDecompileTarget(absPath, relPath, entry string) (target, jarEntry string, cleanup func(), err error) {
	ext := strings.ToLower(filepath.Ext(relPath))

	switch ext {
	case ".class":
		if err := checkFileSize(absPath, maxDecompileInputBytes); err != nil {
			return "", "", nil, err
		}
		return absPath, "", nil, nil

	case ".jar":
		if err := checkFileSize(absPath, maxDecompileInputBytes); err != nil {
			return "", "", nil, err
		}
		if entry == "" {
			// 整 jar 反编译。
			return absPath, "", nil, nil
		}
		if !isSafeArchiveEntryName(entry) {
			return "", "", nil, fmt.Errorf("非法的归档条目名: %s", entry)
		}
		if !strings.HasSuffix(strings.ToLower(entry), ".class") {
			return "", "", nil, fmt.Errorf("仅支持反编译 .class 条目: %s", entry)
		}
		// 从 jar 抽出该 class 到临时文件（避免把整 jar 交给 CFR 全量反编译）。
		tmp, terr := extractClassFromJar(absPath, entry)
		if terr != nil {
			return "", "", nil, terr
		}
		return tmp, "", func() { _ = os.Remove(tmp) }, nil

	default:
		return "", "", nil, fmt.Errorf("不支持反编译该文件类型（仅 .class / .jar）: %s", relPath)
	}
}

// extractClassFromJar 从 jar 中抽出某 .class 条目到临时 .class 文件，返回临时文件路径。
// 抽出体积截断到上限，防异常巨条目。
func extractClassFromJar(jarPath, entry string) (string, error) {
	zr, err := zip.OpenReader(jarPath)
	if err != nil {
		return "", fmt.Errorf("打开 jar 失败: %w", err)
	}
	defer zr.Close()

	var f *zip.File
	for _, e := range zr.File {
		if e.Name == entry {
			f = e
			break
		}
	}
	if f == nil {
		return "", fmt.Errorf("jar 内不存在条目: %s", entry)
	}
	if f.FileInfo().IsDir() {
		return "", fmt.Errorf("%s 是目录条目", entry)
	}

	rc, err := f.Open()
	if err != nil {
		return "", fmt.Errorf("读取 jar 条目失败: %w", err)
	}
	defer rc.Close()

	tmp, err := os.CreateTemp("", "jm-decompile-*.class")
	if err != nil {
		return "", fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := io.Copy(tmp, io.LimitReader(rc, maxDecompileInputBytes)); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("抽取 jar 条目失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return "", err
	}
	return tmpName, nil
}

// checkFileSize 校验文件存在且不超过 limit 字节。
func checkFileSize(path string, limit int64) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("读取目标失败: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("目标是目录，无法反编译")
	}
	if info.Size() > limit {
		return fmt.Errorf("目标过大（%d 字节，上限 %d），拒绝反编译", info.Size(), limit)
	}
	return nil
}

// systemJDKRoots 返回 Worker 本机可作为系统 JDK 的根目录候选（供 CFR 运行时回退）。
// 来源：jdkMgr 探测到的各 JDK 路径 + JAVA_HOME。实例未绑 JDK 时用这些兜底。
func (s *Server) systemJDKRoots() []string {
	var roots []string
	if s.jdkMgr != nil {
		if infos, err := s.jdkMgr.List(); err == nil {
			for _, i := range infos {
				if i.Path != "" {
					roots = append(roots, i.Path)
				}
			}
		}
	}
	if jh := os.Getenv("JAVA_HOME"); jh != "" {
		roots = append(roots, jh)
	}
	return roots
}

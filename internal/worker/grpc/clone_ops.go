package grpc

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/wcpe/JianManager/proto/workerpb"
)

// CloneWorkDir 复制源实例工作目录到目标实例工作目录，排除运行态文件（FR-036 一键复制）。
// 源与目标须已注册（据其工作目录解析）；本机本地拷贝，不跨节点。
func (s *Server) CloneWorkDir(ctx context.Context, req *workerpb.CloneWorkDirRequest) (*workerpb.CloneWorkDirResponse, error) {
	src, ok := s.manager.GetInstance(req.SrcInstanceUuid)
	if !ok {
		return &workerpb.CloneWorkDirResponse{Success: false, Error: fmt.Sprintf("源实例 %s 未注册", req.SrcInstanceUuid)}, nil
	}
	dst, ok := s.manager.GetInstance(req.DstInstanceUuid)
	if !ok {
		return &workerpb.CloneWorkDirResponse{Success: false, Error: fmt.Sprintf("目标实例 %s 未注册", req.DstInstanceUuid)}, nil
	}
	if strings.TrimSpace(src.WorkDir) == "" || strings.TrimSpace(dst.WorkDir) == "" {
		return &workerpb.CloneWorkDirResponse{Success: false, Error: "源或目标工作目录为空"}, nil
	}

	files, bytesCopied, skipped, err := copyDirFiltered(src.WorkDir, dst.WorkDir, req.Include, req.Exclude)
	if err != nil {
		return &workerpb.CloneWorkDirResponse{Success: false, Error: err.Error()}, nil
	}
	return &workerpb.CloneWorkDirResponse{Success: true, CopiedFiles: files, CopiedBytes: bytesCopied, Skipped: skipped}, nil
}

// copyDirFiltered 递归复制 srcDir → dstDir：include 非空时仅复制顶层段命中 include 的项，
// 再按 exclude 排除。返回复制文件数、字节数与被跳过的顶层项/目录（FR-036 / FR-231）。
func copyDirFiltered(srcDir, dstDir string, include, exclude []string) (int64, int64, []string, error) {
	var files, bytesCopied int64
	skipped := []string{}
	err := filepath.Walk(srcDir, func(p string, info os.FileInfo, werr error) error {
		if werr != nil {
			return werr
		}
		rel, rerr := filepath.Rel(srcDir, p)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		// include 非空：不在 include 内的顶层项/目录跳过（FR-231 快速/高级筛选）；exclude 始终排除（运行态垃圾等）。
		if !cloneIncluded(rel, include) || cloneExcluded(rel, exclude) {
			if info.IsDir() {
				skipped = append(skipped, filepath.ToSlash(rel))
				return filepath.SkipDir
			}
			if !strings.Contains(rel, string(filepath.Separator)) {
				skipped = append(skipped, filepath.ToSlash(rel))
			}
			return nil
		}
		target := filepath.Join(dstDir, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		if !info.Mode().IsRegular() {
			return nil // 跳过符号链接/设备等特殊文件
		}
		if err := copyFile(p, target, info.Mode()); err != nil {
			return err
		}
		files++
		bytesCopied += info.Size()
		return nil
	})
	if err != nil {
		return 0, 0, nil, fmt.Errorf("复制工作目录失败: %w", err)
	}
	return files, bytesCopied, skipped, nil
}

// cloneIncluded 判断相对路径是否命中 include（FR-231）：include 空=全包含；
// 否则取 rel 首段，匹配某 include 模式（精确 / basename glob；自动去 "/**"、"/" 后缀，如 plugins、*.jar）。
func cloneIncluded(rel string, include []string) bool {
	if len(include) == 0 {
		return true
	}
	relSlash := filepath.ToSlash(rel)
	first := relSlash
	if i := strings.IndexByte(relSlash, '/'); i >= 0 {
		first = relSlash[:i]
	}
	for _, pat := range include {
		pat = filepath.ToSlash(strings.TrimSpace(pat))
		pat = strings.TrimSuffix(strings.TrimSuffix(pat, "/**"), "/")
		if pat == "" {
			continue
		}
		if pat == first {
			return true
		}
		if !strings.Contains(pat, "/") {
			if ok, _ := path.Match(pat, first); ok {
				return true
			}
		}
	}
	return false
}

// cloneExcluded 判断相对路径是否命中排除：目录前缀 / 精确路径 / basename glob（不含 '/' 的模式如 *.pid）。
func cloneExcluded(rel string, patterns []string) bool {
	relSlash := filepath.ToSlash(rel)
	base := path.Base(relSlash)
	for _, pat := range patterns {
		pat = filepath.ToSlash(strings.TrimSpace(pat))
		if pat == "" {
			continue
		}
		if pat == relSlash || strings.HasPrefix(relSlash, pat+"/") {
			return true
		}
		if !strings.Contains(pat, "/") {
			if ok, _ := path.Match(pat, base); ok {
				return true
			}
		}
	}
	return false
}

// copyFile 复制单个文件，保留权限位。
func copyFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return nil
}

package grpc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/wcpe/JianManager/proto/workerpb"
)

// FR-373：文件/目录权限元数据、写前探测、单 path 非递归 chmod、中文诊断。
// 口径相对 Worker 进程有效用户；不 chown、不递归、不 root。

// pathAccess 一次探测结果（纯数据，供 List/Browse/Check 复用）。
type pathAccess struct {
	Exists     bool
	IsDir      bool
	Readable   bool
	Writable   bool
	ModeOctal  string
	ModeString string
	Owner      string
	Group      string
	Reason     string
}

// probePathAccess 探测 absPath 相对当前进程用户的可读/可写与 mode 展示。
// 不存在时 exists=false；探测副作用：目录可写时会 CreateTemp 再删除。
func probePathAccess(absPath string) pathAccess {
	st, err := os.Lstat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return pathAccess{Reason: "路径不存在"}
		}
		return pathAccess{Reason: formatPermError("访问", err)}
	}
	out := pathAccess{
		Exists: true,
		IsDir:  st.IsDir(),
	}
	out.ModeOctal, out.ModeString = formatFileMode(st.Mode())
	out.Owner, out.Group = fileOwnerGroup(st)

	if st.IsDir() {
		out.Readable = dirReadable(absPath)
		out.Writable = dirWritable(absPath)
	} else {
		out.Readable = fileReadable(absPath)
		out.Writable = fileWritable(absPath)
	}
	switch {
	case !out.Readable && !out.Writable:
		if out.Reason == "" {
			out.Reason = "当前 Worker 用户对该路径既不可读也不可写"
		}
	case !out.Writable:
		out.Reason = "当前 Worker 用户可读但不可写该路径"
	case !out.Readable:
		out.Reason = "当前 Worker 用户可写但不可读该路径"
	}
	return out
}

func fileReadable(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

func fileWritable(path string) bool {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

func dirReadable(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	_, err = f.Readdirnames(1)
	// nil=有条目；io.EOF=空目录，均视为可读
	return err == nil || errors.Is(err, io.EOF)
}

func dirWritable(path string) bool {
	f, err := os.CreateTemp(path, ".jm-perm-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

// formatFileMode 返回八进制串（如 "0644"）与 rwx 串；无权限位时 octal 可空。
func formatFileMode(m fs.FileMode) (octal, str string) {
	perm := m.Perm()
	octal = fmt.Sprintf("%04o", perm)
	str = permString(perm)
	return octal, str
}

func permString(perm fs.FileMode) string {
	const rwx = "rwxrwxrwx"
	b := []byte("---------")
	for i := 0; i < 9; i++ {
		if perm&(1<<uint(8-i)) != 0 {
			b[i] = rwx[i]
		}
	}
	return string(b)
}

// formatPermError 将 permission denied 等映射为中文诊断。
func formatPermError(op string, err error) string {
	if err == nil {
		return ""
	}
	if os.IsPermission(err) || isEACCES(err) || isEPERM(err) {
		switch op {
		case "读取目录", "列出":
			return "没有权限读取该目录（Worker 用户无法列出内容）。可换路径、改用「搬进托管区」，或以有权限的用户运行 Worker；若属主是你，可尝试「修复权限」。"
		case "写入", "写文件":
			return "没有权限写入该文件。请检查属主/只读挂载，或尝试「修复权限」。"
		case "chmod", "修改权限":
			return "无法修改权限：Worker 用户不是属主或文件系统不允许 chmod。"
		default:
			return fmt.Sprintf("没有权限%s该路径（Worker 用户权限不足）。可换路径、换 Worker 运行用户，或尝试「修复权限」。", op)
		}
	}
	if os.IsNotExist(err) {
		return "路径不存在"
	}
	return fmt.Sprintf("%s失败: %v", op, err)
}

func isEACCES(err error) bool {
	var pe *os.PathError
	if errors.As(err, &pe) && pe.Err != nil {
		return pe.Err == syscall.EACCES || strings.Contains(pe.Err.Error(), "permission denied")
	}
	return strings.Contains(strings.ToLower(err.Error()), "permission denied")
}

func isEPERM(err error) bool {
	var pe *os.PathError
	if errors.As(err, &pe) && pe.Err != nil {
		return pe.Err == syscall.EPERM
	}
	return false
}

// applyChmod 对 absPath 做非递归 chmod。mode 空=在现有 mode 上 OR u+rw（目录 OR u+rwx）。
func applyChmod(absPath, mode string) (newOctal string, err error) {
	st, err := os.Lstat(absPath)
	if err != nil {
		return "", fmt.Errorf("%s", formatPermError("访问", err))
	}
	var target fs.FileMode
	mode = strings.TrimSpace(mode)
	if mode == "" {
		cur := st.Mode().Perm()
		if st.IsDir() {
			target = cur | 0o700
		} else {
			target = cur | 0o600
		}
	} else {
		// 允许 "0644" / "644" / "0o644"
		raw := strings.TrimPrefix(strings.ToLower(mode), "0o")
		v, perr := strconv.ParseUint(raw, 8, 32)
		if perr != nil {
			return "", fmt.Errorf("非法 mode %q（须为八进制如 0644）", mode)
		}
		target = fs.FileMode(v) & fs.ModePerm
	}
	if err := os.Chmod(absPath, target); err != nil {
		return "", fmt.Errorf("%s", formatPermError("chmod", err))
	}
	st2, err := os.Lstat(absPath)
	if err != nil {
		return fmt.Sprintf("%04o", target), nil
	}
	oct, _ := formatFileMode(st2.Mode())
	return oct, nil
}

// fillFileInfoPerm 把权限元数据填入 FileInfo（name/size 等由调用方已填）。
func fillFileInfoPerm(info *workerpb.FileInfo, absPath string) {
	a := probePathAccess(absPath)
	info.ModeOctal = a.ModeOctal
	info.ModeString = a.ModeString
	info.Readable = a.Readable
	info.Writable = a.Writable
	info.Owner = a.Owner
	info.Group = a.Group
}

// fillBrowseDirEntryPerm 填充 BrowseDirEntry 权限字段。
func fillBrowseDirEntryPerm(e *workerpb.BrowseDirEntry, absPath string) {
	a := probePathAccess(absPath)
	e.ModeOctal = a.ModeOctal
	e.ModeString = a.ModeString
	e.Readable = a.Readable
	e.Writable = a.Writable
	e.Owner = a.Owner
	e.Group = a.Group
}

// CheckPathAccess 探测路径访问能力（FR-373）。
func (s *Server) CheckPathAccess(_ context.Context, req *workerpb.CheckPathAccessRequest) (*workerpb.CheckPathAccessResponse, error) {
	abs, errMsg := s.resolvePermPath(req.InstanceUuid, req.Path)
	if errMsg != "" {
		return &workerpb.CheckPathAccessResponse{Success: false, Error: errMsg}, nil
	}
	a := probePathAccess(abs)
	return &workerpb.CheckPathAccessResponse{
		Success:    true,
		Exists:     a.Exists,
		IsDir:      a.IsDir,
		Readable:   a.Readable,
		Writable:   a.Writable,
		ModeOctal:  a.ModeOctal,
		ModeString: a.ModeString,
		Owner:      a.Owner,
		Group:      a.Group,
		Reason:     a.Reason,
	}, nil
}

// ChmodPath 单 path 非递归 chmod（FR-373）。
func (s *Server) ChmodPath(_ context.Context, req *workerpb.ChmodPathRequest) (*workerpb.ChmodPathResponse, error) {
	abs, errMsg := s.resolvePermPath(req.InstanceUuid, req.Path)
	if errMsg != "" {
		return &workerpb.ChmodPathResponse{Success: false, Error: errMsg}, nil
	}
	oct, err := applyChmod(abs, req.Mode)
	if err != nil {
		return &workerpb.ChmodPathResponse{Success: false, Error: err.Error()}, nil
	}
	return &workerpb.ChmodPathResponse{Success: true, ModeOctal: oct}, nil
}

// resolvePermPath 解析实例相对路径或节点绝对路径；失败返回中文 errMsg。
func (s *Server) resolvePermPath(instanceUUID, path string) (abs string, errMsg string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", "路径不能为空"
	}
	if instanceUUID != "" {
		inst, exists := s.manager.GetInstance(instanceUUID)
		if !exists {
			return "", fmt.Sprintf("实例 %s 不存在", instanceUUID)
		}
		target := filepath.Join(inst.WorkDir, path)
		if err := validatePath(inst.WorkDir, target); err != nil {
			return "", err.Error()
		}
		abs, err := filepath.Abs(target)
		if err != nil {
			return "", fmt.Sprintf("解析路径失败: %v", err)
		}
		return abs, ""
	}
	// 节点绝对路径模式（与 BrowseDir 一致）
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return "", "必须是绝对路径"
	}
	return clean, ""
}

// fileOwnerGroup 尽量解析属主/组名；失败返回空或数字串。平台相关实现见 perm_owner_*.go。
func fileOwnerGroup(st os.FileInfo) (owner, group string) {
	return ownerGroupFromFileInfo(st)
}

// ownerGroupFromFileInfo 由构建标签分发；默认空实现保证 Windows/无 syscall 也编译。
var ownerGroupFromFileInfo = ownerGroupStub

func ownerGroupStub(st os.FileInfo) (string, string) {
	_ = st
	if runtime.GOOS == "windows" {
		return "", ""
	}
	return "", ""
}

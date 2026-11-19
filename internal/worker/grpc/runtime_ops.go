package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	wruntime "github.com/wcpe/JianManager/internal/worker/runtime"
	"github.com/wcpe/JianManager/internal/worker/jdk"
	"github.com/wcpe/JianManager/internal/worker/runtimescan"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// SetRuntimeScanner 注入运行时扫描器（节点运行时库）。
// 由 main 装配；不注入则 ScanRuntimes 返回空候选（向后兼容）。
func (s *Server) SetRuntimeScanner(sc *runtimescan.Scanner) {
	s.runtimeScanner = sc
}

// SetRuntimeManager 注入非 JDK 运行时安装管理器（首批 Node.js）。
// 由 main 装配；不注入则 InstallRuntime/RemoveRuntime 返回明确错误。
func (s *Server) SetRuntimeManager(m *wruntime.Manager) {
	s.runtimeMgr = m
}

// ScanRuntimes 扫描节点常见安装路径发现运行时候选（jdk/nodejs）。
// 路径不存在/探测失败静默跳过；托管根下的候选标 already_registered，
// CP 侧另按 DB 已登记路径补标。
func (s *Server) ScanRuntimes(ctx context.Context, req *workerpb.ScanRuntimesRequest) (*workerpb.ScanRuntimesResponse, error) {
	if s.runtimeScanner == nil {
		return &workerpb.ScanRuntimesResponse{}, nil
	}
	cands := s.runtimeScanner.Scan(req.Types)
	out := make([]*workerpb.RuntimeCandidate, 0, len(cands))
	for _, c := range cands {
		out = append(out, &workerpb.RuntimeCandidate{
			Type:              c.Type,
			Vendor:            c.Vendor,
			Version:           c.Version,
			MajorVersion:      int32(c.Major),
			Arch:              c.Arch,
			Path:              c.Path,
			AlreadyRegistered: c.AlreadyRegistered,
		})
	}
	return &workerpb.ScanRuntimesResponse{Candidates: out}, nil
}

// ProbeRuntime 探测指定路径上的运行时（jdk/nodejs）。
//
// 【与 ScanRuntimes 的分工】Scan 只在硬编码的常见目录里找；用户把运行时装在别处时
// 扫不到，但他自己知道路径。登记接口又要求提供 version 等字段，让用户手填版本号
// 既容易错也没必要——本 RPC 就是"给一个路径，读出它是什么版本"。
//
// 【失败语义】节点上路径不对是**常规输入错误**，不是 RPC 异常：
// 返回 valid=false + error 让界面直接展示，调用方据此区分
// "节点失联"（RPC error）与"这个路径不对"（valid=false）。
func (s *Server) ProbeRuntime(ctx context.Context, req *workerpb.ProbeRuntimeRequest) (*workerpb.ProbeRuntimeResponse, error) {
	typ := strings.ToLower(strings.TrimSpace(req.GetType()))
	path := strings.TrimSpace(req.GetPath())
	if typ == "" || path == "" {
		return &workerpb.ProbeRuntimeResponse{Valid: false, Error: "缺少 type 或 path"}, nil
	}

	switch typ {
	case runtimescan.TypeNodeJS:
		if s.runtimeScanner == nil {
			return &workerpb.ProbeRuntimeResponse{Valid: false, Error: "节点未启用运行时扫描能力"}, nil
		}
		c, ok := s.runtimeScanner.ProbePath(typ, path)
		if !ok {
			return &workerpb.ProbeRuntimeResponse{
				Valid: false,
				Error: fmt.Sprintf("该路径不是有效的 Node.js：未找到可执行的 node，或无法读取版本（%s）", path),
			}, nil
		}
		return &workerpb.ProbeRuntimeResponse{
			Valid: true,
			Candidate: &workerpb.RuntimeCandidate{
				Type:         c.Type,
				Vendor:       c.Vendor,
				Version:      c.Version,
				MajorVersion: int32(c.Major),
				Arch:         c.Arch,
				Path:         c.Path,
			},
		}, nil
	case "jdk":
		/*
			JDK 的探测用 Manager.Probe 而不是包级 Detect：
			Probe 会先 normalizeJDKHome，因此用户贴 home、贴 bin/java、贴 java 都能认；
			Detect 只接受 home 目录，贴 bin/java 会失败。
			Probe 不读实例状态，零值 Manager 即可（probe_test.go 里也是这么用的），
			不必为了探测去装配带托管根的实例。
		*/
		info, err := (&jdk.Manager{}).Probe(path)
		if err != nil {
			return &workerpb.ProbeRuntimeResponse{Valid: false, Error: err.Error()}, nil
		}
		return &workerpb.ProbeRuntimeResponse{
			Valid: true,
			Candidate: &workerpb.RuntimeCandidate{
				Type:         "jdk",
				Vendor:       info.Vendor,
				Version:      info.Version,
				MajorVersion: int32(info.MajorVersion),
				Arch:         info.Arch,
				Path:         info.Path,
			},
		}, nil
	default:
		return &workerpb.ProbeRuntimeResponse{
			Valid: false,
			Error: fmt.Sprintf("不支持探测的运行时类型：%s", typ),
		}, nil
	}
}

// runtimeResult 是 runtime_install 任务成功时写入 TaskSnapshot.result 的 JSON 结构。
// CP 收到终态 succeeded 时反序列化它落一条 model.NodeRuntime（managed=true）。
type runtimeResult struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Major   int    `json:"major"`
	Arch    string `json:"arch"`
	Path    string `json:"path"`
	Managed bool   `json:"managed"`
}

// InstallRuntime 下载并安装指定运行时（首批 nodejs）。
//
// 仅支持异步任务路径（req.TaskId 必填，语义同 InstallJDK）：登记内存任务表、
// 启动后台 goroutine 下载解压（经进度回调更新任务表），RPC 立即返回；
// 进度/日志/终态经心跳上报 CP，CP 据终态落 NodeRuntime + 发站内信。
// 强制停止时由心跳侧 registry.Cancel 真中断下载 context。
func (s *Server) InstallRuntime(ctx context.Context, req *workerpb.InstallRuntimeRequest) (*workerpb.InstallRuntimeResponse, error) {
	if s.runtimeMgr == nil {
		return &workerpb.InstallRuntimeResponse{Success: false, Error: "运行时管理器未启用"}, nil
	}
	if strings.ToLower(strings.TrimSpace(req.Type)) != "nodejs" {
		return &workerpb.InstallRuntimeResponse{Success: false, Error: fmt.Sprintf("不支持的运行时类型: %q（当前仅支持 nodejs）", req.Type)}, nil
	}
	if req.TaskId == "" {
		return &workerpb.InstallRuntimeResponse{Success: false, Error: "task_id 必填（运行时安装仅支持异步任务路径）"}, nil
	}

	taskID := req.TaskId
	taskCtx, cancel := context.WithCancel(context.Background())
	s.tasks.Start(taskID, cancel)
	// 复制下发参数，goroutine 不持有 req（避免在 RPC 返回后引用其底层内存）。
	major, arch, mirrorBase := int(req.Major), req.Arch, req.MirrorBase
	go func() {
		defer cancel()
		info, err := s.runtimeMgr.InstallNodeJS(taskCtx, major, arch, mirrorBase,
			func(percent int, line string) {
				s.tasks.SetProgress(taskID, int32(percent))
				if line != "" {
					s.tasks.AppendLog(taskID, line)
				}
			})
		if err != nil {
			s.tasks.AppendLog(taskID, "安装失败: "+err.Error())
			s.tasks.Fail(taskID, err.Error())
			return
		}
		result, err := json.Marshal(runtimeResult{
			Type:    info.Type,
			Name:    info.Name,
			Version: info.Version,
			Major:   info.Major,
			Arch:    info.Arch,
			Path:    info.Path,
			Managed: info.Managed,
		})
		if err != nil {
			s.tasks.Fail(taskID, "序列化运行时安装结果失败: "+err.Error())
			return
		}
		s.tasks.Succeed(taskID, string(result))
	}()
	return &workerpb.InstallRuntimeResponse{Success: true, TaskId: taskID}, nil
}

// RemoveRuntime 删除托管运行时目录（删除顶层清理语义同 RemoveJDK）。
func (s *Server) RemoveRuntime(ctx context.Context, req *workerpb.RemoveRuntimeRequest) (*workerpb.RemoveRuntimeResponse, error) {
	if s.runtimeMgr == nil {
		return &workerpb.RemoveRuntimeResponse{Success: false, Error: "运行时管理器未启用"}, nil
	}
	if strings.ToLower(strings.TrimSpace(req.Type)) != "nodejs" {
		return &workerpb.RemoveRuntimeResponse{Success: false, Error: fmt.Sprintf("不支持的运行时类型: %q（当前仅支持 nodejs）", req.Type)}, nil
	}
	if err := s.runtimeMgr.Remove(req.Path); err != nil {
		return &workerpb.RemoveRuntimeResponse{Success: false, Error: err.Error()}, nil
	}
	return &workerpb.RemoveRuntimeResponse{Success: true}, nil
}

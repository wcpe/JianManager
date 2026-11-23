package grpc

import (
	"context"
	"errors"
	"log/slog"

	"github.com/wcpe/JianManager/internal/platform/taillog"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// selfLogPath 返回 Worker 进程自身日志文件路径。
//
// 由 apps/worker 在日志装配时注入（见 installProcessLogger）。
// 为空表示未启用文件日志（例如日志目录不可写），此时 PullSelfLog 返回结构化错误
// 而不是空成功——“没有日志”和“读不到日志”必须能区分。
var selfLogPath = func() string { return "" }

// SetSelfLogPathProvider 注入日志路径提供者。由 main 在装配完成后调用一次。
func SetSelfLogPathProvider(fn func() string) {
	if fn != nil {
		selfLogPath = fn
	}
}

// PullSelfLog 返回 Worker 自身进程日志的尾部若干行。
//
// # 边界（与日志检索严格区分）
//
// 这不是日志查询：不落存储、不进 VL、不做关键字/时间窗/分页。
// 它只回答“这个节点最近发生了什么”，因为 Worker 的 journald 或 local 文件
// 在 CP 侧完全不可见（Worker 不监听端口，CP 读不到它的磁盘）。
//
// # 为什么上限必须在这里夹紧
//
// 请求来自浏览器（经 CP 转发），不信任 max_lines/max_bytes；
// taillog.Read 会把超限值夹到硬上限，保证一次请求不会把 GB 级日志读进内存。
func (s *Server) PullSelfLog(ctx context.Context, req *workerpb.PullSelfLogRequest) (*workerpb.PullSelfLogResponse, error) {
	path := selfLogPath()
	if path == "" {
		return &workerpb.PullSelfLogResponse{
			ErrorMsg: "未启用进程日志文件（日志目录不可写？）",
		}, nil
	}

	maxLines := int(req.GetMaxLines())
	maxBytes := int(req.GetMaxBytes())
	res, err := taillog.Read(path, maxLines, maxBytes)
	if err != nil {
		// 文件不存在不是 RPC 错误：如实回给调用方，让它能展示“尚未产生日志”。
		if errors.Is(err, taillog.ErrFileMissing) {
			return &workerpb.PullSelfLogResponse{
				Path:     path,
				ErrorMsg: "日志文件尚未创建（进程可能刚启动）",
			}, nil
		}
		slog.Warn("读取进程日志尾部失败", "path", path, "error", err)
		return &workerpb.PullSelfLogResponse{
			Path:     path,
			ErrorMsg: "读取日志失败",
		}, nil
	}

	return &workerpb.PullSelfLogResponse{
		Content:   res.Content,
		Truncated: res.Truncated,
		FileBytes: res.FileBytes,
		Path:      res.Path,
	}, nil
}

package grpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wcpe/JianManager/proto/workerpb"
)

// FetchBinary 把通用二进制制品取到实例工作目录并置可执行位（FR-441 通用二进制运行时搭建）。
//
// 与 DownloadCore 的分工：DownloadCore 只服务 MC 核心 jar（绑定节点核心缓存与组合键），
// 本 RPC 服务 coreType=binary 的两类 Worker 侧来源：
//   - url：Worker 直连远程下载（避免 CP 中转大文件），可选 sha256 校验；
//   - node_file：节点本地文件就地复制，路径须落在节点受控区/放行根内。
//
// asset 来源由 CP 签发短期 token 转成 url 形态下发（复用制品库签名分发通道），
// 故本 RPC 无需感知制品库语义。
//
// 流式返回（服务端流）：二进制体积可达数十 MB、慢链路分钟级，周期性上报字节进度使前端
// 能区分「在下载」与「卡住」；末帧（done）携带终态结果。**末帧缺失即视为失败**——
// 调用方绝不能把「流断了」当成功，否则半截文件会被当成完整二进制落地。
func (s *Server) FetchBinary(req *workerpb.FetchBinaryRequest, stream workerpb.WorkerService_FetchBinaryServer) error {
	inst, exists := s.manager.GetInstance(req.InstanceUuid)
	if !exists {
		return stream.Send(fetchBinaryFailure(fmt.Sprintf("实例 %s 未注册", req.InstanceUuid)))
	}

	dest := strings.TrimSpace(req.DestFilename)
	if !safePlainFilename(dest) {
		return stream.Send(fetchBinaryFailure("非法的目标文件名"))
	}
	ctx := stream.Context()
	target := filepath.Join(inst.WorkDir, dest)

	switch strings.ToLower(strings.TrimSpace(req.SourceKind)) {
	case "url":
		return s.fetchBinaryFromURL(ctx, req, stream, target, dest)
	case "node_file":
		return s.fetchBinaryFromLocalFile(req, stream, target, dest)
	case "":
		return stream.Send(fetchBinaryFailure("缺少来源类型（url | node_file）"))
	default:
		return stream.Send(fetchBinaryFailure(fmt.Sprintf("未知来源类型: %q（url | node_file）", req.SourceKind)))
	}
}

// fetchBinaryFromURL 远程下载到工作目录并（可选）校验摘要；进度经流上报。
func (s *Server) fetchBinaryFromURL(ctx context.Context, req *workerpb.FetchBinaryRequest, stream workerpb.WorkerService_FetchBinaryServer, target, dest string) error {
	if strings.TrimSpace(req.DownloadUrl) == "" {
		return stream.Send(fetchBinaryFailure("下载地址为空"))
	}
	want := strings.ToLower(strings.TrimSpace(req.Sha256))

	slog.Info("二进制取件开始", "instance", req.InstanceUuid, "url", req.DownloadUrl, "dest", dest)
	start := time.Now()
	size, sum, err := downloadBinaryWithProgress(ctx, s.outboundClient(), req.DownloadUrl, target, func(downloaded, total int64) {
		// 进度帧发送失败（客户端已断开）不阻断下载本身：调用方据末帧缺失判定失败，
		// 在这里中断反而会把「前端断开」与「取件失败」混为一谈。
		_ = stream.Send(&workerpb.FetchBinaryProgress{Downloaded: downloaded, Total: total})
	})
	if err != nil {
		slog.Warn("二进制取件失败", "instance", req.InstanceUuid, "url", req.DownloadUrl,
			"elapsed", time.Since(start).Round(time.Second), "error", err)
		return stream.Send(fetchBinaryFailure(err.Error()))
	}
	if want != "" && want != sum {
		_ = os.Remove(target)
		// 校验不符必须删除半成品：留着会被当成有效二进制启动（与 core 下载同一教训）。
		return stream.Send(fetchBinaryFailure(
			fmt.Sprintf("sha256 校验不符：期望 %s 实得 %s", want, sum)))
	}
	if err := applyBinaryExecutable(target, req.Executable); err != nil {
		return stream.Send(fetchBinaryFailure(err.Error()))
	}
	slog.Info("二进制取件完成", "instance", req.InstanceUuid, "size", size,
		"elapsed", time.Since(start).Round(time.Second), "dest", dest)
	return stream.Send(&workerpb.FetchBinaryProgress{
		Done: true, Success: true, Size: size, Sha256: sum, FromLocal: false,
	})
}

// fetchBinaryFromLocalFile 节点本地文件就地复制到工作目录（无网络），可选校验摘要。
//
// 安全边界：路径由 CP 侧按放行根先行校验（CP 是策略真源），Worker 侧再做一次
// 「必须绝对路径 + 必须是常规文件（不跟随符号链接指向的目录）+ 不得是设备/管道等
// 特殊文件」的纵深防御。两级都不放行「任意路径」——即使 CP 配置被改错，Worker 也只
// 复制一个普通文件到工作目录内，不会变成任意读或任意写。
func (s *Server) fetchBinaryFromLocalFile(req *workerpb.FetchBinaryRequest, stream workerpb.WorkerService_FetchBinaryServer, target, dest string) error {
	src := strings.TrimSpace(req.NodePath)
	if src == "" {
		return stream.Send(fetchBinaryFailure("缺少节点本地文件路径"))
	}
	clean := filepath.Clean(src)
	if !filepath.IsAbs(clean) {
		return stream.Send(fetchBinaryFailure("节点本地文件路径必须是绝对路径"))
	}
	// Lstat 而非 Stat：不跟随符号链接判定，避免 `link -> /etc/shadow` 这类把戏
	// 让「常规文件」检查形同虚设。
	st, err := os.Lstat(clean)
	if err != nil {
		return stream.Send(fetchBinaryFailure(fmt.Sprintf("无法访问节点本地文件: %v", err)))
	}
	if !st.Mode().IsRegular() {
		// 显式拒绝符号链接与目录/设备/管道：本 RPC 只做「普通文件的复制」。
		return stream.Send(fetchBinaryFailure("节点本地文件必须是常规文件（不接受符号链接、目录或特殊文件）"))
	}

	slog.Info("二进制取件开始（节点本地）", "instance", req.InstanceUuid, "src", clean, "dest", dest)
	size, sum, err := copyBinaryFromFile(clean, target, func(downloaded, total int64) {
		_ = stream.Send(&workerpb.FetchBinaryProgress{Downloaded: downloaded, Total: total})
	})
	if err != nil {
		return stream.Send(fetchBinaryFailure(err.Error()))
	}
	want := strings.ToLower(strings.TrimSpace(req.Sha256))
	if want != "" && want != sum {
		_ = os.Remove(target)
		return stream.Send(fetchBinaryFailure(fmt.Sprintf("sha256 校验不符：期望 %s 实得 %s", want, sum)))
	}
	if err := applyBinaryExecutable(target, req.Executable); err != nil {
		return stream.Send(fetchBinaryFailure(err.Error()))
	}
	slog.Info("二进制取件完成（节点本地）", "instance", req.InstanceUuid, "size", size, "dest", dest)
	return stream.Send(&workerpb.FetchBinaryProgress{
		Done: true, Success: true, Size: size, Sha256: sum, FromLocal: true,
	})
}

// fetchBinaryFailure 构造一个「终态失败」帧：done + success=false + 原因。
func fetchBinaryFailure(reason string) *workerpb.FetchBinaryProgress {
	return &workerpb.FetchBinaryProgress{Done: true, Success: false, Error: reason}
}

// binaryProgressStride 是进度上报的字节步长。取 1MiB：约 30MB 的二进制约 30 帧，
// 既让进度条平滑，又不至于让 gRPC 流与任务日志被逐帧刷屏。
const binaryProgressStride = 1 << 20

// downloadBinaryWithProgress 下载到 target（临时文件 + 原子 rename），边下边算 sha256，
// 并周期性回调字节进度。任何失败都删除临时文件，不留半截目标。
func downloadBinaryWithProgress(ctx context.Context, client *http.Client, url, target string, onProgress func(downloaded, total int64)) (int64, string, error) {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return 0, "", fmt.Errorf("创建工作目录失败: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, "", err
	}
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Minute}
	} else if client.Timeout == 0 {
		// 工厂 client 默认不设整体超时；为二进制下载补一个上限（不改原 client）。
		c := *client
		c.Timeout = 20 * time.Minute
		client = &c
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return 0, "", fmt.Errorf("下载二进制失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0, "", fmt.Errorf("下载二进制返回 HTTP %d", resp.StatusCode)
	}

	// 先写 .part、校验通过后原子 rename：目标路径要么没有、要么是完整文件。
	// 否则慢源下载的几分钟窗口内躺着半截可执行文件，用户点启动即得
	// `cannot execute binary file`（与 core 下载同一真机教训）。
	tmpPath := target + ".part"
	f, err := os.Create(tmpPath)
	if err != nil {
		return 0, "", fmt.Errorf("创建文件失败: %w", err)
	}

	h := sha256.New()
	size, copyErr := copyWithProgress(io.MultiWriter(f, h), resp.Body, resp.ContentLength, onProgress)
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(tmpPath)
		return 0, "", fmt.Errorf("写入二进制失败: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		return 0, "", fmt.Errorf("关闭二进制文件失败: %w", closeErr)
	}
	if resp.ContentLength >= 0 && size != resp.ContentLength {
		_ = os.Remove(tmpPath)
		return 0, "", fmt.Errorf("二进制下载不完整：期望 %d 字节实得 %d 字节", resp.ContentLength, size)
	}
	if err := os.Rename(tmpPath, target); err != nil {
		_ = os.Remove(tmpPath)
		return 0, "", fmt.Errorf("落位二进制文件失败: %w", err)
	}
	return size, hex.EncodeToString(h.Sum(nil)), nil
}

// copyBinaryFromFile 就地复制本地文件到 target（同样「临时文件 + 原子 rename」），
// 边拷边算 sha256 并回调进度。用于 node_file 来源。
func copyBinaryFromFile(src, target string, onProgress func(downloaded, total int64)) (int64, string, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, "", fmt.Errorf("打开节点本地文件失败: %w", err)
	}
	defer func() { _ = in.Close() }()
	st, err := in.Stat()
	if err != nil {
		return 0, "", fmt.Errorf("读取节点本地文件信息失败: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return 0, "", fmt.Errorf("创建工作目录失败: %w", err)
	}

	tmpPath := target + ".part"
	out, err := os.Create(tmpPath)
	if err != nil {
		return 0, "", fmt.Errorf("创建文件失败: %w", err)
	}
	h := sha256.New()
	size, copyErr := copyWithProgress(io.MultiWriter(out, h), in, st.Size(), onProgress)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(tmpPath)
		return 0, "", fmt.Errorf("复制节点本地文件失败: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		return 0, "", fmt.Errorf("关闭目标文件失败: %w", closeErr)
	}
	if size != st.Size() {
		// 源文件在复制期间被改写会导致字节数不符；不静默接受（可能拷到半截二进制）。
		_ = os.Remove(tmpPath)
		return 0, "", fmt.Errorf("复制不完整：源 %d 字节，实得 %d 字节（源文件可能在复制期间被改写）", st.Size(), size)
	}
	if err := os.Rename(tmpPath, target); err != nil {
		_ = os.Remove(tmpPath)
		return 0, "", fmt.Errorf("落位二进制文件失败: %w", err)
	}
	return size, hex.EncodeToString(h.Sum(nil)), nil
}

// copyWithProgress 把 src 拷到 dst，按 binaryProgressStride 回调进度（total<=0 表示未知长度）。
func copyWithProgress(dst io.Writer, src io.Reader, total int64, onProgress func(downloaded, total int64)) (int64, error) {
	buf := make([]byte, 256<<10)
	var written int64
	lastReported := int64(0)
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return written, werr
			}
			written += int64(n)
			if onProgress != nil && written-lastReported >= binaryProgressStride {
				lastReported = written
				onProgress(written, total)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return written, rerr
		}
	}
	if onProgress != nil {
		onProgress(written, total)
	}
	return written, nil
}

// applyBinaryExecutable 按请求设置可执行位（executable=true，或未显式指定时的默认 true）。
// Windows 上 os.Chmod 对可执行位无实义，失败不阻断（平台本身按扩展名/内容判定可执行性）。
func applyBinaryExecutable(path string, executable bool) error {
	if !executable {
		return nil
	}
	st, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("读取文件权限失败: %w", err)
	}
	mode := st.Mode().Perm() | 0o755
	if err := os.Chmod(path, mode); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("设置可执行位失败（Worker 用户不是属主或文件系统不支持 chmod）: %w", err)
		}
		return fmt.Errorf("设置可执行位失败: %w", err)
	}
	return nil
}

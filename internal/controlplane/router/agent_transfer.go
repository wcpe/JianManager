package router

import (
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// AgentTransferHandler 流式传输票据数据面（FR-397）。
//
// 控制与数据分离：MCP 只承载小文本，大文件经此端点走既有流式链路。
// 票据即凭据——本端点不挂 Agent 鉴权中间件，也不接受任何路径/实例参数：
// 授权上下文全部来自票据 claims，因此没有参数注入面。
type AgentTransferHandler struct {
	tickets    *service.AgentTransferTicketService
	fileSvc    *service.FileService
	versionSvc *service.FileVersionService
}

// NewAgentTransferHandler 创建传输票据数据面处理器。
// tickets 为 nil 时（未配置主密钥）端点返回功能不可用。
func NewAgentTransferHandler(
	tickets *service.AgentTransferTicketService,
	fileSvc *service.FileService,
	versionSvc *service.FileVersionService,
) *AgentTransferHandler {
	return &AgentTransferHandler{tickets: tickets, fileSvc: fileSvc, versionSvc: versionSvc}
}

// consume 校验并一次性消费票据，同时校验方向是否与端点匹配。
// 所有失败形态统一返回 403 与同一句中文，不泄露票据的具体失效原因。
func (h *AgentTransferHandler) consume(c *gin.Context, wantDirection string) (*service.AgentTransferClaims, bool) {
	if h.tickets == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "UNAVAILABLE", "message": "传输票据功能不可用（未配置服务端主密钥）",
		})
		return nil, false
	}
	claims, err := h.tickets.Consume(c.Query("ticket"))
	if err != nil || claims.Direction != wantDirection {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "FORBIDDEN", "message": service.ErrAgentTransferTicketInvalid.Error(),
		})
		return nil, false
	}
	return claims, true
}

// Upload 票据上传：请求体为原始字节流，经 FileService 流式转发到 Worker（内存 O(chunk)）。
// 覆盖已存在文件前做改前快照，与管理面上传语义一致（FR-051）。
func (h *AgentTransferHandler) Upload(c *gin.Context) {
	claims, ok := h.consume(c, service.AgentTransferDirectionUpload)
	if !ok {
		return
	}
	// 作者 ID 取 0：写入方为 Agent Token 而非平台用户，归属已由调用流水记录。
	if err := h.versionSvc.SnapshotBeforeWrite(claims.InstanceID, claims.Path, 0); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}
	if err := h.fileSvc.UploadFile(c.Request.Context(), claims.InstanceID, claims.Path, c.Request.Body); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已上传", "path": claims.Path})
}

// Download 票据下载：逐帧 Recv Worker 流并写响应体，任意大小不截断。
func (h *AgentTransferHandler) Download(c *gin.Context) {
	claims, ok := h.consume(c, service.AgentTransferDirectionDownload)
	if !ok {
		return
	}
	stream, err := h.fileSvc.DownloadFile(c.Request.Context(), claims.InstanceID, claims.Path)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}
	// 先收首帧再写头：打开失败/越界/目录仍能返回 JSON 错误而非半截文件。
	first, err := stream.Recv()
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error": "BUSINESS_ERROR", "message": downloadFirstFrameMessage(err),
		})
		return
	}
	writeTransferStream(c, first.TotalSize, first.Content, stream.Recv)
}

// downloadFirstFrameMessage 把首帧错误翻译为可操作的中文提示。
func downloadFirstFrameMessage(err error) string {
	switch {
	case status.Code(err) == codes.Unimplemented:
		return "节点 Worker 版本过旧，不支持大文件流式下载，请先升级节点"
	case err == io.EOF:
		return "下载流异常结束"
	default:
		return err.Error()
	}
}

// transferChunk 是下载流后续帧的最小读取契约，便于与 Worker 流解耦。
type transferChunk interface {
	GetContent() []byte
}

// writeTransferStream 写出下载响应：显式 Content-Length 让客户端可校验完整性——
// 流中途失败时收到字节数与之不符，客户端按下载失败处理而非拿到静默截断的文件。
func writeTransferStream[T transferChunk](c *gin.Context, totalSize int64, first []byte, next func() (T, error)) {
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Length", strconv.FormatInt(totalSize, 10))
	c.Status(http.StatusOK)
	flusher, _ := c.Writer.(http.Flusher)

	writeChunk := func(b []byte) bool {
		if len(b) == 0 {
			return true
		}
		if _, err := c.Writer.Write(b); err != nil {
			return false // 客户端断开
		}
		if flusher != nil {
			flusher.Flush()
		}
		return true
	}

	if !writeChunk(first) {
		return
	}
	for {
		chunk, err := next()
		if err != nil {
			// EOF 为正常结束；中途失败时响应头已发出，只能截断连接。
			return
		}
		if !writeChunk(chunk.GetContent()) {
			return
		}
	}
}

// RegisterRoutes 注册票据数据面端点。
// 注意：必须挂在无 Agent/JWT 鉴权的组下——票据自身即完整凭据。
func (h *AgentTransferHandler) RegisterRoutes(rg *gin.RouterGroup) {
	transfer := rg.Group("/agent-transfer")
	{
		transfer.PUT("/upload", h.Upload)
		transfer.GET("/download", h.Download)
	}
}

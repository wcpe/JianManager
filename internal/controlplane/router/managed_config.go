package router

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// ManagedConfigHandler 探针托管配置路由处理器（运营侧下发与对账）。
//
// 职责边界：本 handler 只做 HTTP 编解码与错误映射，全部业务判定在 service 层。
// 三档分级（SAFE/GUARDED/FORBIDDEN）由既有 managed_mode_tiers.go 统一提供，
// 本层不重复实现——两处各写一份分级表必然漂移。
type ManagedConfigHandler struct {
	configSvc *service.ManagedConfigService
	pushSvc   *service.ManagedPushService
	matrixSvc *service.ManagedMatrixService
	authz     *service.AuthzService
	// instances 只用于节点作用域的可见性判定（该节点下是否有对用户可见的实例）。
	// 本层不直接查托管配置，业务读写一律走 service。
	instances *service.InstanceService
}

// NewManagedConfigHandler 创建探针托管配置路由处理器。
func NewManagedConfigHandler(
	configSvc *service.ManagedConfigService,
	pushSvc *service.ManagedPushService,
	matrixSvc *service.ManagedMatrixService,
	authz *service.AuthzService,
	instances *service.InstanceService,
) *ManagedConfigHandler {
	return &ManagedConfigHandler{
		configSvc: configSvc, pushSvc: pushSvc, matrixSvc: matrixSvc,
		authz: authz, instances: instances,
	}
}

// RegisterRoutes 注册托管配置相关端点。
//
// 读端点挂 read 组（instance.read 即可），写端点挂 write 组（必须 instance.write）。
// 两档必须分开注册：RequireAnyPerm 是「任一节点」语义，把写端点也挂进
// instance.read|instance.write 的任一可读组，会让只有读权限的用户改层、推配置。
// 托管配置会改所有被覆盖实例的运行行为，与「改实例配置」同一风险档，故沿用既有节点，
// 不新造权限。
func (h *ManagedConfigHandler) RegisterRoutes(read, write *gin.RouterGroup) {
	// 配置键目录：66 键的类型 / 默认值 / 中文说明 / 取值范围 / 档位。
	// 前端据此把"手写 JSON"换成可视化控件——没有它就只能给用户一个空白文本框。
	read.GET("/managed-configs/catalog", h.Catalog)

	read.GET("/managed-configs", h.ListLayers)
	read.GET("/managed-configs/layer", h.GetLayer)
	write.PUT("/managed-configs", h.SaveLayer)
	write.DELETE("/managed-configs", h.DeleteLayer)
	read.POST("/managed-configs/validate", h.Validate)
	read.POST("/managed-configs/impact", h.EstimateImpact)

	write.POST("/managed-pushes", h.CreatePush)
	write.POST("/managed-pushes/:id/start", h.StartPush)
	write.POST("/managed-pushes/:id/release", h.ReleasePush)
	write.POST("/managed-pushes/:id/pause", h.PausePush)
	write.POST("/managed-pushes/:id/resume", h.ResumePush)
	write.POST("/managed-pushes/:id/cancel", h.CancelPush)
	read.POST("/managed-pushes/:id/targets", h.ListTargets)
	read.GET("/managed-pushes", h.ListPushes)
	write.POST("/managed-pushes/rollback", h.Rollback)

	read.GET("/managed-matrix", h.Matrix)
	read.GET("/managed-matrix/summary", h.MatrixSummary)
	read.GET("/managed-matrix/instances/:id", h.MatrixDetail)
}

// Catalog GET /managed-configs/catalog — 配置键目录（可视化编辑器的数据源）。
//
// 返回全部键的元数据：类型、分组、档位、中文名与说明、默认值、数值边界、可选值。
// 前端按这些信息决定用哪种控件——没有它，编辑器只能退回"手写 JSON 文本框"。
//
// 禁止档键也会返回（前端按 group=local 渲染成只读展示），因为"为什么这个键不能改"
// 本身就是用户需要知道的信息，藏起来只会让人以为是功能缺失。
func (h *ManagedConfigHandler) Catalog(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"items": service.ManagedKeyCatalog(),
		// 分组的中文名放在服务端：前端只负责渲染，不再维护一份翻译映射
		//（两处维护必然漂移，而分组名漂移会让界面出现无法解释的空组）。
		"groups": service.ManagedKeyGroupLabels(),
	})
}

// ListLayers GET /managed-configs?scope= — 列出各层配置。
//
// 不带 scope 时按三层各取一次，前端一次拿全；带 scope 时只取该层。
func (h *ManagedConfigHandler) ListLayers(c *gin.Context) {
	scope := model.ManagedConfigScope(c.Query("scope"))
	scopes := []model.ManagedConfigScope{scope}
	if scope == "" {
		scopes = []model.ManagedConfigScope{
			model.ManagedScopeGlobal,
			model.ManagedScopeNode,
			model.ManagedScopeInstance,
		}
	}
	items := make([]model.ManagedConfigLayer, 0, 8)
	for _, s := range scopes {
		rows, err := h.configSvc.Layers(s)
		if err != nil {
			h.respondConfigError(c, err)
			return
		}
		items = append(items, rows...)
	}
	// 逐行做作用域可见性过滤：与 GetLayer 的口径一致——不可见的层连存在性都不该泄露
	//（scope_name 含节点/实例名，未过滤等于把别组的实例清单拱手送出）。
	visible := items[:0]
	for _, row := range items {
		if h.scopeAccessible(c, row.Scope, row.ScopeRefID) {
			visible = append(visible, row)
		}
	}
	c.JSON(http.StatusOK, gin.H{"items": visible})
}

// GetLayer GET /managed-configs/layer?scope=&scopeRefId= — 取某一层。
func (h *ManagedConfigHandler) GetLayer(c *gin.Context) {
	scope := model.ManagedConfigScope(c.Query("scope"))
	refID := uint(atoiDefault(c.Query("scopeRefId"), 0))
	if !h.scopeAccessible(c, scope, refID) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "作用域不存在"})
		return
	}
	row, found, err := h.configSvc.GetLayer(scope, refID)
	if err != nil {
		h.respondConfigError(c, err)
		return
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "该层尚未配置"})
		return
	}
	c.JSON(http.StatusOK, row)
}

// SaveLayer PUT /managed-configs — 写入某一层（整体替换该层增量）。
func (h *ManagedConfigHandler) SaveLayer(c *gin.Context) {
	var in service.ManagedLayerInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	if !h.scopeWritable(c, in.Scope, in.ScopeRefID) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "作用域不存在"})
		return
	}
	row, err := h.configSvc.SaveLayer(in, currentUserID(c))
	if err != nil {
		h.respondConfigError(c, err)
		return
	}
	c.JSON(http.StatusOK, row)
}

// DeleteLayer DELETE /managed-configs?scope=&scopeRefId= — 删除某一层。
func (h *ManagedConfigHandler) DeleteLayer(c *gin.Context) {
	scope := model.ManagedConfigScope(c.Query("scope"))
	refID := uint(atoiDefault(c.Query("scopeRefId"), 0))
	if !h.scopeWritable(c, scope, refID) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "作用域不存在"})
		return
	}
	if err := h.configSvc.DeleteLayer(scope, refID); err != nil {
		h.respondConfigError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

// managedConfigTextRequest 承载一段 JSON 配置文本（校验与影响预演共用）。
type managedConfigTextRequest struct {
	Scope      model.ManagedConfigScope `json:"scope"`
	ScopeRefID uint                     `json:"scopeRefId"`
	// Config 嵌套对象 JSON 文本；显式 null 表示删键。
	Config string `json:"config"`
}

// Validate POST /managed-configs/validate — 只校验不落库（编辑器实时校验用）。
func (h *ManagedConfigHandler) Validate(c *gin.Context) {
	var req managedConfigTextRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	values, err := decodeManagedConfigText(req.Config)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_CONFIG", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, h.configSvc.Validate(values))
}

// EstimateImpact POST /managed-configs/impact — 预演本次改动波及谁、放大多少数据量。
//
// 这是发布前的刹车：改 GUARDED 键（如采集周期）会成倍放大指标量，运维必须事先看到。
func (h *ManagedConfigHandler) EstimateImpact(c *gin.Context) {
	var req managedConfigTextRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	values, err := decodeManagedConfigText(req.Config)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_CONFIG", "message": err.Error()})
		return
	}
	if !h.scopeAccessible(c, req.Scope, req.ScopeRefID) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "作用域不存在"})
		return
	}
	impact, err := h.configSvc.EstimateImpact(req.Scope, req.ScopeRefID, values)
	if err != nil {
		h.respondConfigError(c, err)
		return
	}
	c.JSON(http.StatusOK, impact)
}

// CreatePush POST /managed-pushes — 建下发会话（不自动开始，需显式 start）。
func (h *ManagedConfigHandler) CreatePush(c *gin.Context) {
	var in service.ManagedPushInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	// 作用域本身也要过可见性：InstanceIDs 为空时服务按 scope 展开目标，
	// 不校验 scope 等于让只能管自己组的运营建出 global 全平台推送。
	if !h.scopeWritable(c, in.Scope, in.ScopeRefID) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "作用域不存在"})
		return
	}
	// 逐台收敛到用户可见集合，只要有一台不可见就整包拒绝。
	// 刻意不做「静默丢弃不可见的」：下发会话是运维对一批机器的承诺，少发一半却回 200
	// 会让人以为全平台已推进——这种半成功比直接失败更难排查。
	kept, denied := h.filterAccessibleInstances(c, in.InstanceIDs)
	if len(denied) > 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "部分实例不存在"})
		return
	}
	in.InstanceIDs = kept
	session, err := h.pushSvc.Create(in, currentUserID(c))
	if err != nil {
		h.respondPushError(c, err)
		return
	}
	c.JSON(http.StatusOK, session)
}

// StartPush POST /managed-pushes/:id/start — 开始推第一批。
func (h *ManagedConfigHandler) StartPush(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if !h.pushSessionAccessible(c, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "会话不存在"})
		return
	}
	h.pushSvc.Start(id)
	c.JSON(http.StatusOK, gin.H{"started": true})
}

// ReleasePush POST /managed-pushes/:id/release — 放行下一批（观察窗后人工确认）。
func (h *ManagedConfigHandler) ReleasePush(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if !h.pushSessionAccessible(c, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "会话不存在"})
		return
	}
	session, err := h.pushSvc.Release(id)
	if err != nil {
		h.respondPushError(c, err)
		return
	}
	c.JSON(http.StatusOK, session)
}

// managedReasonRequest 承载可读原因（暂停/取消/回滚都要求可解释）。
type managedReasonRequest struct {
	Reason string `json:"reason"`
}

// PausePush POST /managed-pushes/:id/pause — 暂停推进（不自动回滚）。
func (h *ManagedConfigHandler) PausePush(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	var req managedReasonRequest
	_ = c.ShouldBindJSON(&req)
	if !h.pushSessionAccessible(c, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "会话不存在"})
		return
	}
	session, err := h.pushSvc.Pause(id, req.Reason)
	if err != nil {
		h.respondPushError(c, err)
		return
	}
	c.JSON(http.StatusOK, session)
}

// ResumePush POST /managed-pushes/:id/resume — 恢复推进。
func (h *ManagedConfigHandler) ResumePush(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if !h.pushSessionAccessible(c, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "会话不存在"})
		return
	}
	session, err := h.pushSvc.Resume(id)
	if err != nil {
		h.respondPushError(c, err)
		return
	}
	c.JSON(http.StatusOK, session)
}

// CancelPush POST /managed-pushes/:id/cancel — 取消会话（不自动回滚已生效目标）。
func (h *ManagedConfigHandler) CancelPush(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	var req managedReasonRequest
	_ = c.ShouldBindJSON(&req)
	if !h.pushSessionAccessible(c, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "会话不存在"})
		return
	}
	session, err := h.pushSvc.Cancel(id, req.Reason)
	if err != nil {
		h.respondPushError(c, err)
		return
	}
	c.JSON(http.StatusOK, session)
}

// ListTargets POST /managed-pushes/:id/targets — 分页查该会话的逐台目标状态。
func (h *ManagedConfigHandler) ListTargets(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	// 目标行含逐台下发的载荷原文（各实例 node/instance 层合并值），
	// 跨组会话的载荷不能让只看得见部分目标的运营整页读出。
	if !h.pushSessionAccessible(c, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "会话不存在"})
		return
	}
	state := c.Query("state")
	batchNo := atoiDefault(c.Query("batchNo"), 0)
	page := atoiDefault(c.Query("page"), 1)
	pageSize := atoiDefault(c.Query("pageSize"), 20)
	items, total, err := h.pushSvc.Targets(id, state, batchNo, page, pageSize)
	if err != nil {
		h.respondPushError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total, "page": page, "pageSize": pageSize})
}

// ListPushes GET /managed-pushes — 分页列出下发会话。
//
// ## 为什么这一条迟到了
// `ManagedPushService.List` 一直有实现，但路由漏注册。前端因此不得不降级成
// 「只显示本次访问内创建或推进过的会话」，刷新页面就看不到历史——用户在页面上
// 被告知"后端未注册该接口"，而这本来是后端该补齐的一条。
func (h *ManagedConfigHandler) ListPushes(c *gin.Context) {
	status := c.Query("status")
	keyword := c.Query("keyword")
	page := atoiDefault(c.Query("page"), 1)
	pageSize := atoiDefault(c.Query("pageSize"), 20)
	items, total, err := h.pushSvc.List(status, keyword, page, pageSize)
	if err != nil {
		h.respondPushError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total, "page": page, "pageSize": pageSize})
}

// Rollback POST /managed-pushes/rollback — 回滚（重推上一次 completed 的冻结快照）。
//
// 必须带原因；不做自动回滚——自动回滚比自动熔断更危险，它会把刚验证过的目标
// 改回未经本轮验证的状态。
func (h *ManagedConfigHandler) Rollback(c *gin.Context) {
	var in service.ManagedRollbackInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	// 未显式给实例时，服务会回放源会话的全部目标——必须先按可见性整包校验，
	// 否则受限运营能把配置回滚推到自己看不见的实例上。
	targetIDs := in.InstanceIDs
	if len(targetIDs) == 0 {
		var terr error
		targetIDs, terr = h.pushSvc.TargetInstanceIDs(in.SourceSessionID)
		if terr != nil {
			h.respondPushError(c, terr)
			return
		}
	}
	kept, denied := h.filterAccessibleInstances(c, targetIDs)
	if len(denied) > 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "部分实例不存在"})
		return
	}
	in.InstanceIDs = kept
	session, err := h.pushSvc.Rollback(in, currentUserID(c))
	if err != nil {
		h.respondPushError(c, err)
		return
	}
	c.JSON(http.StatusOK, session)
}

// Matrix GET /managed-matrix — 对账矩阵（期望 vs 观测）。
func (h *ManagedConfigHandler) Matrix(c *gin.Context) {
	filter := h.matrixFilter(c)
	rows, total, err := h.matrixSvc.Matrix(filter)
	if err != nil {
		h.respondConfigError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": rows, "total": total, "page": filter.Page, "pageSize": filter.PageSize})
}

// MatrixSummary GET /managed-matrix/summary — 各一致性档位的计数（页头与筛选芯片用）。
//
// 注意与 Matrix 的口径差异：summary 不受 consistency 筛选影响，否则点进「漂移」后
// 其它档位全变 0，会被误读成「问题都解决了」。
func (h *ManagedConfigHandler) MatrixSummary(c *gin.Context) {
	filter := h.matrixFilter(c)
	filter.Consistency = ""
	summary, err := h.matrixSvc.Summary(filter)
	if err != nil {
		h.respondConfigError(c, err)
		return
	}
	c.JSON(http.StatusOK, summary)
}

// MatrixDetail GET /managed-matrix/instances/:id — 单实例的期望/观测明细与逐键来源。
func (h *ManagedConfigHandler) MatrixDetail(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	// 明细会返回该实例真实生效的配置原文，跨组可见等于泄露别人的配置内容，
	// 故与既有实例端点同口径：不可见一律 404（不返回 403，避免探测实例是否存在）。
	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}
	detail, err := h.matrixSvc.Detail(id)
	if err != nil {
		h.respondConfigError(c, err)
		return
	}
	c.JSON(http.StatusOK, detail)
}

// matrixFilter 从查询串构造矩阵筛选条件（各端点共用，避免口径漂移）。
//
// 除用户可见的筛选项外，还注入**实例可见性收敛**：矩阵行含实例名、节点与生效配置版本，
// 不加这一层会让 A 组用户看到 B 组的全部实例清单。收敛值来自授权服务，不是查询参数，
// 前端无法覆盖。
func (h *ManagedConfigHandler) matrixFilter(c *gin.Context) service.ManagedMatrixFilter {
	return service.ManagedMatrixFilter{
		Consistency:   c.Query("consistency"),
		Connection:    c.Query("connection"),
		Configuration: c.Query("configuration"),
		Keyword:       c.Query("keyword"),
		NodeID:        uint(atoiDefault(c.Query("nodeId"), 0)),
		// 直接给 nil 表示不受限（管理员），空切片表示一个都不许看——两者语义不同，
		// 由 accessibleScopes 区分（见其注释）。
		AccessibleInstanceIDs: h.accessibleScopeOrNil(c),
		Page:                  atoiDefault(c.Query("page"), 1),
		PageSize:              atoiDefault(c.Query("pageSize"), 20),
	}
}

// accessibleScopeOrNil 返回调用方可访问的实例 ID 集合；不受限时返回 nil。
//
// 出错时返回空切片（而非 nil）：授权判定失败必须收敛到「什么都看不到」，
// 不能因为一次 DB/授权错误就放开全表。
func (h *ManagedConfigHandler) accessibleScopeOrNil(c *gin.Context) []uint {
	access := getAccess(c)
	if access == nil || h.authz == nil {
		return []uint{}
	}
	ids, all, err := h.authz.AccessibleInstanceIDs(access)
	if err != nil {
		return []uint{}
	}
	// all=false 表示不受限（平台管理员）：返回 nil 即「不过滤」。
	// all=true 才应用 id 集合——两个方向都不得反，反了管理员被关死、受限用户被放开。
	if !all {
		return nil
	}
	if ids == nil {
		return []uint{}
	}
	return ids
}

// decodeManagedConfigText 把前端传来的 JSON 文本解析为配置树。
//
// 空串视为空配置（清空该层）。必须是 JSON **对象**——数组或标量无法参与键级合并，
// 早期就拒绝比让合并阶段产生诡异结果更好。
func decodeManagedConfigText(text string) (map[string]any, error) {
	return service.DecodeManagedConfigObject(text)
}

// respondConfigError 把配置层错误映射为 HTTP 状态码。
func (h *ManagedConfigHandler) respondConfigError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrManagedConfigConflict):
		c.JSON(http.StatusConflict, gin.H{"error": "VERSION_CONFLICT", "message": err.Error()})
	case errors.Is(err, service.ErrManagedConfigNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": err.Error()})
	case errors.Is(err, service.ErrManagedConfigForbidden),
		errors.Is(err, service.ErrManagedConfigInvalid),
		errors.Is(err, service.ErrManagedScopeInvalid),
		errors.Is(err, service.ErrManagedScopeRefInvalid),
		errors.Is(err, service.ErrManagedConfigLayerEmpty):
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_CONFIG", "message": err.Error()})
	case errors.Is(err, service.ErrManagedConfigScopeRefNotFound), errors.Is(err, gorm.ErrRecordNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "目标不存在"})
	default:
		slog.Warn("托管配置操作失败", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": err.Error()})
	}
}

// respondPushError 把下发编排错误映射为 HTTP 状态码。
func (h *ManagedConfigHandler) respondPushError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrManagedPushNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": err.Error()})
	case errors.Is(err, service.ErrManagedPushConfirmRequired):
		c.JSON(http.StatusBadRequest, gin.H{"error": "CONFIRM_REQUIRED", "message": err.Error()})
	case errors.Is(err, service.ErrManagedPushStateInvalid),
		errors.Is(err, service.ErrManagedPushNoTargets):
		c.JSON(http.StatusConflict, gin.H{"error": "PUSH_STATE_INVALID", "message": err.Error()})
	default:
		slog.Warn("下发编排操作失败", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": err.Error()})
	}
}

// atoiDefault 解析整型查询参数，缺失或非法时回退默认值。
func atoiDefault(raw string, def int) int {
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return v
}

// scopeAccessible 校验当前用户能否对某作用域读写托管配置。
//
// 三种作用域的隔离口径不同，任一不满足都必须拒绝：
//   - global：影响全平台每个实例，只有能看见**全部**实例的人才可写（无实例可见性的
//     普通用户不该因为「改的是 global」而绕过实例级授权）；
//   - node：节点下必须至少有一个实例对该用户可见——否则这人本来就管不到那台机器上的
//     任何实例，却能改到它们的配置；
//   - instance：直接走实例级授权，与「改实例配置」同一口径。
//
// 为什么不只看组级权限：`permRead("instance.read","instance.write")` 只回答「这个人有没有
// 实例读/写这个**类别**的权限」，不回答「这个实例归不归他管」。本项目既有端点
// （config.go / config_file.go / probe_update.go）都在 handler 里再做一次实例级收敛，
// 本 handler 与之一致——否则 A 组用户能改到 B 组实例的探针配置并读到其生效配置原文。
func (h *ManagedConfigHandler) scopeAccessible(c *gin.Context, scope model.ManagedConfigScope, refID uint) bool {
	switch scope {
	case model.ManagedScopeGlobal:
		return h.seesAllInstances(c)
	case model.ManagedScopeNode:
		return h.nodeVisible(c, refID)
	case model.ManagedScopeInstance:
		return canAccessInstance(c, h.authz, refID)
	default:
		return false
	}
}

// seesAllInstances 判定用户是否有「全部实例」的可见范围。
//
// 复用 AccessibleInstanceIDs 的 all 语义：all=false（如平台管理员）表示不受限，
// 此时才允许写 global 层；all=true 表示须按实例集合收敛。
func (h *ManagedConfigHandler) seesAllInstances(c *gin.Context) bool {
	access := getAccess(c)
	if access == nil || h.authz == nil {
		return false
	}
	_, all, err := h.authz.AccessibleInstanceIDs(access)
	if err != nil {
		return false
	}
	return !all
}

// nodeVisible 判定用户是否在该节点下至少可见一个实例。
//
// 用「至少一个」而不是「全部」：节点可能承载多个组的实例，运维只对自己的那部分负责，
// 不应因为同节点还有别人的实例就完全失去对该节点的配置权。
func (h *ManagedConfigHandler) nodeVisible(c *gin.Context, nodeID uint) bool {
	access := getAccess(c)
	if access == nil || h.authz == nil || h.instances == nil {
		return false
	}
	ids, all, err := h.authz.AccessibleInstanceIDs(access)
	if err != nil {
		return false
	}
	// all=false 是不受限：节点是否存在由 service 层判定（ManagedScopeRefNotFound），
	// 这里只回答「这个人对这个节点下实例的可见性」，不受限即可见。
	if !all {
		return true
	}
	if len(ids) == 0 {
		return false
	}
	// 逐个按可见集合内的实例判定节点归属；集合通常远小于全表，无需另开查询口径。
	for _, id := range ids {
		inst, err := h.instances.GetByID(id)
		if err != nil || inst == nil {
			continue
		}
		if inst.NodeID == nodeID {
			return true
		}
	}
	return false
}

// nodeFullyVisible 判定节点下**全部**实例对该用户可见。
//
// 与 nodeVisible 的区别是读写口径：node 层是一份共享配置，写到它会合并进节点上
// 每台实例——包括别组的。只看得见一台就能改整层，等于允许 A 组运营通过共享 node 层
// 改到 B 组实例的探针配置。写 node 层必须能管到节点上所有实例（或不受限）。
func (h *ManagedConfigHandler) nodeFullyVisible(c *gin.Context, nodeID uint) bool {
	access := getAccess(c)
	if access == nil || h.authz == nil || h.instances == nil {
		return false
	}
	ids, all, err := h.authz.AccessibleInstanceIDs(access)
	if err != nil {
		return false
	}
	if !all {
		return true
	}
	set := make(map[uint]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	nodeInsts, err := h.instances.List(service.InstanceFilter{NodeID: &nodeID})
	if err != nil || len(nodeInsts) == 0 {
		return false
	}
	for _, inst := range nodeInsts {
		if _, ok := set[inst.ID]; !ok {
			return false
		}
	}
	return true
}

// scopeWritable 写作用域判定：global 同 scopeAccessible；instance 同 scopeAccessible；
// node 层共享配置写入要求节点下实例全部可见（理由见 nodeFullyVisible）。
func (h *ManagedConfigHandler) scopeWritable(c *gin.Context, scope model.ManagedConfigScope, refID uint) bool {
	if scope == model.ManagedScopeNode {
		return h.nodeFullyVisible(c, refID)
	}
	return h.scopeAccessible(c, scope, refID)
}

// pushSessionAccessible 判定会话的所有目标实例对当前用户可见。
//
// 生命周期操作（开始/放行/暂停/恢复/取消/查目标）只对会话目标生效，
// 但目标可能含有其他组的实例：受限用户不应能对跨组会话整体下达指令或读到
// 逐台载荷原文。只要有一台不可见就按不存在处理（与整包拒绝口径一致）。
func (h *ManagedConfigHandler) pushSessionAccessible(c *gin.Context, sessionID uint) bool {
	ids, err := h.pushSvc.TargetInstanceIDs(sessionID)
	if err != nil {
		return false
	}
	_, denied := h.filterAccessibleInstances(c, ids)
	return len(denied) == 0
}

// filterAccessibleInstances 把请求里的实例 ID 收敛到当前用户可见集合。
//
// 返回 kept（保留下来的）与 denied（存在但不可访问的）。调用方据此决定是「静默丢弃」
// 还是「整包拒绝」——下发会话必须整包拒绝：静默少发会让运维以为全平台已推进。
func (h *ManagedConfigHandler) filterAccessibleInstances(c *gin.Context, ids []uint) (kept []uint, denied []uint) {
	if len(ids) == 0 {
		return nil, nil
	}
	access := getAccess(c)
	if access == nil || h.authz == nil {
		return nil, ids
	}
	allowed, all, err := h.authz.AccessibleInstanceIDs(access)
	if err != nil {
		return nil, ids
	}
	// all=false 是不受限（全保留）；all=true 才按可见集合过滤。
	if !all {
		return ids, nil
	}
	set := make(map[uint]struct{}, len(allowed))
	for _, id := range allowed {
		set[id] = struct{}{}
	}
	for _, id := range ids {
		if _, ok := set[id]; ok {
			kept = append(kept, id)
		} else {
			denied = append(denied, id)
		}
	}
	return kept, denied
}

package router

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// InstanceGroupHandler 实例组织分组树路由。
// 组织归类正交于用户组（RBAC）与网络群组（部署），权限沿用实例写权限节点
// instance:write（树是实例的人为归类，按实例写权限收敛，不引入新权限节点）。
type InstanceGroupHandler struct {
	svc   *service.InstanceGroupService
	authz *service.AuthzService
}

// NewInstanceGroupHandler 创建实例组织分组路由处理器。
func NewInstanceGroupHandler(svc *service.InstanceGroupService, authz *service.AuthzService) *InstanceGroupHandler {
	return &InstanceGroupHandler{svc: svc, authz: authz}
}

// requireRead 校验读分组树的权限（instance:read）。
func (h *InstanceGroupHandler) requireRead(c *gin.Context) bool {
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermInstanceRead) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return false
	}
	return true
}

// requireWrite 校验写分组树的权限（instance:write）。
func (h *InstanceGroupHandler) requireWrite(c *gin.Context) bool {
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermInstanceWrite) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return false
	}
	return true
}

// Tree GET /instance-groups
//
// 返回前按调用者可见实例收敛：组织分组是实例的人为归类，非管理员只能看到
// 自己有权访问的实例及其数量。分组节点本身（名称/层级）不敏感，保留全量，
// 但 MemberInstanceIDs 与 InstanceCount 必须只算可见实例——否则组用户可以
// 从中枚举其他组的实例 ID 与规模。
func (h *InstanceGroupHandler) Tree(c *gin.Context) {
	if !h.requireRead(c) {
		return
	}
	tree, err := h.svc.Tree()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": err.Error()})
		return
	}
	if visible, scoped, ok := h.visibleInstanceSet(c); ok && scoped {
		for i := range tree {
			tree[i].MemberInstanceIDs = intersectIDs(tree[i].MemberInstanceIDs, visible)
			tree[i].InstanceCount = len(tree[i].MemberInstanceIDs)
		}
	}
	c.JSON(http.StatusOK, tree)
}

// visibleInstanceSet 返回调用者可访问实例集合；scoped=false 表示不收敛（平台管理员）。
// 取不到时写 500 并返回 ok=false（fail-closed）。
func (h *InstanceGroupHandler) visibleInstanceSet(c *gin.Context) (map[uint]struct{}, bool, bool) {
	access := getAccess(c)
	if access == nil || h.authz == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "权限校验失败"})
		return nil, false, false
	}
	ids, scoped, err := h.authz.AccessibleInstanceIDs(access)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "权限校验失败"})
		return nil, false, false
	}
	set := make(map[uint]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set, scoped, true
}

// intersectIDs 保留在 visible 里的 ID（保持原顺序）。
func intersectIDs(ids []uint, visible map[uint]struct{}) []uint {
	out := make([]uint, 0, len(ids))
	for _, id := range ids {
		if _, ok := visible[id]; ok {
			out = append(out, id)
		}
	}
	return out
}

type createInstanceGroupRequest struct {
	Name     string `json:"name" binding:"required,min=1,max=128"`
	ParentID *uint  `json:"parentId"`
}

// Create POST /instance-groups
func (h *InstanceGroupHandler) Create(c *gin.Context) {
	if !h.requireWrite(c) {
		return
	}
	var req createInstanceGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	// 父分组归属校验：不能把新组挂到无关树上（parentId=null 表示建在根，放行）。
	if req.ParentID != nil && !h.requireGroupWritable(c, *req.ParentID) {
		return
	}
	node, err := h.svc.Create(req.Name, req.ParentID)
	if err != nil {
		writeInstanceGroupError(c, err)
		return
	}
	c.JSON(http.StatusCreated, node)
}

// Update PUT /instance-groups/:id
// 以原始 JSON map 解析，借「parentId 字段是否出现」显式区分三态：
// 字段缺省=不改父；parentId:null=移到根（NULL）；parentId:N=移到 N 下。
func (h *InstanceGroupHandler) Update(c *gin.Context) {
	if !h.requireWrite(c) {
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}
	raw := map[string]json.RawMessage{}
	if err := c.ShouldBindJSON(&raw); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}

	var namePtr *string
	if rawName, ok := raw["name"]; ok {
		var name string
		if err := json.Unmarshal(rawName, &name); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "名称格式错误"})
			return
		}
		namePtr = &name
	}

	var parentArg **uint
	if rawParent, ok := raw["parentId"]; ok {
		// 字段出现：本次要改父。值为 null → 移到根；否则解析为 *uint。
		var pid *uint
		if err := json.Unmarshal(rawParent, &pid); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "父分组格式错误"})
			return
		}
		parentArg = &pid
	}

	// 目标组与目标父组都必须可写：否则可以把别人组改名/搬走/删掉。
	if !h.requireGroupWritable(c, id) {
		return
	}
	if parentArg != nil && *parentArg != nil && !h.requireGroupWritable(c, **parentArg) {
		return
	}
	node, err := h.svc.Update(id, namePtr, parentArg)
	if err != nil {
		writeInstanceGroupError(c, err)
		return
	}
	c.JSON(http.StatusOK, node)
}

// Delete DELETE /instance-groups/:id
func (h *InstanceGroupHandler) Delete(c *gin.Context) {
	if !h.requireWrite(c) {
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}
	// 目标组可写才能删：否则能删掉别人的分类节点（即使组为空）。
	if !h.requireGroupWritable(c, id) {
		return
	}
	if err := h.svc.Delete(id); err != nil {
		writeInstanceGroupError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

type instanceGroupMembersRequest struct {
	InstanceIDs []uint `json:"instanceIds" binding:"required"`
}

// AddMembers POST /instance-groups/:id/members
func (h *InstanceGroupHandler) AddMembers(c *gin.Context) {
	if !h.requireWrite(c) {
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}
	var req instanceGroupMembersRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	// 目标组可写 + 成员实例可写，两者都要：
	// 前者防往别人组里塞东西，后者防把别人的实例拉进来。
	if !h.requireGroupWritable(c, id) {
		return
	}
	// 写路径逐台校验：组织分组是人为归类，但把其他组的实例加进自己的分组
	// 会泄露/操弄实例归属，必须与实例写权限同口径。
	if !h.requireInstancesWritable(c, req.InstanceIDs) {
		return
	}
	added, members, err := h.svc.AddMembers(id, req.InstanceIDs)
	if err != nil {
		writeInstanceGroupError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"added": added, "members": members})
}

// RemoveMembers DELETE /instance-groups/:id/members
func (h *InstanceGroupHandler) RemoveMembers(c *gin.Context) {
	if !h.requireWrite(c) {
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}
	var req instanceGroupMembersRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	// 移除同样逐台校验：移除别人的实例也是一种越权写。
	// 同时目标组本身必须可写（否则能从别人的组里抽掉实例）。
	if !h.requireGroupWritable(c, id) {
		return
	}
	if !h.requireInstancesWritable(c, req.InstanceIDs) {
		return
	}
	if err := h.svc.RemoveMembers(id, req.InstanceIDs); err != nil {
		writeInstanceGroupError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// Members GET /instance-groups/:id/members
// 返回该组「子树（含自身及后代）去重」的成员实例概要 + 实例 ID 集合，
// 供右列表展示与「按组（含子树）筛选」共用。
func (h *InstanceGroupHandler) MembersSubtree(c *gin.Context) {
	if !h.requireRead(c) {
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}
	ids, err := h.svc.SubtreeInstanceIDs(id)
	if err != nil {
		writeInstanceGroupError(c, err)
		return
	}
	// 只返回可见实例：该端点用于前端「按组筛选」，返回全集等于把跨组实例 ID 列出去。
	if visible, scoped, ok := h.visibleInstanceSet(c); ok && scoped {
		ids = intersectIDs(ids, visible)
	}
	c.JSON(http.StatusOK, gin.H{"instanceIds": ids})
}

// RegisterRoutes 注册实例组织分组路由。
func (h *InstanceGroupHandler) RegisterRoutes(rg *gin.RouterGroup) {
	g := rg.Group("/instance-groups")
	{
		g.GET("", h.Tree)
		g.POST("", h.Create)
		g.PUT("/:id", h.Update)
		g.DELETE("/:id", h.Delete)
		g.GET("/:id/instances", h.MembersSubtree)
		g.POST("/:id/members", h.AddMembers)
		g.DELETE("/:id/members", h.RemoveMembers)
	}
}

func writeInstanceGroupError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrInstanceGroupNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "INSTANCE_GROUP_NOT_FOUND", "message": err.Error()})
	case errors.Is(err, service.ErrInstanceGroupParentNotFound):
		c.JSON(http.StatusBadRequest, gin.H{"error": "INSTANCE_GROUP_PARENT_NOT_FOUND", "message": err.Error()})
	case errors.Is(err, service.ErrInstanceGroupCycle):
		c.JSON(http.StatusConflict, gin.H{"error": "INSTANCE_GROUP_CYCLE", "message": err.Error()})
	case errors.Is(err, service.ErrInstanceGroupNotEmpty):
		c.JSON(http.StatusConflict, gin.H{"error": "INSTANCE_GROUP_NOT_EMPTY", "message": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": err.Error()})
	}
}

// requireInstancesWritable 逐台校验调用者能否管理这些实例；任一不可管理即 404。
//
// 用「可管理」而非「可读」：分组成员变更属于写操作，组织归类被越权修改
// 会让其他组的运维在按分组筛选时看到错误的集合。
func (h *InstanceGroupHandler) requireInstancesWritable(c *gin.Context, instanceIDs []uint) bool {
	if len(instanceIDs) == 0 {
		return true
	}
	access := getAccess(c)
	if access == nil || h.authz == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "权限校验失败"})
		return false
	}
	if access.IsPlatformAdmin {
		return true
	}
	for _, id := range instanceIDs {
		ok, err := h.authz.CanManageInstance(access, id)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "权限校验失败"})
			return false
		}
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
			return false
		}
	}
	return true
}

// requireGroupWritable 校验调用者是否有权写入目标组织分组（fail-closed）。
//
// 【为什么需要它】原有写路径只校验了两件事：全局 instance:write 节点，
// 以及「成员实例可写」。但**分组节点本身**没有任何归属校验：
// 一个只属于 A 组（RBAC）的运维，持 instance:write 就能：
//   - Create(parentId=<别人组的子分组>)——把新组挂到无关树上；
//   - Update(id=<别人组>)——改名、或把别人组整个挂到自己下面；
//   - Delete(id=<别人组>)——删掉别人的分类节点；
//   - AddMembers/RemoveMembers(id=<别人组>)——往别人的组里塞/抽实例。
//
// 这些都是跨组织的结构性破坏，且不泄露数据也能造成误导（按分组筛选得到错的集合）。
//
// 【判定口径】「可写该分组」= 平台管理员，或该分组子树下存在调用者可管理的实例，
// 或该分组是空组且其父链可见（新建子组的常见情形）。
// 空组特意放行：刚建的空分组没有任何实例可判，若一律拒，
// 非管理员就永远无法建立自己的分类骨架——那是把安全做成不可用。
func (h *InstanceGroupHandler) requireGroupWritable(c *gin.Context, groupID uint) bool {
	if groupID == 0 {
		// 根（parentId=null/0）不归任何人所有，写权限由全局节点把关。
		return true
	}
	access := getAccess(c)
	if access == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "权限校验失败"})
		return false
	}
	if access.IsPlatformAdmin {
		return true
	}
	if h.svc == nil || h.authz == nil {
		// 未装配时 fail-closed。
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "权限校验失败"})
		return false
	}
	ids, err := h.svc.SubtreeInstanceIDs(groupID)
	if err != nil {
		// 组不存在：与其它实例级端点同口径，报 404 不泄露存在性。
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "分组不存在"})
		return false
	}
	if len(ids) == 0 {
		// 空组（含只有空子组的情形）：允许写——没有实例可越权。
		return true
	}
	for _, id := range ids {
		ok, err := h.authz.CanManageInstance(access, id)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "权限校验失败"})
			return false
		}
		if ok {
			// 「任一可管理」即可写该组：组内可能混有多个团队共管的实例，
			// 要求全部可管理会让任何共享分组永远无法维护。
			return true
		}
	}
	c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "分组不存在"})
	return false
}

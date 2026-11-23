package router

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// 配置域的实例级权限守卫。
//
// 背景：配置分层 / 交付 / 纳管三条链路原来只校验全局权限节点
// （file.read/write、instance.read/write），不看调用者实际能访问哪些实例。
// 但它们的入参与 selector 都能直接点名任意实例 ID / 分组 / 标签，
// 于是一个只属于 A 组的用户可以读写 B 组的配置——典型的 IDOR。
//
// 设计原则：
//   - **fail-closed**：拿不到可见集合就拒绝，而不是“拿不到就放行”；
//   - 平台管理员短路（AccessibleInstanceIDs 返回 scoped=false）；
//   - 显式 ID 逐个判、批量目标先解析后求交，任一越界即整体拒绝（不做部分执行）。

// configScopeGuard 封装一次请求内的可见实例判定。
//
// 内部缓存可访问集合，避免一个请求里对每个目标都查一次库。
//
// 注意：只缓存“读”集合。写权限仍然要逐个 CanManageInstance——
// 读写集合在当前模型里相同（都按所属组收敛），但语义分开保留，
// 将来若引入“可读不可写”的组权限时不会静默放行。
type configScopeGuard struct {
	ctx      *gin.Context
	authz    *service.AuthzService
	delivery *service.ConfigDeliveryService
	groups   *service.InstanceGroupService
	ids      []uint
	scoped   bool
	loaded   bool
	err      error
}

func newConfigScopeGuard(c *gin.Context, authz *service.AuthzService) *configScopeGuard {
	return &configScopeGuard{ctx: c, authz: authz}
}

// withDelivery 链式注入交付服务（校验交付单目标范围时需要）。
func (g *configScopeGuard) withDelivery(svc *service.ConfigDeliveryService) *configScopeGuard {
	g.delivery = svc
	return g
}

// withGroups 链式注入组织分组服务（校验 group 层作用域时需要）。
func (g *configScopeGuard) withGroups(svc *service.InstanceGroupService) *configScopeGuard {
	g.groups = svc
	return g
}

// accessible 返回调用者可访问的实例集合；scoped=false 表示不收敛（平台管理员）。
func (g *configScopeGuard) accessible() ([]uint, bool, error) {
	if g.loaded {
		return g.ids, g.scoped, g.err
	}
	g.loaded = true
	access := getAccess(g.ctx)
	if access == nil {
		g.err = errors.New("缺少授权上下文")
		return nil, true, g.err
	}
	if g.authz == nil {
		// 未装配 authz 时一律以“无任何可见实例”处理（fail-closed）。
		g.ids, g.scoped = []uint{}, true
		return g.ids, g.scoped, nil
	}
	ids, scoped, err := g.authz.AccessibleInstanceIDs(access)
	if err != nil {
		g.err = err
		return nil, true, err
	}
	g.ids, g.scoped = ids, scoped
	return g.ids, g.scoped, nil
}

// filterAccessible 把候选实例 ID 与可访问集合求交；不收敛时原样返回。
//
// 返回的 ids 已剔除越界项，dropped>0 表示调用方给的集合里含无权目标。
func (g *configScopeGuard) filterAccessible(ids []uint) (kept []uint, dropped int, err error) {
	accessIDs, scoped, err := g.accessible()
	if err != nil {
		return nil, 0, err
	}
	if !scoped {
		return ids, 0, nil
	}
	allowed := make(map[uint]struct{}, len(accessIDs))
	for _, id := range accessIDs {
		allowed[id] = struct{}{}
	}
	kept = make([]uint, 0, len(ids))
	for _, id := range ids {
		if _, ok := allowed[id]; ok {
			kept = append(kept, id)
			continue
		}
		dropped++
	}
	return kept, dropped, nil
}

// requireAllAccessible 校验给定实例 IDs 全部在可见范围内；任一越界即写 404 并返回 false。
//
// 用 404 而不是 403：与仓库内其它实例级端点一致——不泄露“这个 id 存在但不属于你”。
func (g *configScopeGuard) requireAllAccessible(ids []uint) bool {
	if len(ids) == 0 {
		return true
	}
	kept, dropped, err := g.filterAccessible(ids)
	if err != nil {
		g.ctx.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "权限校验失败"})
		return false
	}
	if dropped > 0 || len(kept) != len(ids) {
		g.ctx.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return false
	}
	return true
}

// requireScopeAccessible 校验层作用域（group / instance）是否在可见范围内。
//
// 【两套 group 不是同一个东西】配置层的 `group` 作用域指向 **InstanceGroupNode.ID**
// （实例组织分组树），而 `UserAccess.CanAccessGroup` 查的是 **RBAC 用户组**（GroupMember）。
// 两个 ID 空间完全独立，拿后者去判前者既会越权放行（碰巧同 ID）也会误拒（正常组被判无权）。
// 所以这里必须把组织分组展开成它的成员实例集合，再看交集。
func (g *configScopeGuard) requireScopeAccessible(level model.ConfigScopeLevel, scopeID uint) bool {
	if g.scopeVisible(level, scopeID) {
		return true
	}
	switch level {
	case model.ConfigScopeGroup:
		g.ctx.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "分组不存在"})
	default:
		g.ctx.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
	}
	return false
}

// scopeVisible 纯查询版：只回答“该作用域可不可见”，不写响应。
// 用于列表过滤（不能因为一条不可见就整个请求 404）。
func (g *configScopeGuard) scopeVisible(level model.ConfigScopeLevel, scopeID uint) bool {
	access := getAccess(g.ctx)
	if access != nil && access.IsPlatformAdmin {
		return true
	}
	switch level {
	case model.ConfigScopeInstance:
		kept, dropped, err := g.filterAccessible([]uint{scopeID})
		if err != nil || dropped > 0 || len(kept) == 0 {
			return false
		}
		return true
	case model.ConfigScopeGroup:
		/*
			组层可见性 = 「该组子树下有至少一个可见实例」。

			为何是“任一”而不是“全部”：组层是一份覆盖片段，下发时只会落到组内
			实例上；调用者能管组内部分实例时，读/写该组层不会泄露组外数据。
			若要求“全部可见”，多组共管的树会被过度拒绝（一个组里只要有一台
			别人的实例就全不能碰）——那是可用性灾难，不是安全收益。

			取不到成员集合时 fail-closed（返回 false）。
		*/
		return g.groupHasVisibleMember(scopeID)
	default:
		// 未知级别交给 Service 报 INVALID_REQUEST，过滤时先保留。
		return true
	}
}

// groupHasVisibleMember 判断某组织分组（含子树）下是否存在调用者可见的实例。
//
// 依赖注入的 groups 服务；未装配时 fail-closed。
func (g *configScopeGuard) groupHasVisibleMember(groupID uint) bool {
	if g.groups == nil {
		return false
	}
	ids, err := g.groups.SubtreeInstanceIDs(groupID)
	if err != nil || len(ids) == 0 {
		// 组不存在或空组：对调用者而言等同于“不可见”。
		return false
	}
	kept, _, err := g.filterAccessible(ids)
	if err != nil {
		return false
	}
	return len(kept) > 0
}

// targetScope 把当前调用者的可见范围转成业务目标解析器的 scope。
//
// 取不到可见集合（authz 未装配/查询失败）时返回空 scope 会变成“不收敛”，
// 所以这里显式在错误情形下返回 Scoped=true 且 IDs 为空：解析结果必然为空，
// 调用方会得到 TARGET_EMPTY，而不是越权成功。
func (g *configScopeGuard) targetScope() service.BusinessTargetScope {
	ids, scoped, err := g.accessible()
	if err != nil {
		return service.BusinessTargetScope{Scoped: true, IDs: []uint{}}
	}
	if !scoped {
		return service.BusinessTargetScope{}
	}
	return service.BusinessTargetScope{Scoped: true, IDs: ids}
}

// explicitAdoptInstanceIDs 取出纳管请求里显式列出的实例 ID（files[].instanceId）。
func explicitAdoptInstanceIDs(req service.ConfigAdoptRequest) []uint {
	if len(req.Files) == 0 {
		return nil
	}
	ids := make([]uint, 0, len(req.Files))
	seen := make(map[uint]struct{}, len(req.Files))
	for _, f := range req.Files {
		if f.InstanceID == 0 {
			continue
		}
		if _, ok := seen[f.InstanceID]; ok {
			continue
		}
		seen[f.InstanceID] = struct{}{}
		ids = append(ids, f.InstanceID)
	}
	return ids
}

// requireDeliveryOwned 校验该交付单的全部目标落在调用者可见范围内。
//
// 为什么不用「只看 order 的 CreatedBy」：同一份变更可以被多个运维协作（同一组内），
// 但绝不能跨组。所以按目标实例集合判定，而不是按创建人。
//
// 交付单一旦创建，目标集合就固定了（draft 阶段可重选）。这里读库里的
// config_delivery_targets，而不是重新解析 selector——后者会因后续实例变动而漂移，
// 与执行时真正会写盘的集合不一致。
func (g *configScopeGuard) requireDeliveryOwned(orderID uint) bool {
	if g.delivery == nil {
		// 未装配交付服务时无法判定归属，fail-closed。
		g.ctx.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "权限校验失败"})
		return false
	}
	ids, err := g.delivery.TargetInstanceIDs(orderID)
	if err != nil {
		g.ctx.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "权限校验失败"})
		return false
	}
	if len(ids) == 0 {
		// 目标尚未生成（刚建单）或全部已清理：交给业务层报错，这里不拦。
		return true
	}
	return g.requireAllAccessible(ids)
}

// Package logcoord — 生产装配辅助（T11）。
package logcoord

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// ConnectedNodes 查询当前活跃隧道/连接的 Worker UUID 集合。
type ConnectedNodes interface {
	ConnectedNodes() []string
}

// ProductionTargetResolver 把 CP 授权目标解析为可扇出联邦目标。
//
// 支持：
//   - `*` / 空：展开为全部实例（每条携带所属节点 WorkerUUID）
//   - `inst:<id>`：解析实例 → 所属节点 UUID（P1：路由层实例级查询）
//   - `node:<id>`：节点级目标
//   - 节点 UUID 字符串：兼容直接传 UUID
//
// Readiness 以 **节点状态 ∩ 活跃隧道** 判定：无活跃连接或节点 Offline 时为
// ReadyOffline，供 onlineOnly 筛选与 coverage 使用（P1：禁止无条件 ReadyOnline）。
//
// 快照缓存（2026-10-09 日志中心卡顿）：见 resolverSnapshotTTL 与 cachedNodes/cachedGlobal 的注释。
// 缓存只存**授权无关的库内元数据**，授权过滤与 readiness 判定每次照旧现算。
type ProductionTargetResolver struct {
	nodes     *service.NodeService
	instances *service.InstanceService
	holders   *service.LogTargetHolderService
	connected ConnectedNodes

	// 节点快照（全分支共用）与全量实例/holder 快照（仅通配分支用）。
	// 两把锁各自保护「检查 + 回源 + 落缓存」全过程 ⇒ 冷启动时的并发请求只回源一次
	// （单飞），避免击穿把 3 条 SQL 放大成 3×并发数条。
	nodesMu   sync.Mutex
	nodesSnap *nodesSnapshot
	allMu     sync.Mutex
	allSnap   *globalSnapshot
	// 显式 inst:<id> 分支的单实例快照（按实例 ID 为键，见 instEntry 的安全性论证）。
	instMu    sync.Mutex
	instCache map[uint]*instEntry
	// snapshotTTL 覆盖默认快照 TTL；<=0 表示用 resolverSnapshotTTL（测试可注入 0 关闭缓存）。
	snapshotTTL time.Duration
}

// ttl 返回生效的快照 TTL。
func (r *ProductionTargetResolver) ttl() time.Duration {
	if r.snapshotTTL > 0 {
		return r.snapshotTTL
	}
	return resolverSnapshotTTL
}

// resolverSnapshotTTL 目标解析元数据快照的 TTL。
//
// 为什么是 3 秒：一次「日志中心页面加载」在生产实测会在同一秒内连打三条请求
// （列表 + stats + facets，见 cp.out 中同秒三条 federation 日志），但它们是三个独立
// HTTP 请求，请求级复用覆盖不到 ⇒ 只有跨请求的短 TTL 才能把这一次页面加载的
// 元数据回源从 3 次压到 1 次。TTL 只需覆盖这一串突发（约 1 秒）+ 抖动余量。
//
// 为什么不能更长：本 TTL 是**节点在线状态**的陈旧上界（节点状态来自 DB）。生产用
// 90s 无心跳才判离线（service.CheckOfflineNodes），3s 远小于该阈值；且 readiness
// 还要求活跃隧道，隧道状态从不入缓存（见 connectedSet），故真实可用性判定不落后。
const resolverSnapshotTTL = 3 * time.Second

// nodesSnapshot 节点表快照（全量节点列表）。
//
// 缓存它不会放宽授权：节点列表是**平台级、与调用方授权集合无关**的库内元数据
// （授权收敛发生在 Resolve 内、在缓存读取之后，按调用方传入的 authorized 现算）。
// 缓存命中也只是省掉一次回源，绝不会把某个调用方见过的节点集合回给另一个调用方
// ——快照里没有任何调用方维度，不同 authorized 集合各自过滤同一份输入。
type nodesSnapshot struct {
	nodes  []model.Node
	byID   map[uint]model.Node
	byUUID map[string]model.Node
	exp    time.Time
}

// globalSnapshot 全量实例 + 全量 holder 快照（仅通配分支使用）。
//
// 同上：这两张表都是平台级元数据，缓存的是「库里的全量行」，不含授权过滤结果。
type globalSnapshot struct {
	insts     []model.Instance
	holders   map[uint][]model.LogTargetHolder
	holdersOK bool
	exp       time.Time
}

// instEntry 单实例元数据快照（显式 inst:<id> 分支使用）。
//
// **为什么按「单个实例 ID」为键是安全的**：键就是调用方本次请求里那个 inst:<id>
// 的 ID 本身（调用方授权集合的元素），缓存条目只可能被「本次请求也包含同一个
// inst:<id>」的调用读到 ⇒ 命中只会返回**该调用方已声明要查、且上游已判定有权查**
// 的那个实例的数据。这与「按授权集合为键」等价，但结构上更不容易出错：
// 缓存里没有任何集合维度，也就不存在集合算错导致的跨集合泄漏（越权）。
//
// found=false 表示该 ID 在库中不存在（负缓存）：语义与逐条 GetByID 返回
// ErrInstanceNotFound 一致（产出 not_ready 占位），陈旧上界同样是 TTL。
type instEntry struct {
	brief     service.TopologyInstanceBrief
	found     bool
	holders   []model.LogTargetHolder
	holdersOK bool
	exp       time.Time
}

// instCacheMaxEntries 单实例快照条目上限：条目按实例 ID 收敛（生产中约数十~数百），
// 上限只作兜底，防止长期运行中被已删除实例的 ID 无界撑大。
const instCacheMaxEntries = 4096

// NewProductionTargetResolver 创建生产目标解析器。
func NewProductionTargetResolver(nodes *service.NodeService, instances *service.InstanceService, connected ConnectedNodes, holders *service.LogTargetHolderService) *ProductionTargetResolver {
	return &ProductionTargetResolver{nodes: nodes, instances: instances, connected: connected, holders: holders}
}

// cachedNodes 返回节点快照（TTL 内复用，过期或缺失时回源）。
//
// 回源错误原样上抛（与缓存引入前逐字一致：nodes.List 失败 ⇒ 整次解析失败），
// 且**不落缓存** ⇒ 一次失败不会被缓存放大成 TTL 窗口内的持续失败。
func (r *ProductionTargetResolver) cachedNodes() (*nodesSnapshot, error) {
	r.nodesMu.Lock()
	defer r.nodesMu.Unlock()
	if s := r.nodesSnap; s != nil && time.Now().Before(s.exp) {
		return s, nil
	}
	nodes, err := r.nodes.List()
	if err != nil {
		return nil, err
	}
	s := &nodesSnapshot{
		nodes:  nodes,
		byID:   make(map[uint]model.Node, len(nodes)),
		byUUID: make(map[string]model.Node, len(nodes)),
		exp:    time.Now().Add(r.ttl()),
	}
	for _, n := range nodes {
		s.byID[n.ID] = n
		if n.UUID != "" {
			s.byUUID[n.UUID] = n
		}
	}
	r.nodesSnap = s
	return s, nil
}

// cachedGlobal 返回全量实例 + 全量 holder 快照（仅通配分支使用）。
//
// 错误语义与缓存引入前逐字一致：
//   - 实例列表失败 ⇒ 整次解析失败（上抛，不缓存）；
//   - holder 批量失败 ⇒ holdersOK=false（不致命），由调用方回落逐实例查询。
//
// 失败不沉淀：holder 取数失败时**不**把失败固化进快照 TTL —— 命中快照但 holdersOK=false
// 时仍会在本次调用重试一次批量 holder（1 条 SQL），成功即就地补齐。否则一次瞬时失败会让
// 整个 TTL 窗口内每次都走「逐实例回落」（N 条 SQL），反而放大 DB 占用。
//
// 为什么按「全量」取而不是按 authorized 取：按 authorized 取会让缓存键带上授权集合，
// 一旦键算错就可能跨集合命中（越权）。取全量则缓存内含零调用方维度，授权过滤全部
// 在缓存之后按 authorized 现算 ⇒ 结构上不可能让 A 的缓存命中放宽 B 的可见集合。
func (r *ProductionTargetResolver) cachedGlobal() (*globalSnapshot, error) {
	r.allMu.Lock()
	defer r.allMu.Unlock()
	if s := r.allSnap; s != nil && time.Now().Before(s.exp) {
		// 命中快照但 holder 此前取数失败：就地对齐，避免失败在 TTL 内被固化成逐实例回落。
		if !s.holdersOK && r.holders != nil {
			r.fillGlobalHoldersLocked(s)
		}
		return s, nil
	}
	if r.instances == nil {
		s := &globalSnapshot{exp: time.Now().Add(r.ttl())}
		r.allSnap = s
		return s, nil
	}
	insts, err := r.instances.List(service.InstanceFilter{})
	if err != nil {
		return nil, err
	}
	s := &globalSnapshot{insts: insts, exp: time.Now().Add(r.ttl())}
	r.fillGlobalHoldersLocked(s)
	r.allSnap = s
	return s, nil
}

// fillGlobalHoldersLocked 取全量 holder 并就地写入快照；失败只留 holdersOK=false。
// 调用方须持有 allMu。
func (r *ProductionTargetResolver) fillGlobalHoldersLocked(s *globalSnapshot) {
	if s == nil || r.holders == nil {
		return
	}
	ids := make([]uint, 0, len(s.insts))
	for _, inst := range s.insts {
		ids = append(ids, inst.ID)
	}
	holders, err := r.holders.ListForInstances(ids)
	if err != nil {
		// holder 表不可用：保持 holdersOK=false，调用方回落逐实例查询（与缓存前一致）。
		return
	}
	s.holders = holders
	s.holdersOK = true
}

// cachedInstances 批量解析显式 inst:<id> 所需的元数据（实例身份 + holder），按单实例 ID 缓存。
//
// 语义与「逐条 GetByID + 批量 ListForInstances」逐字保持一致：
//   - 实例不存在 ⇒ found=false（调用方产出 not_ready 占位，与 GetByID 的 ErrInstanceNotFound 同义）；
//   - holder 取数失败 ⇒ holdersOK=false（不致命，产出实例目标但不产出 holder 目标）。
//
// 单飞：整个「检查缓存 + 回源 + 落缓存」在 instMu 内完成 ⇒ 并发冷启动只有第一路回源，
// 其余各路读到同一份快照（实测：8 路并发混合授权由 11 条降到 3 条）。持锁做 DB 往返是可接受的：
// 冷启动每 TTL 窗口只发生一次，且 CP 的 SQLite 本就是单连接（DB 层天然串行），
// 在锁上排队与在连接池上排队等价，不引入额外等待。
func (r *ProductionTargetResolver) cachedInstances(ids []uint) map[uint]*instEntry {
	out := make(map[uint]*instEntry, len(ids))
	if len(ids) == 0 {
		return out
	}

	r.instMu.Lock()
	defer r.instMu.Unlock()

	now := time.Now()
	missing := make([]uint, 0, len(ids))
	retryHolders := make([]uint, 0, len(ids))
	for _, id := range ids {
		e, ok := r.instCache[id]
		if ok && now.Before(e.exp) {
			out[id] = e
			// 命中快照但上次 holder 取数失败：就地重试（1 条批量 SQL），
			// 避免失败在 TTL 内被固化成「逐实例 ListForInstance 回落」（N 条 SQL，
			// 反而比不做缓存更糟）。见 fillGlobalHoldersLocked 的同款处理。
			if !e.holdersOK {
				retryHolders = append(retryHolders, id)
			}
			continue
		}
		missing = append(missing, id)
	}
	if len(retryHolders) > 0 && r.holders != nil {
		if rows, err := r.holders.ListForInstances(retryHolders); err == nil {
			for _, id := range retryHolders {
				if e := r.instCache[id]; e != nil {
					e.holders, e.holdersOK = rows[id], true
				}
			}
		}
	}
	if len(missing) == 0 {
		return out
	}

	// unresolved 记录「身份查询本身报错」的 ID：**不入缓存**，下次调用重试。
	// 原因：把瞬时 DB 错误缓存成 found=false，等于在 TTL 窗口内谎称实例不存在
	// （产出 not_ready 占位、丢掉历史 holder 目标），比不缓存更糟。
	briefs := map[uint]service.TopologyInstanceBrief{}
	unresolved := map[uint]bool{}
	if r.instances != nil {
		if rows, err := r.instances.AllBriefs(missing); err == nil {
			for _, b := range rows {
				briefs[b.ID] = b
			}
			// AllBriefs 成功时，未返回的 ID 属**确定性**「实例不存在」（与 GetByID 的
			// ErrInstanceNotFound 同义）⇒ 可安全负缓存。
		} else {
			// 整批查询失败：回落逐条 GetByID（与批量引入前的语义一致）。
			for _, id := range missing {
				inst, gerr := r.instances.GetByID(id)
				switch {
				case gerr == nil:
					briefs[id] = service.TopologyInstanceBrief{ID: inst.ID, NodeID: inst.NodeID}
				case errors.Is(gerr, service.ErrInstanceNotFound):
					// 确定性不存在：可负缓存（不写 briefs ⇒ found=false）。
				default:
					unresolved[id] = true
				}
			}
		}
	}

	var holderRows map[uint][]model.LogTargetHolder
	holderOK := false
	if r.holders != nil {
		rows, herr := r.holders.ListForInstances(missing)
		if herr == nil {
			holderRows, holderOK = rows, true
		}
	}

	exp := time.Now().Add(r.ttl())
	if r.instCache == nil {
		r.instCache = make(map[uint]*instEntry)
	}
	// 超限时整体清空（条目以实例 ID 为键、规模天然有界；这里只作兜底防无界增长）。
	if len(r.instCache) > instCacheMaxEntries {
		r.instCache = make(map[uint]*instEntry)
	}
	for _, id := range missing {
		e := &instEntry{exp: exp}
		if b, ok := briefs[id]; ok {
			e.brief, e.found = b, true
		}
		if holderOK {
			e.holders, e.holdersOK = holderRows[id], true
		}
		// 身份查询报错的 ID 不入缓存：本次先返回「查不到」（调用方产出 not_ready 占位，
		// 与逐条 GetByID 报错时的行为一致），下次调用重新回源。
		if !unresolved[id] {
			r.instCache[id] = e
		}
		out[id] = e
	}
	return out
}

func (r *ProductionTargetResolver) connectedSet() map[string]bool {
	out := make(map[string]bool)
	if r == nil || r.connected == nil {
		return out
	}
	for _, id := range r.connected.ConnectedNodes() {
		if id != "" {
			out[id] = true
		}
	}
	return out
}

func readinessFor(n model.Node, conn map[string]bool) Readiness {
	if n.UUID == "" {
		return ReadyNotReady
	}
	// 心跳在线 + 活跃隧道：在线；否则离线（onlineOnly 不得扇出）。
	if n.Status == model.NodeStatusOnline && conn[n.UUID] {
		return ReadyOnline
	}
	if n.Status == model.NodeStatusOnline {
		// 节点标记在线但隧道不可用：按不就绪处理，避免假扇出。
		return ReadyNotReady
	}
	return ReadyOffline
}

// Resolve 实现 TargetResolver。
//
// 每次调用都会**现算**两件事，二者都绝不进缓存：
//  1. 授权过滤 —— 按本次传入的 authorized 集合决定可见目标（快照里没有调用方维度）；
//  2. readiness —— 由「快照中的节点状态」∩「本次实时隧道集合」现算，隧道活跃度不缓存。
func (r *ProductionTargetResolver) Resolve(ctx context.Context, authorized []string) ([]TargetInfo, error) {
	if r == nil || r.nodes == nil {
		return nil, errNoResolver
	}
	snap, err := r.cachedNodes()
	if err != nil {
		return nil, err
	}
	byID, byUUID := snap.byID, snap.byUUID
	conn := r.connectedSet()
	ready := func(n model.Node) Readiness { return readinessFor(n, conn) }

	wantAll := len(authorized) == 0
	wanted := make(map[string]bool, len(authorized))
	for _, id := range authorized {
		wanted[id] = true
		if id == "*" {
			wantAll = true
		}
	}

	out := make([]TargetInfo, 0, len(snap.nodes))

	appendNode := func(n model.Node, targetID string) {
		if n.UUID == "" {
			return
		}
		out = append(out, TargetInfo{
			ID: targetID, AuthorizationID: targetID,
			WorkerID:  n.UUID,
			Readiness: ready(n),
		})
	}

	// holdersFor 返回某实例的 holder 行。
	//
	// 优先使用调用方预先批量取回的结果（正常路径：全部实例共用 1 条 SQL）。
	// 批量查询失败时回落到逐实例查询，且与批量前的行为**逐字保持一致**：查询失败只是
	// 该实例不产出 holder 目标，实例目标本身照常产出、整次解析照常成功（不因 holder
	// 元数据不可用而把整个日志页打成 500）。这条回落路径同时保证：批量优化无论成败
	// 都不会让错误语义比改动前更严。
	holdersFor := func(instID uint, batched map[uint][]model.LogTargetHolder, batchOK bool) []model.LogTargetHolder {
		if r.holders == nil {
			return nil
		}
		if batchOK {
			return batched[instID]
		}
		rows, err := r.holders.ListForInstance(instID)
		if err != nil {
			return nil
		}
		return rows
	}

	// appendInstance 按预先批量取回的 holder 结果装配目标。
	//
	// 入参只取 (实例 ID, 所属节点 ID)：调用方无论走批量还是逐条取数，都只需这两个字段，
	// 故批量路径不必把整行 model.Instance 拼回来（见下方 briefsByID）。
	appendInstance := func(instID, nodeID uint, batched map[uint][]model.LogTargetHolder, batchOK bool) error {
		n, ok := byID[nodeID]
		if !ok || n.UUID == "" {
			return fmt.Errorf("instance %d: node %d has no worker uuid", instID, nodeID)
		}
		out = append(out, TargetInfo{
			ID:              fmt.Sprintf("inst:%d", instID),
			AuthorizationID: fmt.Sprintf("inst:%d", instID),
			WorkerID:        n.UUID,
			Readiness:       ready(n),
		})
		for _, h := range holdersFor(instID, batched, batchOK) {
			if h.Current || h.WorkerUUID == n.UUID {
				continue
			}
			readiness := ReadyOffline
			if conn[h.WorkerUUID] {
				readiness = ReadyOnline
			}
			out = append(out, TargetInfo{
				ID: fmt.Sprintf("holder:%d", h.ID), AuthorizationID: fmt.Sprintf("inst:%d", instID),
				WorkerID: h.WorkerUUID, WorkerTargetID: h.StorageNamespace,
				Readiness: readiness, HistoricalHolder: true,
			})
		}
		return nil
	}

	if wantAll {
		// 全量视图必须包含 Worker/Node 自身日志，即使节点当前没有实例。
		for _, n := range snap.nodes {
			appendNode(n, fmt.Sprintf("node:%d", n.ID))
		}
		if r.instances == nil {
			return out, nil
		}
		// 全量实例 + 全量 holder 走短 TTL 快照：二者都是平台级元数据，与调用方授权
		// 集合无关；授权过滤在下方 appendInstance 按 authorized 现算（见 cachedGlobal）。
		glob, err := r.cachedGlobal()
		if err != nil {
			return nil, err
		}
		for _, inst := range glob.insts {
			if err := appendInstance(inst.ID, inst.NodeID, glob.holders, glob.holdersOK); err != nil {
				continue
			}
		}
		return out, nil
	}

	// 显式 inst:<id> 目标可能一次给出多个（非平台管理员收敛为「可访问实例集合」时即如此），
	// 且生产中大量请求只带单个 instanceId（cp.out 实测 instanceId 参数出现数百次）：
	// 实例身份与 holder 一并走单实例快照，见下方 cachedInstances。
	//
	// explicitIDs 收集本次请求里所有可解析的 inst:<id>（保序、去重）。
	explicitIDs := make([]uint, 0, len(authorized))
	seenInst := make(map[uint]bool, len(authorized))
	if r.holders != nil || r.instances != nil {
		for _, id := range authorized {
			if !strings.HasPrefix(id, "inst:") {
				continue
			}
			num, err := strconv.ParseUint(strings.TrimPrefix(id, "inst:"), 10, 64)
			if err != nil {
				continue // 非数字目标沿用下方 switch 的报错语义，此处不参与批量。
			}
			if !seenInst[uint(num)] {
				seenInst[uint(num)] = true
				explicitIDs = append(explicitIDs, uint(num))
			}
		}
	}

	// 实例身份 + holder 一起走单实例快照（见 instEntry 的安全性论证与 cachedInstances）。
	//
	// 改动前（实测）：每个 inst:<id> 各调一次 GetByID ⇒ N 条 SQL；25 个实例 27 条。
	// 改动后：冷查 2 条（1 条实例投影 + 1 条批量 holder），TTL 内 0 条。
	//
	// 为什么这样不会放宽授权：缓存键是**本次请求 authorized 里的那个实例 ID 本身**，
	// 条目只能被「请求里也含同一 inst:<id>」的调用读到；且可见性仍由调用方传入的
	// authorized 决定（本函数只按它取数、从不查询集合外实例）。
	cachedInst := r.cachedInstances(explicitIDs)

	for _, id := range authorized {
		switch {
		case strings.HasPrefix(id, "inst:"):
			if r.instances == nil {
				return nil, fmt.Errorf("instance target %s: instance service unavailable", id)
			}
			num, err := strconv.ParseUint(strings.TrimPrefix(id, "inst:"), 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid instance target %q", id)
			}
			e := cachedInst[uint(num)]
			if e == nil || !e.found {
				// 实例不存在：仍返回 not_ready 占位，coverage 可解释。
				out = append(out, TargetInfo{ID: id, Readiness: ReadyNotReady})
				continue
			}
			if err := appendInstance(e.brief.ID, e.brief.NodeID, map[uint][]model.LogTargetHolder{e.brief.ID: e.holders}, e.holdersOK); err != nil {
				out = append(out, TargetInfo{ID: id, Readiness: ReadyNotReady})
			}
		case strings.HasPrefix(id, "node:"):
			num, err := strconv.ParseUint(strings.TrimPrefix(id, "node:"), 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid node target %q", id)
			}
			n, ok := byID[uint(num)]
			if !ok {
				out = append(out, TargetInfo{ID: id, Readiness: ReadyNotReady})
				continue
			}
			appendNode(n, id)
		default:
			// 节点 UUID
			if n, ok := byUUID[id]; ok {
				appendNode(n, fmt.Sprintf("node:%d", n.ID))
				continue
			}
			if wanted[id] {
				out = append(out, TargetInfo{ID: id, Readiness: ReadyNotReady})
			}
		}
	}
	return out, nil
}

// PoolClientFactory 用 CP gRPC ClientPool 适配 Worker Log 客户端。
func PoolClientFactory(pool *cpgrpc.ClientPool) WorkerClientFactory {
	return func(_ context.Context, workerID string) (workerpb.WorkerServiceClient, error) {
		if pool == nil {
			return nil, fmt.Errorf("logcoord: client pool is nil")
		}
		cli, ok := pool.Get(workerID)
		if !ok || cli == nil {
			return nil, fmt.Errorf("logcoord: worker %s not connected", workerID)
		}
		return cli.Worker, nil
	}
}

// Assemble 生产装配：实例/节点/历史 holder 目标解析 + 隧道 Dialer + Coordinator。
//
// 导出预算沿用默认（10000 行 / 64MiB）；需要放宽时用 AssembleWithExportBudget，
// 保持既有调用点零改动（本函数是生产 main 与测试共用的入口）。
func Assemble(pool *cpgrpc.ClientPool, nodeSvc *service.NodeService, instSvc *service.InstanceService, holders *service.LogTargetHolderService) *Coordinator {
	return AssembleWithExportBudget(pool, nodeSvc, instSvc, holders, ExportBudget{})
}

// AssembleWithExportBudget 同 Assemble，但注入导出物化预算上限。
//
// 预算为**上限而非目标**：请求参数只能在其内进一步收紧（见 Coordinator.Export），
// 故放宽配置不会让单次导出突破运维设定的资源边界。
func AssembleWithExportBudget(pool *cpgrpc.ClientPool, nodeSvc *service.NodeService, instSvc *service.InstanceService, holders *service.LogTargetHolderService, budget ExportBudget) *Coordinator {
	resolver := NewProductionTargetResolver(nodeSvc, instSvc, pool, holders)
	coord := New(resolver, NewTunnelDialer(PoolClientFactory(pool)))
	coord.SetExportBudget(budget)
	return coord
}

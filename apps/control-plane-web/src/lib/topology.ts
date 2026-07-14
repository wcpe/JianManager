/**
 * 群组服 proxy↔backend 拓扑（FR-145）。
 *
 * 把「代理 + 其已注册后端」聚合为一张二部图（M:N）：节点 = 实例（proxy/backend），
 * 连线 = 注册关系。一个 backend 可被多个 proxy 注册，故跨 proxy 去重为单节点、
 * 每条注册各产一条 edge。布局与着色全为纯函数，供 SVG 自绘（无图库）与 vitest 覆盖。
 */
import type { Registration } from '@/api/registrations'
import type { InstanceInfo } from '@/api/instances'
import type { NetworkMember } from '@/api/networks'
import { instanceStatusLevel, type StatusLevel } from '@/lib/threshold'

/** 单个代理及其已注册后端（拓扑构建入参单元）。 */
export interface ProxyRegistrations {
  proxy: InstanceInfo
  registrations: Registration[]
}

/** 拓扑节点的种类。 */
export type TopoKind = 'proxy' | 'backend'

/** 拓扑节点（实例）。 */
export interface TopoNode {
  id: number
  kind: TopoKind
  name: string
  /** 原始运行状态枚举（RUNNING/CRASHED/…），着色经 instanceStatusLevel 归一。 */
  status: string
  /** 监听端口（backend 来自 registration.backend.serverPort，proxy 来自实例 serverPort）。 */
  port?: number
  /** 所属节点（Worker Node）ID。 */
  nodeId?: number
  /** 作为该种角色被连接的次数（backend：被几个 proxy 注册；proxy：注册了几个 backend）。 */
  registrationCount: number
}

/** 拓扑连线（一条注册关系）。 */
export interface TopoEdge {
  proxyId: number
  backendId: number
  alias: string
  enabled: boolean
  /** 连线着色等级：禁用恒中性，启用跟随后端运行状态。 */
  level: StatusLevel
}

/** 拓扑图模型（未布局）。 */
export interface Topology {
  nodes: TopoNode[]
  edges: TopoEdge[]
}

/**
 * 注册连线的着色等级（FR-145）：
 * 禁用的注册不参与转发，恒为中性；启用时复用实例状态等级（运行=绿/崩溃=红/启停=黄）。
 */
export function edgeLevel(enabled: boolean, backendStatus: string): StatusLevel {
  if (!enabled) return 'neutral'
  return instanceStatusLevel(backendStatus)
}

/**
 * 由「代理 + 其注册」聚合为拓扑图模型。
 * - proxy 节点直接来自入参（保序）。
 * - backend 节点跨 proxy 按 id 去重，registrationCount 累加被注册次数。
 * - 每条 registration 产出一条 edge（M:N），共享后端 → 多 edge 单节点。
 * - 容错缺失的 backend 概要（仅有 backendId 时名称回退 `#<id>`、状态未知）。
 */
export function buildTopology(input: ProxyRegistrations[]): Topology {
  const nodes: TopoNode[] = []
  const edges: TopoEdge[] = []
  const backendNodes = new Map<number, TopoNode>()

  for (const { proxy, registrations } of input) {
    nodes.push({
      id: proxy.id,
      kind: 'proxy',
      name: proxy.name,
      status: proxy.status,
      port: proxy.serverPort,
      nodeId: proxy.nodeId,
      registrationCount: registrations.length,
    })

    for (const r of registrations) {
      const b = r.backend
      let node = backendNodes.get(r.backendId)
      if (!node) {
        node = {
          id: r.backendId,
          kind: 'backend',
          name: b?.name || `#${r.backendId}`,
          status: b?.status ?? '',
          port: b?.serverPort,
          nodeId: b?.nodeId,
          registrationCount: 0,
        }
        backendNodes.set(r.backendId, node)
        nodes.push(node)
      }
      node.registrationCount += 1
      edges.push({
        proxyId: r.proxyId,
        backendId: r.backendId,
        alias: r.alias,
        enabled: r.enabled,
        level: edgeLevel(r.enabled, b?.status ?? ''),
      })
    }
  }

  return { nodes, edges }
}

/** 布局参数（像素）。 */
export interface LayoutOptions {
  /** 画布宽度。 */
  width: number
  /** 单行行高（节点纵向间距）。 */
  rowHeight: number
  /** 节点盒宽度（用于连线贴边和列定位）。 */
  nodeWidth: number
  /** 上下内边距。 */
  paddingY: number
}

/** 已布局节点（带坐标，x/y 为节点中心）。 */
export interface LaidNode extends TopoNode {
  x: number
  y: number
}

/** 已布局连线（带两端坐标）。 */
export interface LaidEdge extends TopoEdge {
  x1: number
  y1: number
  x2: number
  y2: number
}

/** 已布局拓扑（含画布高度）。 */
export interface LaidTopology {
  nodes: LaidNode[]
  edges: LaidEdge[]
  width: number
  height: number
}

/**
 * 二部布局：proxy 居左列、backend 居右列，各列内按出现序等距纵向排开。
 * 高度按较多一侧的行数推算（至少一行，空图退化为单行高度）。连线从 proxy 右边缘
 * 连到 backend 左边缘。纯函数，便于 vitest 校验列分离与坐标解析。
 */
export function layoutTopology(topo: Topology, opts: LayoutOptions): LaidTopology {
  const { width, rowHeight, nodeWidth, paddingY } = opts
  const half = nodeWidth / 2
  const proxies = topo.nodes.filter((n) => n.kind === 'proxy')
  const backends = topo.nodes.filter((n) => n.kind === 'backend')

  const colX = (col: 0 | 1): number => {
    // 两列分别落在画布 1/4 与 3/4 处（含半宽留白），保证 proxy 在左。
    return col === 0 ? Math.max(half + 8, width * 0.25) : Math.min(width - half - 8, width * 0.75)
  }

  const rows = Math.max(proxies.length, backends.length, 1)
  const height = rows * rowHeight + Math.max(rows - 1, 0) * paddingY

  const placeColumn = (list: TopoNode[], col: 0 | 1): LaidNode[] => {
    const x = colX(col)
    return list.map((n, i) => ({
      ...n,
      x,
      y: paddingY + i * (rowHeight + paddingY) + rowHeight / 2,
    }))
  }

  const laidProxies = placeColumn(proxies, 0)
  const laidBackends = placeColumn(backends, 1)
  const laidNodes = [...laidProxies, ...laidBackends]

  const byKey = new Map<string, LaidNode>()
  for (const n of laidNodes) byKey.set(`${n.kind}:${n.id}`, n)

  const edges: LaidEdge[] = topo.edges.map((e) => {
    const p = byKey.get(`proxy:${e.proxyId}`)
    const b = byKey.get(`backend:${e.backendId}`)
    return {
      ...e,
      x1: (p?.x ?? 0) + half,
      y1: p?.y ?? 0,
      x2: (b?.x ?? 0) - half,
      y2: b?.y ?? 0,
    }
  })

  return { nodes: laidNodes, edges, width, height }
}

/** 成员实例按运行状态的计数桶（五态零补齐，对应后端 NetworkSummary.memberStatus，FR-335）。 */
export interface MemberStatusCounts {
  running: number
  stopped: number
  crashed: number
  starting: number
  stopping: number
}

/** 群组成员健康分布（FR-145 列表行）：按运行/崩溃/过渡/停止分桶。 */
export interface MemberHealth {
  total: number
  running: number
  crashed: number
  /** 启停中等过渡态。 */
  transitioning: number
  /** 停止/未知（中性）。 */
  stopped: number
}

/**
 * 由后端概要的五态计数桶直接得健康分布（FR-335），供列表页免详情请求渲染。
 * 口径与 memberHealth 一致：starting+stopping → transitioning 桶；total = 五桶之和。
 */
export function memberHealthFromStatus(counts: MemberStatusCounts): MemberHealth {
  const running = counts.running || 0
  const crashed = counts.crashed || 0
  const stopped = counts.stopped || 0
  const transitioning = (counts.starting || 0) + (counts.stopping || 0)
  return {
    total: running + crashed + stopped + transitioning,
    running,
    crashed,
    transitioning,
    stopped,
  }
}

/**
 * 统计成员状态分布，供列表行健康分布条与摘要。
 * 等级复用 instanceStatusLevel：success=运行 / danger=崩溃 / warning=过渡 / 其余=停止桶。
 */
export function memberHealth(members: NetworkMember[]): MemberHealth {
  const h: MemberHealth = { total: 0, running: 0, crashed: 0, transitioning: 0, stopped: 0 }
  for (const m of members) {
    h.total += 1
    switch (instanceStatusLevel(m.status)) {
      case 'success':
        h.running += 1
        break
      case 'danger':
        h.crashed += 1
        break
      case 'warning':
        h.transitioning += 1
        break
      default:
        h.stopped += 1
    }
  }
  return h
}

// ─────────────────────────── 分组分层（FR-335） ───────────────────────────

/** 拓扑分组入参：一个 network 的成员归属（软标签非独占，ADR-007）。 */
export interface TopoGroupBrief {
  id: number
  name: string
  memberInstanceIds: number[]
}

/** 已分带的拓扑：每带一个 network（或未分组兜底带），带内保留 proxy/backend 节点子集与全量连线。 */
export interface TopoBand {
  /** network id；未分组兜底带为 null。 */
  id: number | null
  name: string
  nodes: TopoNode[]
}

/** 分组后的拓扑模型（未布局）。 */
export interface GroupedTopology {
  bands: TopoBand[]
  edges: TopoEdge[]
  /** 归属多个 network 的节点 id 集合（落首带 + 角标提示，见 spec §6）。 */
  multiHomed: Set<number>
}

/** 未分组兜底带的固定 id 标记（null）。 */
const UNGROUPED_BAND_ID = null

/**
 * 按 network 成员归属把拓扑节点归带（FR-335）：
 * - 每个 network 一带（保持 groups 传入序）；节点落**首个**包含它的 group（软标签可多归属）。
 * - 不属任何 network 的节点归「未分组」兜底带（带 id=null）。
 * - multiHomed 记录归属 >1 个 group 的节点 id（渲染层加角标 +n）。
 * - 连线原样携带（渲染层按端点节点所在带跨带连接）。
 * 纯函数，便于 vitest 校验归带与多归属判定。
 */
export function groupTopology(topo: Topology, groups: TopoGroupBrief[]): GroupedTopology {
  // 每个实例 id → 命中的 group 序号列表（判定多归属 + 取首带）。
  const hitGroups = new Map<number, number[]>()
  groups.forEach((g, gi) => {
    for (const iid of g.memberInstanceIds) {
      const arr = hitGroups.get(iid)
      if (arr) arr.push(gi)
      else hitGroups.set(iid, [gi])
    }
  })

  const multiHomed = new Set<number>()
  for (const [iid, gis] of hitGroups) {
    if (gis.length > 1) multiHomed.add(iid)
  }

  // 初始化带（groups 顺序 + 末尾未分组带）。
  const bandNodes: TopoNode[][] = groups.map(() => [])
  const ungrouped: TopoNode[] = []

  for (const n of topo.nodes) {
    const gis = hitGroups.get(n.id)
    if (gis && gis.length > 0) {
      bandNodes[gis[0]].push(n) // 落首个所属带
    } else {
      ungrouped.push(n)
    }
  }

  const bands: TopoBand[] = []
  groups.forEach((g, gi) => {
    // 仅保留有节点的带，避免空带占位（空 network 无成员则不出现在拓扑）。
    if (bandNodes[gi].length > 0) {
      bands.push({ id: g.id, name: g.name, nodes: bandNodes[gi] })
    }
  })
  if (ungrouped.length > 0) {
    bands.push({ id: UNGROUPED_BAND_ID, name: '', nodes: ungrouped })
  }

  return { bands, edges: topo.edges, multiHomed }
}

/** 分组布局参数（像素）。 */
export interface GroupedLayoutOptions extends LayoutOptions {
  /** 每带标题条高度（含上下留白）。 */
  bandHeaderHeight: number
  /** 带与带之间的垂直间隔。 */
  bandGap: number
}

/** 一个已布局的带（带名标题 + 垂直区间）。 */
export interface LaidBand {
  id: number | null
  name: string
  /** 带顶部 y（含标题条）。 */
  y: number
  /** 带总高度（标题条 + 内容）。 */
  height: number
}

/** 已布局的分组拓扑（含带信息与总画布高度）。 */
export interface LaidGroupedTopology extends LaidTopology {
  bands: LaidBand[]
  /** 归属多个 network 的节点 id 集合（渲染层加角标）。 */
  multiHomed: Set<number>
}

/**
 * 分组分层布局（FR-335）：每带一个横向分层带（带内 proxy 左列 / backend 右列），带间留隔。
 * 画布总高 = Σ(带标题条 + 带内容高) + 带间隔，避免单列纵向线性膨胀。
 * 连线端点解析到各自节点（可能跨带）。纯函数，便于 vitest 校验带分层与坐标。
 */
export function layoutTopologyGrouped(
  grouped: GroupedTopology,
  opts: GroupedLayoutOptions,
): LaidGroupedTopology {
  const { width, rowHeight, nodeWidth, paddingY, bandHeaderHeight, bandGap } = opts
  const half = nodeWidth / 2

  const colX = (col: 0 | 1): number =>
    col === 0 ? Math.max(half + 8, width * 0.25) : Math.min(width - half - 8, width * 0.75)

  const laidNodes: LaidNode[] = []
  const bands: LaidBand[] = []
  let cursorY = 0

  grouped.bands.forEach((band, bi) => {
    if (bi > 0) cursorY += bandGap
    const bandTop = cursorY
    const contentTop = bandTop + bandHeaderHeight

    const proxies = band.nodes.filter((n) => n.kind === 'proxy')
    const backends = band.nodes.filter((n) => n.kind === 'backend')
    const rows = Math.max(proxies.length, backends.length, 1)
    const contentHeight = rows * rowHeight + Math.max(rows - 1, 0) * paddingY

    const placeColumn = (list: TopoNode[], col: 0 | 1) => {
      const x = colX(col)
      list.forEach((n, i) => {
        laidNodes.push({ ...n, x, y: contentTop + paddingY + i * (rowHeight + paddingY) + rowHeight / 2 })
      })
    }
    placeColumn(proxies, 0)
    placeColumn(backends, 1)

    const bandHeight = bandHeaderHeight + contentHeight + paddingY
    bands.push({ id: band.id, name: band.name, y: bandTop, height: bandHeight })
    cursorY = bandTop + bandHeight
  })

  const byKey = new Map<string, LaidNode>()
  for (const n of laidNodes) byKey.set(`${n.kind}:${n.id}`, n)

  const edges: LaidEdge[] = grouped.edges.map((e) => {
    const p = byKey.get(`proxy:${e.proxyId}`)
    const b = byKey.get(`backend:${e.backendId}`)
    return {
      ...e,
      x1: (p?.x ?? 0) + half,
      y1: p?.y ?? 0,
      x2: (b?.x ?? 0) - half,
      y2: b?.y ?? 0,
    }
  })

  return {
    nodes: laidNodes,
    edges,
    width,
    height: Math.max(cursorY, rowHeight),
    bands,
    multiHomed: grouped.multiHomed,
  }
}

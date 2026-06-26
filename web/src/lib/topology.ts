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

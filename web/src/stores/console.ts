import { create } from 'zustand'

/** 工作区分段：实例的统一视图分段——终端 / 文件 / 配置 / 插件 / 监控 / 服务器状态 / 业务 / 经济 / Bot（FR-039、FR-052 插件、FR-060 监控、FR-077 状态、FR-119 业务、FR-123 经济）。 */
export type WorkspaceSegment =
  | 'terminal'
  | 'files'
  | 'config'
  | 'plugins'
  | 'metrics'
  | 'serverstate'
  | 'business'
  | 'economy'
  | 'bot'

/** 侧栏布局持久键（FR-131）。 */
const SIDEBAR_COLLAPSED_KEY = 'sidebar.collapsed'
const COLLAPSED_GROUPS_KEY = 'sidebar.collapsedGroups'
const SELECTED_NODE_KEY = 'sidebar.selectedNodeId'

/** 安全读取布尔持久值（非 DOM/解析失败回退默认）。 */
function loadBool(key: string, fallback: boolean): boolean {
  if (typeof localStorage === 'undefined') return fallback
  const v = localStorage.getItem(key)
  return v === null ? fallback : v === '1'
}

/** 安全读取 JSON 持久值。 */
function loadJSON<T>(key: string, fallback: T): T {
  if (typeof localStorage === 'undefined') return fallback
  const raw = localStorage.getItem(key)
  if (!raw) return fallback
  try {
    return JSON.parse(raw) as T
  } catch {
    return fallback
  }
}

/** 安全读取选中节点 id（持久值，null = 全部节点）。 */
function loadSelectedNode(): number | null {
  if (typeof localStorage === 'undefined') return null
  const raw = localStorage.getItem(SELECTED_NODE_KEY)
  if (!raw) return null
  const n = Number(raw)
  return Number.isFinite(n) ? n : null
}

function persist(key: string, value: string | null): void {
  if (typeof localStorage === 'undefined') return
  if (value === null) localStorage.removeItem(key)
  else localStorage.setItem(key, value)
}

/**
 * 运维控制台的客户端 UI 状态（ADR-009 / FR-037 / FR-039 / FR-131）。
 * 存「当前选中节点」「当前在工作区打开的实例」「每实例工作区分段」「侧栏折叠/分组折叠态」，不进 URL，
 * 避免与既有 `/instances/:id` 详情路由语义冲突。侧栏折叠态/分组态/选中节点持久化 localStorage（FR-131）。
 */
interface ConsoleState {
  /** 实例树节点筛选：null = 全部节点，否则为某节点 id（持久） */
  selectedNodeId: number | null
  /** 工作区当前打开的实例 id；null = 未打开任何实例 */
  openInstanceId: number | null
  /** 每个已打开实例的工作区分段（终端/Bot），按实例 id 记忆，缺省为终端 */
  workspaceSegmentByInstance: Record<number, WorkspaceSegment>
  /** 多级侧栏中被折叠的分组 key 集合（FR-061/FR-131）；默认展开，记录已折叠者（持久）。 */
  collapsedGroups: Record<string, boolean>
  /** 侧栏是否折叠为仅图标轨（FR-131，持久）。 */
  sidebarCollapsed: boolean
  setSelectedNodeId: (nodeId: number | null) => void
  openInstance: (instanceId: number) => void
  closeInstance: () => void
  /** 设置某实例的工作区分段（终端/Bot），持久化于本会话内 */
  setWorkspaceSegment: (instanceId: number, segment: WorkspaceSegment) => void
  /** 切换侧栏分组展开/折叠（FR-061/FR-131）。 */
  toggleGroup: (key: string) => void
  /** 切换侧栏折叠态（仅图标轨 ⇄ 展开，FR-131）。 */
  toggleSidebar: () => void
}

export const useConsoleStore = create<ConsoleState>((set) => ({
  selectedNodeId: loadSelectedNode(),
  openInstanceId: null,
  workspaceSegmentByInstance: {},
  collapsedGroups: loadJSON<Record<string, boolean>>(COLLAPSED_GROUPS_KEY, {}),
  sidebarCollapsed: loadBool(SIDEBAR_COLLAPSED_KEY, false),
  setSelectedNodeId: (nodeId) => {
    persist(SELECTED_NODE_KEY, nodeId === null ? null : String(nodeId))
    set({ selectedNodeId: nodeId })
  },
  openInstance: (instanceId) => set({ openInstanceId: instanceId }),
  closeInstance: () => set({ openInstanceId: null }),
  setWorkspaceSegment: (instanceId, segment) =>
    set((s) => ({
      workspaceSegmentByInstance: { ...s.workspaceSegmentByInstance, [instanceId]: segment },
    })),
  toggleGroup: (key) =>
    set((s) => {
      const next = { ...s.collapsedGroups, [key]: !s.collapsedGroups[key] }
      persist(COLLAPSED_GROUPS_KEY, JSON.stringify(next))
      return { collapsedGroups: next }
    }),
  toggleSidebar: () =>
    set((s) => {
      const next = !s.sidebarCollapsed
      persist(SIDEBAR_COLLAPSED_KEY, next ? '1' : '0')
      return { sidebarCollapsed: next }
    }),
}))

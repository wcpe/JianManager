import { create } from 'zustand'

/** 侧栏布局持久键（FR-131）。 */
const SIDEBAR_COLLAPSED_KEY = 'sidebar.collapsed'
const COLLAPSED_GROUPS_KEY = 'sidebar.collapsedGroups'
const SELECTED_NODE_KEY = 'sidebar.selectedNodeId'
/** 上次选择的工作区键（FR-496 阶段 6）。 */
const LAST_WORKSPACE_KEY = 'console.lastWorkspace'

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

/** 安全读取字符串持久值（空串按「未设置」处理，避免写入空值后永久覆盖默认）。 */
function loadString(key: string, fallback: string | null): string | null {
  if (typeof localStorage === 'undefined') return fallback
  const value = localStorage.getItem(key)
  return value ? value : fallback
}

function persist(key: string, value: string | null): void {
  if (typeof localStorage === 'undefined') return
  if (value === null) localStorage.removeItem(key)
  else localStorage.setItem(key, value)
}

/**
 * 运维控制台的客户端 UI 状态（ADR-009 / FR-037 / FR-039 / FR-131 / FR-166）。
 * 存「页眉节点作用域」「侧栏折叠/分组折叠态」。实例视图一律由 `/instances/:id`
 * 深链承载，避免 URL 与工作区状态双轨漂移。侧栏折叠态/分组态/节点作用域持久化 localStorage（FR-131/FR-268）。
 * 打开实例后的画布/卡片/预设状态由 `stores/workspace.ts` 承载（FR-166 可组合卡片工作区）。
 * 当前工作区**不由本 store 承载**（它由路由决定，见 `workspace-navigation.ts` 的 `workspaceOfPath`）；
 * 这里只记「上次选择的工作区」做高亮兜底（FR-496 阶段 6）。
 */
interface ConsoleState {
  /** 页眉节点作用域：null = 全部节点，否则为某节点 id（持久） */
  selectedNodeId: number | null
  /** 多级侧栏中被折叠的分组 key 集合（FR-061/FR-131）；默认展开，记录已折叠者（持久）。 */
  collapsedGroups: Record<string, boolean>
  /** 侧栏是否折叠为仅图标轨（FR-131，持久）。 */
  sidebarCollapsed: boolean
  /** 全局命令面板是否打开（FR-241 Ctrl+K，不持久）。 */
  commandPaletteOpen: boolean
  /**
   * 上次选择的工作区键（FR-496 阶段 6，持久）。
   * 类型故意保持 `string`：工作区键的唯一真源在导航数据层，store 不该反向依赖它；
   * 消费方（`WorkspaceSidebar`）会按当前可见工作区列表校验，陈旧值自然失效。
   */
  lastWorkspaceKey: string | null
  setSelectedNodeId: (nodeId: number | null) => void
  /** 切换侧栏分组展开/折叠（FR-061/FR-131）。 */
  toggleGroup: (key: string) => void
  /** 切换侧栏折叠态（仅图标轨 ⇄ 展开，FR-131）。 */
  toggleSidebar: () => void
  /** 打开/关闭全局命令面板（FR-241）。 */
  setCommandPaletteOpen: (open: boolean) => void
  /** 记住用户选过的工作区（FR-496 阶段 6）：仅供顶栏在路由判不出工作区时兜底高亮。 */
  setLastWorkspaceKey: (key: string) => void
}

export const useConsoleStore = create<ConsoleState>((set) => ({
  selectedNodeId: loadSelectedNode(),
  collapsedGroups: loadJSON<Record<string, boolean>>(COLLAPSED_GROUPS_KEY, {}),
  sidebarCollapsed: loadBool(SIDEBAR_COLLAPSED_KEY, false),
  commandPaletteOpen: false,
  lastWorkspaceKey: loadString(LAST_WORKSPACE_KEY, null),
  setSelectedNodeId: (nodeId) => {
    persist(SELECTED_NODE_KEY, nodeId === null ? null : String(nodeId))
    set({ selectedNodeId: nodeId })
  },
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
  setCommandPaletteOpen: (open) => set({ commandPaletteOpen: open }),
  setLastWorkspaceKey: (key) => {
    persist(LAST_WORKSPACE_KEY, key)
    set({ lastWorkspaceKey: key })
  },
}))

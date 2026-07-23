/**
 * 资源管理器多标签状态（FR-376）：纯函数，便于单测。
 * 标签绑定目录/打开文件元数据；实际 Explorer 状态由各自组件实例持有。
 */

export const MAX_EXPLORER_TABS = 8
export const MAX_EXPLORER_FLOATS = 3

export interface ExplorerTab {
  id: string
  title: string
  currentDir: string
  openFilePath?: string
  floated: boolean
  dirty: boolean
}

export interface ExplorerTabsState {
  tabs: ExplorerTab[]
  activeId: string
}

let tabSeq = 0

/** 生成稳定唯一 id（测试可重置）。 */
export function nextTabId(): string {
  tabSeq += 1
  return `etab-${tabSeq}`
}

/** 测试用：重置 id 序列。 */
export function resetTabIdSeq(n = 0): void {
  tabSeq = n
}

export function titleFromPath(dir: string, file?: string): string {
  if (file) {
    const base = file.includes('/') ? file.slice(file.lastIndexOf('/') + 1) : file
    return base || file
  }
  if (!dir) return '/'
  const parts = dir.replace(/\\/g, '/').split('/').filter(Boolean)
  return parts[parts.length - 1] || '/'
}

export function createTab(partial?: Partial<Pick<ExplorerTab, 'currentDir' | 'openFilePath' | 'title'>>): ExplorerTab {
  const currentDir = partial?.currentDir ?? ''
  const openFilePath = partial?.openFilePath
  return {
    id: nextTabId(),
    title: partial?.title ?? titleFromPath(currentDir, openFilePath),
    currentDir,
    openFilePath,
    floated: false,
    dirty: false,
  }
}

export function emptyTabsState(initial?: Partial<Pick<ExplorerTab, 'currentDir' | 'openFilePath'>>): ExplorerTabsState {
  const tab = createTab(initial)
  return { tabs: [tab], activeId: tab.id }
}

export type OpenTabResult =
  | { ok: true; state: ExplorerTabsState; tab: ExplorerTab }
  | { ok: false; reason: 'max_tabs'; state: ExplorerTabsState }

/** 新建标签并激活；达上限失败。 */
export function openTab(
  state: ExplorerTabsState,
  partial?: Partial<Pick<ExplorerTab, 'currentDir' | 'openFilePath' | 'title'>>,
): OpenTabResult {
  if (state.tabs.length >= MAX_EXPLORER_TABS) {
    return { ok: false, reason: 'max_tabs', state }
  }
  const tab = createTab(partial)
  return {
    ok: true,
    tab,
    state: { tabs: [...state.tabs, tab], activeId: tab.id },
  }
}

/** 关闭标签；至少保留一个时不允许清空到 0——若仅 1 个则 no-op 返回原 state。 */
export function closeTab(state: ExplorerTabsState, id: string): ExplorerTabsState {
  if (state.tabs.length <= 1) return state
  const idx = state.tabs.findIndex((t) => t.id === id)
  if (idx < 0) return state
  const tabs = state.tabs.filter((t) => t.id !== id)
  let activeId = state.activeId
  if (activeId === id) {
    const neighbor = state.tabs[idx + 1] ?? state.tabs[idx - 1]
    activeId = neighbor && neighbor.id !== id ? neighbor.id : tabs[0].id
    // neighbor 可能是被删的；取过滤后邻位
    activeId = tabs[Math.min(idx, tabs.length - 1)]?.id ?? tabs[0].id
  }
  return { tabs, activeId }
}

export function activateTab(state: ExplorerTabsState, id: string): ExplorerTabsState {
  if (!state.tabs.some((t) => t.id === id)) return state
  return { ...state, activeId: id }
}

export type FloatResult =
  | { ok: true; state: ExplorerTabsState }
  | { ok: false; reason: 'max_floats' | 'not_found'; state: ExplorerTabsState }

/** 弹出为浮动；已浮动则 no-op 成功。 */
export function floatTab(state: ExplorerTabsState, id: string): FloatResult {
  const tab = state.tabs.find((t) => t.id === id)
  if (!tab) return { ok: false, reason: 'not_found', state }
  if (tab.floated) return { ok: true, state }
  const floats = state.tabs.filter((t) => t.floated).length
  if (floats >= MAX_EXPLORER_FLOATS) {
    return { ok: false, reason: 'max_floats', state }
  }
  const tabs = state.tabs.map((t) => (t.id === id ? { ...t, floated: true } : t))
  // 弹出后若当前签被浮起，激活一个未浮动的邻签
  let activeId = state.activeId
  if (activeId === id) {
    const docked = tabs.filter((t) => !t.floated)
    if (docked.length > 0) activeId = docked[0].id
  }
  return { ok: true, state: { tabs, activeId } }
}

/** 收回浮动。 */
export function dockTab(state: ExplorerTabsState, id: string): ExplorerTabsState {
  if (!state.tabs.some((t) => t.id === id)) return state
  const tabs = state.tabs.map((t) => (t.id === id ? { ...t, floated: false } : t))
  return { tabs, activeId: id }
}

/** 更新标签上下文（目录/文件/脏/标题）。 */
export function updateTabContext(
  state: ExplorerTabsState,
  id: string,
  ctx: { dir?: string; file?: string | null; dirty?: boolean; title?: string },
): ExplorerTabsState {
  const tabs = state.tabs.map((t) => {
    if (t.id !== id) return t
    const currentDir = ctx.dir !== undefined ? ctx.dir : t.currentDir
    const openFilePath =
      ctx.file === null ? undefined : ctx.file !== undefined ? ctx.file : t.openFilePath
    const dirty = ctx.dirty !== undefined ? ctx.dirty : t.dirty
    const title =
      ctx.title ??
      (ctx.dir !== undefined || ctx.file !== undefined
        ? titleFromPath(currentDir, openFilePath)
        : t.title)
    return { ...t, currentDir, openFilePath, dirty, title }
  })
  return { ...state, tabs }
}

export function getActiveTab(state: ExplorerTabsState): ExplorerTab | undefined {
  return state.tabs.find((t) => t.id === state.activeId)
}

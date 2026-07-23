/**
 * 资源管理器导航历史栈（FR-375）。
 * 条目为相对工作目录路径（空串=根）；支持 push / back / forward。
 */

export interface NavHistoryState {
  /** 过去路径（不含 current）。index 0 最旧。 */
  stack: string[]
  /** 当前路径。 */
  current: string
  /** 前进栈（从近到远）。 */
  forward: string[]
}

export function emptyNavHistory(initial = ''): NavHistoryState {
  return { stack: [], current: initial, forward: [] }
}

/** 导航到新路径：压入历史并清空前进栈。同路径 no-op。 */
export function navPush(state: NavHistoryState, path: string): NavHistoryState {
  if (path === state.current) return state
  return {
    stack: [...state.stack, state.current],
    current: path,
    forward: [],
  }
}

export function canNavBack(state: NavHistoryState): boolean {
  return state.stack.length > 0
}

export function canNavForward(state: NavHistoryState): boolean {
  return state.forward.length > 0
}

/** 后退一步；无历史时返回原 state。 */
export function navBack(state: NavHistoryState): NavHistoryState {
  if (state.stack.length === 0) return state
  const prev = state.stack[state.stack.length - 1]
  return {
    stack: state.stack.slice(0, -1),
    current: prev,
    forward: [state.current, ...state.forward],
  }
}

/** 前进一步。 */
export function navForward(state: NavHistoryState): NavHistoryState {
  if (state.forward.length === 0) return state
  const [next, ...rest] = state.forward
  return {
    stack: [...state.stack, state.current],
    current: next,
    forward: rest,
  }
}

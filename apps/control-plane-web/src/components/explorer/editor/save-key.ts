/**
 * Ctrl+S / Cmd+S 保存键判定（FR-070）。纯函数，可单测。
 * 编辑器与全局监听都用它判断「是否应触发保存」，统一行为、避免与浏览器默认保存网页冲突。
 */
export interface SaveKeyEvent {
  key: string
  ctrlKey?: boolean
  metaKey?: boolean
}

/** 是否为保存组合键：Ctrl+S（Win/Linux）或 Cmd+S（macOS）。 */
export function isSaveKey(e: SaveKeyEvent): boolean {
  const isS = e.key === 's' || e.key === 'S'
  return isS && (e.ctrlKey === true || e.metaKey === true)
}

import { HOT_SET_SIZE } from '@/lib/terminal-session-manager'

/**
 * 跨服热缓存 LRU 纯逻辑（FR-296，ADR-067）：热集为「越靠前越新」的实例 id 数组，
 * 队首恒为当前活跃实例。抽为纯函数便于单测（宿主组件 InstanceConsoleCache 消费）。
 */

/** 把 id 置顶（命中则前移、未命中则插入队首），不做容量裁剪（淘汰另行决策）。 */
export function promoteHotSet(prev: number[], id: number): number[] {
  if (prev[0] === id) return prev
  return [id, ...prev.filter((x) => x !== id)]
}

/**
 * 超容时选淘汰目标：不淘汰队首（当前活跃）；从队尾（最久未用）向前找
 * 第一个**无未保存草稿**的成员；全部带草稿则被迫淘汰队尾（调用方负责 toast 警示）。
 * 未超容返回 null。
 */
export function pickEvictionTarget(
  hotSet: number[],
  hasDraft: (id: number) => boolean,
  capacity = HOT_SET_SIZE,
): number | null {
  if (hotSet.length <= capacity) return null
  const candidates = hotSet.slice(1)
  for (let i = candidates.length - 1; i >= 0; i--) {
    if (!hasDraft(candidates[i])) return candidates[i]
  }
  return candidates[candidates.length - 1]
}

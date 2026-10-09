/**
 * 跨服热缓存 LRU 纯逻辑（FR-296，ADR-067）：热集为「越靠前越新」的实例 id 数组，
 * 队首恒为当前活跃实例。抽为纯函数便于单测（宿主组件 InstanceConsoleCache 消费）。
 *
 * FR-414 起 `capacity` 由「默认取 HOT_SET_SIZE」改为**必传**：容量已不再是一个常量，
 * 而是 `resolveHotSetCapacity(可见终端数)` 的结果。留默认值会让调用方悄悄用上过时的
 * 基线，把正在被看的终端淘汰掉——这正是 FR-414 要修的病。
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
 *
 * @param capacity 生效容量，必传（FR-414）——由 `resolveHotSetCapacity` 求得。
 */
export function pickEvictionTarget(
  hotSet: number[],
  hasDraft: (id: number) => boolean,
  capacity: number,
): number | null {
  if (hotSet.length <= capacity) return null
  const candidates = hotSet.slice(1)
  for (let i = candidates.length - 1; i >= 0; i--) {
    if (!hasDraft(candidates[i])) return candidates[i]
  }
  return candidates[candidates.length - 1]
}

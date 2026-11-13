import type { LogEntry } from '@/lib/console/console-log-types'

/** Stable newest-first merge. Unknown timestamps stay after known timestamps. */
export function mergeLogEntries(sources: readonly (readonly LogEntry[])[]): LogEntry[] {
  const rows = sources.flatMap((items) => items).map((item, index) => ({ item, index }))
  const usedIds = new Set<number>()
  return rows
    .sort((left, right) => {
      const leftTime = Date.parse(left.item.time)
      const rightTime = Date.parse(right.item.time)
      const leftKnown = Number.isFinite(leftTime)
      const rightKnown = Number.isFinite(rightTime)
      if (leftKnown !== rightKnown) return leftKnown ? -1 : 1
      if (leftKnown && rightKnown && leftTime !== rightTime) return rightTime - leftTime
      return left.index - right.index
    })
    .map(({ item }) => {
      let id = item.id
      while (usedIds.has(id)) id = id === 2147483647 ? -2147483648 : id + 1
      usedIds.add(id)
      return { ...item, id }
    })
}

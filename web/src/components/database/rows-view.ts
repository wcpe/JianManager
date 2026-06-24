import type { DbColumn, DbRowsResult } from '@/api/db'

/**
 * 数据库行浏览的纯逻辑（FR-084 / BUG-009）。
 *
 * 后端 Go 把空结果的 nil 切片序列化为 JSON `null`（而非 `[]`），
 * 因此 `DbRowsResult.rows` / `columns` 在「空表 / 过滤无命中 / 加载首帧」时可能是 `null`。
 * 组件渲染若对其裸读 `.length`/`.map` 会抛 `Cannot read properties of null (reading 'length')`
 * 致整页白屏。这里集中做空值归一与判定，组件只消费已归一的安全值。
 */

/** 把可能为 null/undefined 的列定义归一为数组（永不为 null）。 */
export function normalizeColumns(data: DbRowsResult | undefined): DbColumn[] {
  return data?.columns ?? []
}

/** 把可能为 null/undefined 的行集合归一为数组（永不为 null）。 */
export function normalizeRows(
  data: DbRowsResult | undefined,
): Array<Record<string, unknown>> {
  return data?.rows ?? []
}

/**
 * 是否渲染「空行」占位：尚无数据、或当前页归一后行数为 0 时为 true。
 * 关键：不得对 `data.rows` 裸读 `.length`——`rows` 可能为 `null`（见上）。
 */
export function shouldShowEmptyRow(data: DbRowsResult | undefined): boolean {
  return normalizeRows(data).length === 0
}

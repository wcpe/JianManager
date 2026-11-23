import type { DbColumn, DbRowsResult } from './db-contracts'

/** 
 * 数据库行浏览的纯逻辑（BUG-009）。
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

/** 主键类列的候选列名（GORM 表几乎都是 `id`）。 */
const ID_COLUMNS = ['id', 'ID'] as const

/** 
 * 为当前页每行算出稳定 key（与行内容一一对应，而不是数组下标）。
 *
 * 为什么不能继续用下标：排序（点列头）、翻页、过滤都会让「同一位置」换成另一条记录，
 * React 会把上一行的 DOM 身份直接套给新数据——行内的文本选择、焦点、以及未来任何
 * 行级状态都会错位到别的记录上。
 *
 * 取值优先级：
 * 1. 主键类列（`id`）：最稳，翻页/排序后仍是同一行的身份；
 * 2. 整行内容指纹（视图/联表结果没有主键时的兜底）；
 * 3. 下标（内容指纹也不可得，如行内出现循环引用）。
 *
 * 同页出现完全相同的两行（无主键的视图结果）时，指纹会撞车，故追加出现序号去重，
 * 保证 key 在页内唯一（否则 React 会报重复 key 并只保留一行）。
 */
export function rowKeys(rows: Array<Record<string, unknown>>): string[] {
  const seen = new Map<string, number>()
  return rows.map((row, index) => {
    const base = baseRowKey(row, index)
    const hit = seen.get(base) ?? 0
    seen.set(base, hit + 1)
    return hit === 0 ? base : `${base}#${hit}`
  })
}

/** 单行的基准 key（未去重）：主键 → 内容指纹 → 下标。 */
function baseRowKey(row: Record<string, unknown>, index: number): string {
  for (const column of ID_COLUMNS) {
    const value = row[column]
    if (value !== undefined && value !== null) return `id:${String(value)}`
  }
  try {
    return `row:${JSON.stringify(row)}`
  } catch {
    // 行内出现不可序列化的值（循环引用 / BigInt）：退回下标，至少不抛错。
    return `idx:${index}`
  }
}

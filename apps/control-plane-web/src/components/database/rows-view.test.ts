import { describe, it, expect } from 'vitest'
import type { DbRowsResult } from '@/api/db'
import { normalizeColumns, normalizeRows, rowKeys, shouldShowEmptyRow } from '@/lib/database/db-rows-view'

/**
 * BUG-009 回归：后端把空结果序列化为 `rows: null` / `columns: null`，
 * 组件初始渲染（硬刷新 /database，首个表恰为空）时对其裸读 `.length` 会抛
 * `Cannot read properties of null (reading 'length')` 致整页白屏。
 * 下列用例锁死「null 不得致崩、且判定正确」。
 */

// 后端对空表/无命中过滤返回 null 切片（Go nil slice → JSON null）。
const nullRows = {
  table: 'audit_logs',
  columns: null,
  rows: null,
  page: 1,
  pageSize: 50,
  total: 0,
} as unknown as DbRowsResult

const withRows: DbRowsResult = {
  table: 'users',
  columns: [{ name: 'id', type: 'integer', sensitive: false }],
  rows: [{ id: 1 }, { id: 2 }],
  page: 1,
  pageSize: 50,
  total: 2,
}

const emptyArrays: DbRowsResult = {
  table: 'users',
  columns: [],
  rows: [],
  page: 1,
  pageSize: 50,
  total: 0,
}

describe('BUG-009 rows-view null 守卫', () => {
  it('rows 为 null 时 normalizeRows 归一为空数组而不抛', () => {
    expect(() => normalizeRows(nullRows)).not.toThrow()
    expect(normalizeRows(nullRows)).toEqual([])
  })

  it('columns 为 null 时 normalizeColumns 归一为空数组而不抛', () => {
    expect(() => normalizeColumns(nullRows)).not.toThrow()
    expect(normalizeColumns(nullRows)).toEqual([])
  })

  it('data 为 undefined（加载首帧）时不抛且归一为空', () => {
    expect(normalizeRows(undefined)).toEqual([])
    expect(normalizeColumns(undefined)).toEqual([])
    expect(shouldShowEmptyRow(undefined)).toBe(true)
  })

  it('rows 为 null 时判定为空行占位且不读 null.length', () => {
    // 这正是崩溃点 `data.rows.length` 的复现：旧逻辑会在此抛异常。
    expect(() => shouldShowEmptyRow(nullRows)).not.toThrow()
    expect(shouldShowEmptyRow(nullRows)).toBe(true)
  })

  it('空数组判定为空行占位', () => {
    expect(shouldShowEmptyRow(emptyArrays)).toBe(true)
  })

  it('有行时不显示空行占位且行/列归一保持原值', () => {
    expect(shouldShowEmptyRow(withRows)).toBe(false)
    expect(normalizeRows(withRows)).toHaveLength(2)
    expect(normalizeColumns(withRows)).toHaveLength(1)
  })
})

/**
 * 数据行 key：排序 / 翻页后同一记录必须保持同一身份。
 *
 * 用数组下标当 key 时，翻页与排序会把「同一位置」换成另一条记录，React 会把上一行的
 * DOM 身份直接套给新数据（行内文本选择、焦点与任何行级状态都会错位到别的记录上）。
 */
describe('rowKeys（行稳定 key）', () => {
  it('优先取主键列 id，与行在数组中的位置无关', () => {
    const keys = rowKeys([{ id: 7, name: 'a' }, { id: 9, name: 'b' }])
    expect(keys).toEqual(['id:7', 'id:9'])
    // 排序/翻页后同一记录仍是同一个 key（不随下标变化）。
    expect(rowKeys([{ id: 9, name: 'b' }, { id: 7, name: 'a' }])).toEqual(['id:9', 'id:7'])
  })

  it('无主键列时退回整行内容指纹', () => {
    const keys = rowKeys([{ name: 'a', v: 1 }, { name: 'b', v: 2 }])
    expect(keys).toEqual([`row:${JSON.stringify({ name: 'a', v: 1 })}`, `row:${JSON.stringify({ name: 'b', v: 2 })}`])
  })

  it('内容完全相同的多行（无主键视图结果）仍产出唯一 key', () => {
    const keys = rowKeys([{ name: 'dup' }, { name: 'dup' }, { name: 'dup' }])
    expect(new Set(keys).size).toBe(3)
  })

  it('id 为 null 时不吃主键分支（回退内容指纹），空集合返回空数组', () => {
    expect(rowKeys([{ id: null, name: 'a' }])).toEqual([`row:${JSON.stringify({ id: null, name: 'a' })}`])
    expect(rowKeys([])).toEqual([])
  })
})

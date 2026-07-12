import { describe, it, expect } from 'vitest'
import type { DbRowsResult } from '@/api/db'
import { normalizeColumns, normalizeRows, shouldShowEmptyRow } from './rows-view'

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

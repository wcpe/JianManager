import { describe, it, expect } from 'vitest'
import { needsDiscardConfirm, type OpenFileSnapshot } from './discard-guard'

const clean: OpenFileSnapshot = { path: 'a.txt', saved: 'x', draft: 'x' }
const dirty: OpenFileSnapshot = { path: 'a.txt', saved: 'x', draft: 'y' }

describe('needsDiscardConfirm — 文本编辑器脏态（BUG-018）', () => {
  it('未打开文件 + 无配置脏态：关闭不拦截', () => {
    expect(needsDiscardConfirm(null, false)).toBe(false)
  })

  it('未打开文件 + 无配置脏态：切换不拦截', () => {
    expect(needsDiscardConfirm(null, false, 'b.txt')).toBe(false)
  })

  it('已打开但干净：切换不拦截', () => {
    expect(needsDiscardConfirm(clean, false, 'b.txt')).toBe(false)
  })

  it('有未保存草稿：切到其他文件需确认', () => {
    expect(needsDiscardConfirm(dirty, false, 'b.txt')).toBe(true)
  })

  it('有未保存草稿：关闭（无目标路径）需确认', () => {
    expect(needsDiscardConfirm(dirty, false)).toBe(true)
  })

  it('有未保存草稿：重开正在编辑的同一文件不拦截（path === nextPath）', () => {
    expect(needsDiscardConfirm(dirty, false, 'a.txt')).toBe(false)
  })
})

describe('needsDiscardConfirm — 配置编辑器自管脏态（BUG-018 #36）', () => {
  it('配置脏态 + 未打开文本文件：切换需确认', () => {
    expect(needsDiscardConfirm(null, true, 'b.txt')).toBe(true)
  })

  it('配置脏态 + 未打开文本文件：关闭需确认', () => {
    expect(needsDiscardConfirm(null, true)).toBe(true)
  })

  it('配置脏态：重开正在编辑的同一文件不拦截', () => {
    expect(needsDiscardConfirm({ ...clean }, true, 'a.txt')).toBe(false)
  })

  it('文本干净但配置脏态：切到其他文件需确认', () => {
    expect(needsDiscardConfirm(clean, true, 'b.txt')).toBe(true)
  })

  it('文本与配置均干净：不拦截', () => {
    expect(needsDiscardConfirm(clean, false, 'b.txt')).toBe(false)
  })
})

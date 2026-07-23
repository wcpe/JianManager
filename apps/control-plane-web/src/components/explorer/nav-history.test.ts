import { describe, it, expect } from 'vitest'
import {
  emptyNavHistory,
  navPush,
  navBack,
  navForward,
  canNavBack,
  canNavForward,
} from './nav-history'

describe('nav-history（FR-375）', () => {
  it('push / back / forward 闭环', () => {
    let s = emptyNavHistory('')
    s = navPush(s, 'plugins')
    s = navPush(s, 'plugins/Essentials')
    expect(s.current).toBe('plugins/Essentials')
    expect(canNavBack(s)).toBe(true)
    s = navBack(s)
    expect(s.current).toBe('plugins')
    expect(canNavForward(s)).toBe(true)
    s = navForward(s)
    expect(s.current).toBe('plugins/Essentials')
  })

  it('同路径 push 不改栈', () => {
    let s = emptyNavHistory('a')
    const next = navPush(s, 'a')
    expect(next).toBe(s)
  })

  it('新 push 清空 forward', () => {
    let s = emptyNavHistory('')
    s = navPush(s, 'a')
    s = navPush(s, 'b')
    s = navBack(s)
    expect(canNavForward(s)).toBe(true)
    s = navPush(s, 'c')
    expect(canNavForward(s)).toBe(false)
    expect(s.current).toBe('c')
  })
})

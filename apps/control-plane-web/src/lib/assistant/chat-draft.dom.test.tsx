import { beforeEach, describe, expect, it } from 'vitest'
import {
  clearDraft,
  loadDraft,
  saveDraft,
  resetDraftScope,
  setDraftScopeUserId,
} from '@/lib/assistant/chat-draft'

/** 
 * 草稿的会话维度与**用户维度**隔离（§7）：
 * 「切到另一会话再切回，草稿仍在；登出换用户后草稿与缓存清空」。
 *
 * 【为什么必须在 key 里带 userId 而不是靠登出时清理】
 * 草稿存 `sessionStorage`，它**不随登出自动清空**（只在关标签页时消失）。
 * 若只靠 `logout()` 里手写清理：一旦某条登出路径漏了（例如 token 过期被动登出、
 * 或直接改 URL 到登录页），上一个人的草稿就会出现在下一个人的输入框里。
 * 让 key 本身带 owner，是把「串号」变成**结构上不可能**，而不是靠记得清理。
 *
 * 这条是纯前端逻辑，不需要真实后端即可验证。
 */
// 与 chat-draft.ts 的 DRAFT_PREFIX 保持一致。该常量未导出，故此处硬编码；
// 若将来改了前缀，这两条**断言 key 形状**的用例会立刻变红——那是期望的行为
// （key 形状是隔离机制的载体，改了就该有人重新确认隔离是否仍然成立）。
const PREFIX = 'jm-assistant-draft:'

describe('草稿隔离 · 会话维度', () => {
  beforeEach(() => {
    sessionStorage.clear()
    setDraftScopeUserId(1)
  })

  it('切到另一会话再切回，草稿仍在', () => {
    saveDraft(10, '会话 10 的草稿')
    saveDraft(20, '会话 20 的草稿')

    // 切回 10
    expect(loadDraft(10)).toBe('会话 10 的草稿')
    // 20 的草稿互不影响
    expect(loadDraft(20)).toBe('会话 20 的草稿')
    // 新会话（conversationId=null）自有独立槽位
    expect(loadDraft(null)).toBe('')
  })

  it('新会话的草稿与任意具体会话互不干扰', () => {
    saveDraft(null, '还没选会话时写的')
    saveDraft(5, '会话 5 的')
    expect(loadDraft(null)).toBe('还没选会话时写的')
    expect(loadDraft(5)).toBe('会话 5 的')
  })

  it('清空草稿即删除键，不留下无意义空串', () => {
    saveDraft(7, '临时')
    saveDraft(7, '')
    expect(loadDraft(7)).toBe('')
    expect(sessionStorage.getItem(`${PREFIX}1:7`), '空内容应删键而不是写空串').toBeNull()
  })
})

describe('草稿隔离 · 用户维度（换用户不串号）', () => {
  beforeEach(() => {
    sessionStorage.clear()
  })

  it('换用户后读不到上一个用户的草稿', () => {
    setDraftScopeUserId(1)
    saveDraft(3, '用户1的私密草稿')
    expect(loadDraft(3), '用户 1 自己能看到').toBe('用户1的私密草稿')

    // 换用户：不清理 sessionStorage，只切 scope
    setDraftScopeUserId(2)
    expect(loadDraft(3), '换用户后必须读不到').toBe('')

    // 切回用户 1，草稿仍在（不是被删了，只是隔离）
    setDraftScopeUserId(1)
    expect(loadDraft(3), '切回原用户应恢复').toBe('用户1的私密草稿')
  })

  it('两个用户对同一会话 id 的草稿互不覆盖', () => {
    setDraftScopeUserId(1)
    saveDraft(9, 'A 写的')
    setDraftScopeUserId(2)
    saveDraft(9, 'B 写的')

    setDraftScopeUserId(1)
    expect(loadDraft(9)).toBe('A 写的')
    setDraftScopeUserId(2)
    expect(loadDraft(9)).toBe('B 写的')
  })

  it('未登录（anon）的草稿不会与任何用户的草稿混同', () => {
    setDraftScopeUserId(0) // 未登录
    saveDraft(4, '匿名时写的')
    expect(sessionStorage.getItem(`${PREFIX}anon:4`), '未登录应落在 anon 键').toBe('匿名时写的')

    setDraftScopeUserId(1)
    expect(loadDraft(4), '登录后读不到匿名草稿').toBe('')
  })

  it('clearDraft 只清当前用户当前会话的那一个键', () => {
    setDraftScopeUserId(1)
    saveDraft(1, 'u1-c1')
    saveDraft(2, 'u1-c2')
    setDraftScopeUserId(2)
    saveDraft(1, 'u2-c1')

    setDraftScopeUserId(1)
    clearDraft(1)
    expect(loadDraft(1), '当前用户当前会话已清').toBe('')
    expect(loadDraft(2), '同用户其它会话不受影响').toBe('u1-c2')

    setDraftScopeUserId(2)
    expect(loadDraft(1), '别的用户的同号会话不受影响').toBe('u2-c1')
  })
})

/** 
 * `resetDraftScope`：登出/换用户时把上一个用户的草稿**真删掉**（不只是隔离）。
 *
 * 与「key 带 owner」互补：隔离让新用户读不到，删除让旧数据不长期留在浏览器里。
 * 两者都要——只隔离不删，等于把别人的未发送内容一直存着。
 */
describe('换用户时真正清理（resetDraftScope）', () => {
  beforeEach(() => {
    sessionStorage.clear()
  })

  it('删除上一个用户的全部草稿键，且不影响其它键', () => {
    setDraftScopeUserId(1)
    saveDraft(1, 'u1-a')
    saveDraft(2, 'u1-b')
    sessionStorage.setItem('unrelated-key', 'keep-me')

    resetDraftScope(2)

    expect(sessionStorage.getItem(`${PREFIX}1:1`), 'u1 的会话 1 草稿应被删').toBeNull()
    expect(sessionStorage.getItem(`${PREFIX}1:2`), 'u1 的会话 2 草稿应被删').toBeNull()
    expect(sessionStorage.getItem('unrelated-key'), '无关键不该被波及').toBe('keep-me')

    // 已经切到新用户作用域：此时写入应落在 u2 命名空间
    saveDraft(1, 'u2-a')
    expect(sessionStorage.getItem(`${PREFIX}2:1`)).toBe('u2-a')
  })

  it('先删旧再切换顺序正确：旧键不会被新作用域误读', () => {
    setDraftScopeUserId(5)
    saveDraft(8, 'user5-draft')
    resetDraftScope(6)

    expect(loadDraft(8), '新用户读不到旧用户的同号会话草稿').toBe('')
    expect(sessionStorage.getItem(`${PREFIX}5:8`), '旧键已删除而不是残留').toBeNull()
  })
})

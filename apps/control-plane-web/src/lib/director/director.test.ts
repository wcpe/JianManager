import { describe, it, expect } from 'vitest'
import {
  DEFAULT_PREHEAT_LIMIT,
  MAX_PREHEAT_LIMIT,
  MIN_PREHEAT_LIMIT,
  activateScene,
  addScene,
  clampLimit,
  createDirectorState,
  nextSceneId,
  preheatedIds,
  removeScene,
  sceneStatus,
  setLimit,
  type DirectorState,
} from './director'

/** 便捷构造：给定场景列表与上限的初始态（无激活）。 */
function state(sceneIds: string[], limit = DEFAULT_PREHEAT_LIMIT): DirectorState {
  return createDirectorState(sceneIds, limit)
}

describe('createDirectorState', () => {
  it('初始无激活、预热集合为空、上限被夹紧', () => {
    const s = state(['a', 'b', 'c'])
    expect(s.sceneIds).toEqual(['a', 'b', 'c'])
    expect(s.activeId).toBeNull()
    expect(preheatedIds(s)).toEqual([])
    expect(s.limit).toBe(DEFAULT_PREHEAT_LIMIT)
  })

  it('上限超界被夹回 [MIN, MAX]', () => {
    expect(createDirectorState(['a'], 0).limit).toBe(MIN_PREHEAT_LIMIT)
    expect(createDirectorState(['a'], 999).limit).toBe(MAX_PREHEAT_LIMIT)
  })
})

describe('clampLimit', () => {
  it('夹回区间，非有限值回退默认', () => {
    expect(clampLimit(2)).toBe(2)
    expect(clampLimit(-5)).toBe(MIN_PREHEAT_LIMIT)
    expect(clampLimit(1000)).toBe(MAX_PREHEAT_LIMIT)
    expect(clampLimit(Number.NaN)).toBe(DEFAULT_PREHEAT_LIMIT)
    expect(clampLimit(2.7)).toBe(2) // 取整向下
  })
})

describe('sceneStatus / activateScene — 三态与激活唯一', () => {
  it('激活一个场景后它是 active、其余 cold', () => {
    const s = activateScene(state(['a', 'b', 'c']), 'a')
    expect(s.activeId).toBe('a')
    expect(sceneStatus(s, 'a')).toBe('active')
    expect(sceneStatus(s, 'b')).toBe('cold')
    expect(sceneStatus(s, 'c')).toBe('cold')
    // active 也算保活连接，计入预热集合
    expect(preheatedIds(s)).toEqual(['a'])
  })

  it('激活唯一：切到新场景后旧激活降为预热（保活）', () => {
    let s = activateScene(state(['a', 'b', 'c']), 'a')
    s = activateScene(s, 'b')
    expect(s.activeId).toBe('b')
    expect(sceneStatus(s, 'a')).toBe('preheated')
    expect(sceneStatus(s, 'b')).toBe('active')
    // a 仍保活在池中（瞬切零延迟的前提）
    expect(preheatedIds(s).sort()).toEqual(['a', 'b'])
  })

  it('激活不存在的场景为 no-op（返回原态）', () => {
    const s0 = activateScene(state(['a', 'b']), 'a')
    const s1 = activateScene(s0, 'zzz')
    expect(s1).toBe(s0)
  })

  it('重复激活同一场景刷新其 LRU 近用但不改三态', () => {
    let s = activateScene(state(['a', 'b'], 2), 'a')
    s = activateScene(s, 'b')
    // 此时 a 是最久未用；再激活 a 刷新近用
    s = activateScene(s, 'a')
    expect(s.activeId).toBe('a')
    expect(preheatedIds(s).sort()).toEqual(['a', 'b'])
  })
})

describe('activateScene — 并发上限 + LRU 驱逐', () => {
  it('达上限时驱逐最久未激活的预热场景（非 active）', () => {
    // 上限 2：池最多 2 个保活连接（含 active）
    let s = activateScene(state(['a', 'b', 'c'], 2), 'a') // 池=[a]
    s = activateScene(s, 'b') // 池=[a,b]，b active，a 预热
    s = activateScene(s, 'c') // 超限：驱逐最久未用预热 a → 池=[b,c]，c active
    expect(s.activeId).toBe('c')
    expect(preheatedIds(s).sort()).toEqual(['b', 'c'])
    expect(sceneStatus(s, 'a')).toBe('cold') // a 被驱逐，下次切换需重连
    expect(sceneStatus(s, 'b')).toBe('preheated')
    expect(sceneStatus(s, 'c')).toBe('active')
  })

  it('LRU 顺序：近用的预热不被驱逐，最久未用的先驱逐', () => {
    // 上限 3，序列让 b 比 a 更近用
    let s = activateScene(state(['a', 'b', 'c', 'd'], 3), 'a') // [a]
    s = activateScene(s, 'b') // [a,b]
    s = activateScene(s, 'a') // 刷新 a 近用 → 近用序: b(旧) < a(新)，active=a
    s = activateScene(s, 'c') // [a,b,c]，active=c，预热=a,b
    s = activateScene(s, 'd') // 超限：预热中最久未用是 b（a 在上一步后比 b 新）→ 驱逐 b
    expect(s.activeId).toBe('d')
    expect(sceneStatus(s, 'b')).toBe('cold')
    expect(preheatedIds(s).sort()).toEqual(['a', 'c', 'd'])
  })

  it('永不驱逐 active：即使 active 是最久未"重新激活"的，也不在驱逐候选内', () => {
    // 上限 2：a 激活后一直不动，b/c 轮流来
    let s = activateScene(state(['a', 'b', 'c'], 2), 'a')
    s = activateScene(s, 'b') // 驱逐候选只有预热的 a？不——a 此刻是预热；b active
    // 上限 2 池=[a,b]
    s = activateScene(s, 'c') // 超限→驱逐预热 a（b 是 active 不驱逐）
    expect(sceneStatus(s, 'b')).toBe('preheated')
    expect(sceneStatus(s, 'a')).toBe('cold')
  })
})

describe('setLimit — 调小上限即时驱逐溢出', () => {
  it('调小到 N 后预热集合裁到 N（保留 active + 最近用）', () => {
    let s = activateScene(state(['a', 'b', 'c', 'd'], 4), 'a')
    s = activateScene(s, 'b')
    s = activateScene(s, 'c')
    s = activateScene(s, 'd') // 池=[a,b,c,d]，active=d
    s = setLimit(s, 2) // 裁到 2：保留 active=d + 最近用一个=c，驱逐 a、b
    expect(s.limit).toBe(2)
    expect(s.activeId).toBe('d')
    expect(preheatedIds(s).sort()).toEqual(['c', 'd'])
    expect(sceneStatus(s, 'a')).toBe('cold')
    expect(sceneStatus(s, 'b')).toBe('cold')
  })

  it('调大上限不驱逐，已预热保持', () => {
    let s = activateScene(state(['a', 'b'], 2), 'a')
    s = activateScene(s, 'b')
    s = setLimit(s, 4)
    expect(s.limit).toBe(4)
    expect(preheatedIds(s).sort()).toEqual(['a', 'b'])
  })

  it('上限被夹紧到合法区间', () => {
    const s = setLimit(state(['a']), 999)
    expect(s.limit).toBe(MAX_PREHEAT_LIMIT)
  })
})

describe('addScene / removeScene', () => {
  it('加场景追加到末尾，重复 id 忽略', () => {
    let s = addScene(state(['a']), 'b')
    expect(s.sceneIds).toEqual(['a', 'b'])
    s = addScene(s, 'b')
    expect(s.sceneIds).toEqual(['a', 'b'])
  })

  it('删场景从列表与预热集合移除；删的是 active 则清空 active', () => {
    let s = activateScene(state(['a', 'b', 'c'], 3), 'a')
    s = activateScene(s, 'b') // active=b，预热=a,b
    s = removeScene(s, 'b')
    expect(s.sceneIds).toEqual(['a', 'c'])
    expect(s.activeId).toBeNull()
    expect(preheatedIds(s)).toEqual(['a']) // a 仍保活
    expect(sceneStatus(s, 'b')).toBe('cold')
  })

  it('删非 active 的预热场景只从池移除，active 不变', () => {
    let s = activateScene(state(['a', 'b'], 2), 'a')
    s = activateScene(s, 'b') // active=b 预热=a,b
    s = removeScene(s, 'a')
    expect(s.activeId).toBe('b')
    expect(preheatedIds(s)).toEqual(['b'])
  })
})

describe('nextSceneId — 轮播序列', () => {
  it('按 sceneIds 顺序环绕到下一个', () => {
    const s = state(['a', 'b', 'c'])
    expect(nextSceneId(s, 'a')).toBe('b')
    expect(nextSceneId(s, 'b')).toBe('c')
    expect(nextSceneId(s, 'c')).toBe('a') // 环绕
  })

  it('当前为 null 或未知时从第一个开始', () => {
    const s = state(['a', 'b', 'c'])
    expect(nextSceneId(s, null)).toBe('a')
    expect(nextSceneId(s, 'zzz')).toBe('a')
  })

  it('空场景列表返回 null', () => {
    expect(nextSceneId(state([]), null)).toBeNull()
  })

  it('单场景轮播指回自身', () => {
    const s = state(['only'])
    expect(nextSceneId(s, 'only')).toBe('only')
  })
})

describe('不可变性 — 纯函数不改入参', () => {
  it('activateScene 返回新对象，入参不被改', () => {
    const s0 = state(['a', 'b'])
    const before = JSON.stringify(s0)
    const s1 = activateScene(s0, 'a')
    expect(JSON.stringify(s0)).toBe(before)
    expect(s1).not.toBe(s0)
  })
})

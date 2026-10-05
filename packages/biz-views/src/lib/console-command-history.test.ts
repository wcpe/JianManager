import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  COMMAND_HISTORY_LIMIT,
  appendCommandHistory,
  clearCommandHistory,
  loadCommandHistory,
  pushCommandHistory,
  saveCommandHistory,
  searchCommandHistory,
} from './console-command-history'

/** 命令历史（FR-416）：按实例持久化 + 相邻去重 + 上限 + `^R` 模糊搜索。 */

const INSTANCE = 7
const OTHER = 8

/**
 * 本文件在 `node` project 下跑（vite.config 的双 project 划分），没有 jsdom 的 localStorage，
 * 故打一个**忠实实现 Storage 契约**的内存桩——模块只用到 get/set/remove。
 * 「刷新后历史仍在」的端到端证明另有 jsdom 组件用例（ConsoleCommandBar.dom.test.tsx），
 * 此处锁的是存储层语义（分桶 / 去重 / 上限 / 损坏容错）。
 */
beforeEach(() => {
  const store = new Map<string, string>()
  vi.stubGlobal('localStorage', {
    getItem: (key: string) => store.get(key) ?? null,
    setItem: (key: string, value: string) => void store.set(key, String(value)),
    removeItem: (key: string) => void store.delete(key),
    clear: () => store.clear(),
    key: (index: number) => [...store.keys()][index] ?? null,
    get length() {
      return store.size
    },
  })
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('appendCommandHistory（纯函数）', () => {
  it('追加到尾部（旧→新）', () => {
    expect(appendCommandHistory(['a'], 'b')).toEqual(['a', 'b'])
  })

  it('相邻重复不入：连按三次 list 只占一个坑', () => {
    let history: string[] = []
    for (let i = 0; i < 3; i++) history = appendCommandHistory(history, 'list')
    expect(history).toEqual(['list'])
  })

  it('非相邻重复保留：a b a 的第二个 a 是有意义的时序信息', () => {
    const history = ['a', 'b'].reduce<string[]>((acc, line) => appendCommandHistory(acc, line), [])
    expect(appendCommandHistory(history, 'a')).toEqual(['a', 'b', 'a'])
  })

  it('空行/纯空白不入，且原历史不被改动', () => {
    const before = ['a']
    expect(appendCommandHistory(before, '   ')).toEqual(['a'])
    expect(appendCommandHistory(before, '')).toEqual(['a'])
  })

  it('两侧空白被裁掉（否则 " list" 与 "list" 会成为两条不同历史）', () => {
    expect(appendCommandHistory([], '  list  ')).toEqual(['list'])
  })

  it(`超过 ${COMMAND_HISTORY_LIMIT} 条丢最旧`, () => {
    let history: string[] = []
    for (let i = 0; i < COMMAND_HISTORY_LIMIT + 5; i++) history = appendCommandHistory(history, `cmd-${i}`)
    expect(history).toHaveLength(COMMAND_HISTORY_LIMIT)
    expect(history[0]).toBe('cmd-5')
    expect(history.at(-1)).toBe(`cmd-${COMMAND_HISTORY_LIMIT + 4}`)
  })
})

describe('持久化（FR-416：刷新后历史仍在）', () => {
  it('push 后重新 load 拿到同一份历史——这就是「刷新后仍在」的存储层证明', () => {
    pushCommandHistory(INSTANCE, 'say hello')
    pushCommandHistory(INSTANCE, 'list')
    // 模拟刷新：不带任何内存态，纯从存储读回。
    expect(loadCommandHistory(INSTANCE)).toEqual(['say hello', 'list'])
  })

  it('按实例分桶：一个服的历史不出现在另一个服', () => {
    pushCommandHistory(INSTANCE, 'op alice')
    pushCommandHistory(OTHER, 'stop')
    expect(loadCommandHistory(INSTANCE)).toEqual(['op alice'])
    expect(loadCommandHistory(OTHER)).toEqual(['stop'])
  })

  it('相邻去重与上限跨刷新同样生效（写盘走同一条追加逻辑）', () => {
    pushCommandHistory(INSTANCE, 'list')
    pushCommandHistory(INSTANCE, 'list')
    expect(loadCommandHistory(INSTANCE)).toEqual(['list'])
  })

  it('存储被写坏（非 JSON / 非数组 / 混入非字符串）一律当空或过滤，不抛不污染输入框', () => {
    localStorage.setItem('console.cmdHistory.7', 'not-json{')
    expect(loadCommandHistory(INSTANCE)).toEqual([])

    localStorage.setItem('console.cmdHistory.7', '{"a":1}')
    expect(loadCommandHistory(INSTANCE)).toEqual([])

    // 混入对象/数字：过滤掉，否则 ↑ 会把 "[object Object]" 填进命令栏。
    localStorage.setItem('console.cmdHistory.7', JSON.stringify(['ok', { x: 1 }, 5, '', 'fine']))
    expect(loadCommandHistory(INSTANCE)).toEqual(['ok', 'fine'])
  })

  it('clearCommandHistory 只清目标实例', () => {
    pushCommandHistory(INSTANCE, 'a')
    pushCommandHistory(OTHER, 'b')
    clearCommandHistory(INSTANCE)
    expect(loadCommandHistory(INSTANCE)).toEqual([])
    expect(loadCommandHistory(OTHER)).toEqual(['b'])
  })

  it('saveCommandHistory 落盘时同样收口到上限', () => {
    saveCommandHistory(INSTANCE, Array.from({ length: COMMAND_HISTORY_LIMIT + 10 }, (_, i) => `c${i}`))
    expect(loadCommandHistory(INSTANCE)).toHaveLength(COMMAND_HISTORY_LIMIT)
  })
})

describe('searchCommandHistory（^R 模糊搜索）', () => {
  const history = ['say hello', 'gamemode creative Steve', 'list', 'op Steve', 'say hello']

  it('子序列匹配：字符按序出现即命中，无需连续', () => {
    // 'gmc' 匹配 "gamemode creative Steve" 的 g…m…c
    expect(searchCommandHistory(history, 'gmc').map((m) => m.value)).toContain('gamemode creative Steve')
  })

  it('结果新→旧排序（^R 的语义是往回找最近一次）', () => {
    const values = searchCommandHistory(history, 'e').map((m) => m.value)
    // 最新的 'say hello' 在最前；'say hello' 的更早那次被去重不再出现。
    expect(values[0]).toBe('say hello')
    expect(values.filter((v) => v === 'say hello')).toHaveLength(1)
  })

  it('大小写不敏感（记错大小写不该找不到）', () => {
    expect(searchCommandHistory(history, 'STEVE').map((m) => m.value)).toEqual([
      'op Steve',
      'gamemode creative Steve',
    ])
  })

  it('无命中返回空数组', () => {
    expect(searchCommandHistory(history, 'zzz')).toEqual([])
  })

  it('空查询返回全部（去重、新→旧），使 ^R 一按下就能当历史浏览器用', () => {
    expect(searchCommandHistory(history, '').map((m) => m.value)).toEqual([
      'say hello',
      'op Steve',
      'list',
      'gamemode creative Steve',
    ])
  })

  it('positions 指向命中字符在候选中的下标（供高亮）', () => {
    const [hit] = searchCommandHistory(['gamemode'], 'gm')
    expect(hit.positions).toEqual([0, 2])
    expect(hit.value[0]).toBe('g')
    expect(hit.value[2]).toBe('m')
  })
})

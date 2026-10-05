import { describe, expect, it } from 'vitest'

import { charWidthClass, countTextLines, estimateRowLines } from './console-wrap'

const SPEC = { charWidthPx: 10, wideWidthPx: 20, safetyPx: 0 }

describe('charWidthClass 字符宽度分类', () => {
  it('ASCII=1、CJK/全角=2、零宽=0', () => {
    expect(charWidthClass('a'.charCodeAt(0))).toBe(1)
    expect(charWidthClass('0'.charCodeAt(0))).toBe(1)
    expect(charWidthClass('中'.charCodeAt(0))).toBe(2)
    expect(charWidthClass('，'.charCodeAt(0))).toBe(2) // FF0C 全角逗号
    expect(charWidthClass('あ'.charCodeAt(0))).toBe(2) // 平假名
    expect(charWidthClass(0x200b)).toBe(0) // 零宽空格
    expect(charWidthClass(0x0301)).toBe(0) // 组合尖音
    expect(charWidthClass(0xfe0f)).toBe(0) // 变体选择符
  })
})

describe('countTextLines 宽度预算折行', () => {
  it('短文本与空文本恒 1 行', () => {
    expect(countTextLines('abcd', 100, SPEC)).toBe(1)
    expect(countTextLines('', 100, SPEC)).toBe(1)
    expect(countTextLines('abc', 0, SPEC)).toBe(1) // 非法预算交自校正兜底
  })

  it('长 ASCII 按 ceil(宽/预算) 折行', () => {
    // 25×10px=250px，预算 100 → 3 行
    expect(countTextLines('a'.repeat(25), 100, SPEC)).toBe(3)
    // 恰好整除不多估
    expect(countTextLines('a'.repeat(20), 100, SPEC)).toBe(2)
  })

  it('CJK 按宽字符宽度计', () => {
    // 10×20px=200px，预算 100 → 2 行
    expect(countTextLines('中'.repeat(10), 100, SPEC)).toBe(2)
  })

  it('零宽字符不计宽，astral 代理对按一个字符计', () => {
    // e + 组合符 = 1 个可见字符宽
    expect(countTextLines('e\u0301', 100, SPEC)).toBe(1)
    // 𝕏 (U+1D54F) 代理对：不是宽区间 → 一个普通宽度，不拆成两半
    expect(countTextLines('𝕏'.repeat(3), 100, SPEC)).toBe(1)
  })
})

describe('estimateRowLines 行级估算', () => {
  const base = { rowWidthPx: 1000, spec: SPEC }

  it('扣除前缀列（内边距/时间/级别/来源+间距）后按正文宽折行', () => {
    // prefix = 16(pad) + 120(ts)+8(gap) + 52(level)+8 + 80([Server])+8 = 292
    const lines = estimateRowLines({
      ...base,
      ts: '12:00:00.123',
      level: 'INFO',
      source: 'Server',
      body: 'x'.repeat(100),
      kind: 'log',
    })
    // bodyAvail = 1000-292 = 708，正文 1000px → 2 行
    expect(lines).toBe(2)
  })

  it('command 行有 > 前缀且无级别列；stack-frame 有缩进', () => {
    const command = estimateRowLines({
      ...base,
      body: 'a'.repeat(80),
      kind: 'command',
    })
    // prefix = 16 + 10(>)+8 = 34 → avail 966 → 800px → 1 行
    expect(command).toBe(1)

    const frame = estimateRowLines({
      ...base,
      body: 'a'.repeat(98),
      kind: 'stack-frame',
    })
    // prefix = 16 + 40(缩进) = 56 → avail 944 → 980px → 2 行
    expect(frame).toBe(2)
  })

  it('堆栈块头行的徽标+复制钮占宽计入', () => {
    const withDecor = estimateRowLines({ ...base, body: 'a'.repeat(92), blockDecorPx: 76 })
    // avail = 1000-16-76 = 908 → 920px → 2 行；无 decor 时 1 行
    const withoutDecor = estimateRowLines({ ...base, body: 'a'.repeat(92) })
    expect(withDecor).toBe(2)
    expect(withoutDecor).toBe(1)
  })

  it('容器宽不可用时恒 1 行（首帧 crossW=0 的退化路径）', () => {
    expect(estimateRowLines({ ...base, rowWidthPx: 0, body: 'a'.repeat(500) })).toBe(1)
  })
})

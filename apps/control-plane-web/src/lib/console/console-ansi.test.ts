import { describe, expect, it } from 'vitest'

import { ansiPlainText, ansiSliceFromPlain, parseAnsi, stripAnsi } from './console-ansi'

const ESC = '\u001b'
const BEL = '\u0007'

/** 
 * ANSI 解析（spec §1.3；ADR-086 代价 1）。
 *
 * 核心判据只有两条：SGR 要按 xterm 默认调色板着色；**其余转义一律吞掉、不留可见垃圾**。
 * 后者是 ADR-086 的验证项「构造未知转义序列不产生可见垃圾字符」。
 */
describe('parseAnsi', () => {
  it('无转义时单片段返回，且不含控制字符', () => {
    expect(parseAnsi('Done (12.345s)!')).toEqual([{ text: 'Done (12.345s)!' }])
  })

  it('SGR 前景色按 xterm 默认调色板着色，reset 后回到无样式', () => {
    const segments = parseAnsi(`plain${ESC}[32mgreen${ESC}[0mtail`)
    expect(segments).toEqual([
      { text: 'plain' },
      { text: 'green', color: '#4e9a06' },
      { text: 'tail' },
    ])
  })

  it('亮色 / 背景 / 粗体分别落到 color、background、bold', () => {
    expect(parseAnsi(`${ESC}[1;91;44mx`)).toEqual([
      { text: 'x', bold: true, color: '#ef2929', background: '#3465a4' },
    ])
  })

  it('空参数 SGR（ESC[m）等价于 reset', () => {
    expect(parseAnsi(`${ESC}[31ma${ESC}[mb`)).toEqual([{ text: 'a', color: '#cc0000' }, { text: 'b' }])
  })

  it('22 关粗体、39/49 恢复默认前景/背景，不影响其余属性', () => {
    expect(parseAnsi(`${ESC}[1;31;41ma${ESC}[22mb${ESC}[39mc${ESC}[49md`)).toEqual([
      { text: 'a', bold: true, color: '#cc0000', background: '#cc0000' },
      { text: 'b', color: '#cc0000', background: '#cc0000' },
      { text: 'c', background: '#cc0000' },
      { text: 'd' },
    ])
  })

  it('256 色 / 真彩的参数被整段吞掉，不被误读成其它 SGR 码', () => {
    // 若不跳过 38;5;<n> 的参数，5 会被当成「慢闪」、196 被忽略，且后续 1 会错位。
    expect(parseAnsi(`${ESC}[38;5;196mx`)).toEqual([{ text: 'x' }])
    expect(parseAnsi(`${ESC}[38;2;1;2;3;1mx`)).toEqual([{ text: 'x', bold: true }])
  })

  describe('未知转义一律吞掉不渲染', () => {
    const cases: Array<[string, string]> = [
      ['光标上移', `${ESC}[3Aabc`],
      ['清屏', `${ESC}[2Jabc`],
      ['清行', `${ESC}[Kabc`],
      ['光标定位', `${ESC}[10;20Habc`],
      ['隐藏光标（带中间字节）', `${ESC}[?25labc`],
      ['OSC 设标题（BEL 终止）', `${ESC}]0;title${BEL}abc`],
      ['OSC 设标题（ST 终止）', `${ESC}]0;title${ESC}\\abc`],
      ['DCS', `${ESC}Pdata${ESC}\\abc`],
      ['ESC 全复位', `${ESC}cabc`],
      ['字符集切换', `${ESC}(Babc`],
      ['被截断的 CSI（分包边界）', `abc${ESC}[0;3`],
      ['孤立 ESC 结尾', `abc${ESC}`],
    ]

    it.each(cases)('%s', (_name, input) => {
      const rendered = parseAnsi(input)
        .map((segment) => segment.text)
        .join('')
      expect(rendered).toBe('abc')
      // 更强的判据：渲染结果里不得残留 ESC / 括号参数等垃圾字符。
      expect(rendered).not.toContain(ESC)
      expect(rendered).not.toContain('[')
    })
  })

  it('回车与其它 C0 控制字符被吞掉，制表符保留（堆栈帧识别依赖它）', () => {
    expect(parseAnsi('a\rb\u0000c')).toEqual([{ text: 'abc' }])
    expect(parseAnsi('\tat Foo.bar(Foo.java:1)')).toEqual([{ text: '\tat Foo.bar(Foo.java:1)' }])
  })

  it('样式切换后未跟文本时不产出空片段', () => {
    expect(parseAnsi(`abc${ESC}[31m`)).toEqual([{ text: 'abc' }])
    expect(parseAnsi('')).toEqual([])
  })
})

describe('stripAnsi', () => {
  it('与 parseAnsi 的片段拼接结果一致（搜索命中下标据此与渲染对齐）', () => {
    const raw = `${ESC}[0;32m[09:12:13 INFO]:${ESC}[m ${ESC}[2Jdone\r`
    const joined = parseAnsi(raw)
      .map((segment) => segment.text)
      .join('')
    expect(stripAnsi(raw)).toBe(joined)
    expect(stripAnsi(raw)).toBe('[09:12:13 INFO]: done')
  })
})

describe('ansiPlainText / ansiSliceFromPlain', () => {
  it('按纯文本下标切原串，并把前缀里的 SGR 前置保留（正文不失色）', () => {
    const raw = `${ESC}[32m[09:12:13]${ESC}[33m body`
    const { plain, map } = ansiPlainText(raw)
    expect(plain).toBe('[09:12:13] body')
    // 切掉 '[09:12:13] '（11 字符）后，前缀里的两条 SGR 都要带到正文前面。
    expect(ansiSliceFromPlain(raw, map, 11)).toBe(`${ESC}[32m${ESC}[33mbody`)
  })

  it('切点为 0 时原串不变；切点越界时只剩前缀 SGR', () => {
    const raw = `${ESC}[32mabc`
    const { map } = ansiPlainText(raw)
    expect(ansiSliceFromPlain(raw, map, 0)).toBe(raw)
    expect(ansiSliceFromPlain(raw, map, 99)).toBe(`${ESC}[32m`)
  })
})

import { describe, expect, it } from 'vitest'

import {
  MC_COMMANDS,
  PLAYER_ARG_COMMANDS,
  TARGET_SELECTORS,
  applyCommonPrefix,
  applyCompletion,
  computeCompletion,
  longestCommonPrefix,
} from './console-completion'

/**
 * 命令补全（FR-416）：命令名 + 玩家参数两处，候选式、**不盲补第一个**。
 *
 * FR-415 把这套能力随旧 xterm 输入路径删掉了（ADR-086「已知回退」），此处是补回后的锁。
 */

const PLAYERS = ['Steve', 'Stevie', 'Alex']

describe('longestCommonPrefix', () => {
  it('空集与单元素退化正确', () => {
    expect(longestCommonPrefix([])).toBe('')
    expect(longestCommonPrefix(['solo'])).toBe('solo')
  })

  it('取共同前缀；无共同前缀为空', () => {
    expect(longestCommonPrefix(['save-all', 'save-off', 'save-on'])).toBe('save-')
    expect(longestCommonPrefix(['abc', 'xyz'])).toBe('')
  })
})

describe('computeCompletion 第一段（命令名）', () => {
  it('空输入给全部命令', () => {
    const state = computeCompletion('', 0, PLAYERS)
    expect(state?.kind).toBe('command')
    expect(state?.candidates).toEqual([...MC_COMMANDS])
  })

  it('按前缀过滤，大小写不敏感', () => {
    expect(computeCompletion('sav', 3, PLAYERS)?.candidates).toEqual(['save-all', 'save-off', 'save-on'])
    expect(computeCompletion('SAV', 3, PLAYERS)?.candidates).toEqual(['save-all', 'save-off', 'save-on'])
  })

  it('唯一命中即单候选（调用方据此可无歧义直接补）', () => {
    expect(computeCompletion('stopso', 6, PLAYERS)?.candidates).toEqual(['stopsound'])
  })

  it('commonSuffix 只到公共前缀，不含任何候选的独有部分', () => {
    const state = computeCompletion('sa', 2, PLAYERS)!
    // 三个 save-* 加上 say，公共前缀是 'sa' 本身 → 无可推进。
    expect(state.candidates).toEqual(['save-all', 'save-off', 'save-on', 'say'])
    expect(state.commonSuffix).toBe('')

    const saveState = computeCompletion('save', 4, PLAYERS)!
    expect(saveState.commonSuffix).toBe('-')
  })

  it('无匹配前缀返回 null（不给「随便找一个」的候选）', () => {
    expect(computeCompletion('zzzz', 4, PLAYERS)).toBeNull()
  })
})

describe('computeCompletion 第二段（玩家名 / 选择器）', () => {
  it('玩家参数命令的第二段给在线玩家名', () => {
    const state = computeCompletion('op ', 3, PLAYERS)
    expect(state?.kind).toBe('player')
    expect(state?.candidates).toEqual(PLAYERS)
  })

  it('按前缀过滤玩家名，且保留玩家名自身大小写', () => {
    const state = computeCompletion('op st', 5, PLAYERS)
    expect(state?.candidates).toEqual(['Steve', 'Stevie'])
    // LCP('Steve','Stevie') = 'Stev'，减去已敲的 2 个字符 → ghost 只有 'ev'。
    // 若这里是 'eve'，说明代码把第一个候选整个当成了公共前缀，即盲补。
    expect(state?.commonPrefix).toBe('Stev')
    expect(state?.commonSuffix).toBe('ev')
  })

  it('无在线玩家时不给候选（宁可不补，也不编造玩家名）', () => {
    expect(computeCompletion('op ', 3, [])).toBeNull()
  })

  it('已敲 @ 才给选择器；未敲 @ 时选择器不淹没玩家名', () => {
    expect(computeCompletion('tp @', 4, PLAYERS)?.candidates).toEqual([...TARGET_SELECTORS])
    expect(computeCompletion('tp ', 3, PLAYERS)?.candidates).toEqual(PLAYERS)
  })

  it('非玩家参数命令的第二段不补', () => {
    expect(PLAYER_ARG_COMMANDS.has('say')).toBe(false)
    expect(computeCompletion('say he', 6, PLAYERS)).toBeNull()
  })

  it('第三段起不补（坐标/物品 id 的候选集依赖服务端状态，猜错比不给更糟）', () => {
    expect(computeCompletion('tp Steve 10', 11, PLAYERS)).toBeNull()
  })

  it('命令名大小写不敏感地识别玩家参数命令', () => {
    expect(computeCompletion('OP ', 3, PLAYERS)?.kind).toBe('player')
  })
})

describe('computeCompletion token 边界', () => {
  it('token 从光标向左吃到空白为止，末尾之后的文本不参与', () => {
    const state = computeCompletion('op ste', 6, PLAYERS)!
    expect(state.token).toBe('ste')
    expect(state.tokenStart).toBe(3)
    expect(state.tokenEnd).toBe(6)
  })

  it('光标在词中间：只按光标之前的部分匹配', () => {
    // 'op stXXX' 光标在 'st' 之后 → token 为 'st'
    const state = computeCompletion('op stXXX', 5, PLAYERS)!
    expect(state.token).toBe('st')
    expect(state.candidates).toEqual(['Steve', 'Stevie'])
  })
})

describe('applyCompletion / applyCommonPrefix', () => {
  it('applyCompletion 替换 token 并补一个空格', () => {
    const state = computeCompletion('op st', 5, PLAYERS)!
    expect(applyCompletion('op st', state, 'Steve')).toEqual({ value: 'op Steve ', caret: 9 })
  })

  it('后面已有空白时不补出双空格', () => {
    const state = computeCompletion('op st extra', 5, PLAYERS)!
    expect(applyCompletion('op st extra', state, 'Steve').value).toBe('op Steve extra')
  })

  it('applyCommonPrefix 只推进公共前缀；无可推进时返回 null', () => {
    const advanceable = computeCompletion('save', 4, PLAYERS)!
    expect(applyCommonPrefix('save', advanceable)).toEqual({ value: 'save-', caret: 5 })

    const stuck = computeCompletion('sa', 2, PLAYERS)!
    expect(applyCommonPrefix('sa', stuck)).toBeNull()
  })

  it('推进后的值仍是所有候选的前缀——即「不盲补」的不变式', () => {
    const state = computeCompletion('op st', 5, PLAYERS)!
    const advanced = applyCommonPrefix('op st', state)!
    // 停在公共前缀 'Stev'，**不是** 'Steve'——多一个 e 就等于替用户选了 Steve 而非 Stevie。
    // 同时大小写被纠正成候选自身的（MC 玩家名大小写敏感，'stev' 服务端不认）。
    expect(advanced.value).toBe('op Stev')
    // 关键：推进结果对每一个候选都成立，故未做任何选择。
    const typedToken = advanced.value.slice(state.tokenStart)
    for (const candidate of state.candidates) {
      expect(candidate.toLocaleLowerCase().startsWith(typedToken.toLocaleLowerCase())).toBe(true)
    }
  })
})

describe('命令表是协议事实的完整性抽查', () => {
  it('收录 vanilla 与 Paper 的常用控制台命令', () => {
    for (const command of ['stop', 'list', 'op', 'whitelist', 'gamemode', 'tps', 'timings', 'plugins', 'version']) {
      expect(MC_COMMANDS).toContain(command)
    }
  })

  it('玩家参数命令集是命令表的子集（不引用不存在的命令）', () => {
    for (const command of PLAYER_ARG_COMMANDS) {
      expect(MC_COMMANDS).toContain(command)
    }
  })
})

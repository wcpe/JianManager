import { describe, it, expect } from 'vitest'
import {
  extractVariables,
  fillTemplate,
  validateVariableValues,
  deriveMarketMeta,
  parseMaxHeapMb,
  formatRamRequirement,
} from './template-apply'

describe('extractVariables', () => {
  it('提取 {{var}} 占位变量并去重保序', () => {
    expect(extractVariables('java -Xmx{{ram}}G -jar {{jar}} --name {{name}}')).toEqual([
      'ram',
      'jar',
      'name',
    ])
  })

  it('同名变量只出现一次', () => {
    expect(extractVariables('{{port}} {{port}} {{host}}')).toEqual(['port', 'host'])
  })

  it('容忍占位内多余空白', () => {
    expect(extractVariables('a {{  foo  }} b {{bar}}')).toEqual(['foo', 'bar'])
  })

  it('无占位返回空数组', () => {
    expect(extractVariables('java -jar server.jar nogui')).toEqual([])
  })

  it('空串/空白返回空数组', () => {
    expect(extractVariables('')).toEqual([])
    expect(extractVariables('   ')).toEqual([])
  })

  it('忽略非法变量名（含空格/特殊符号的占位不算变量）', () => {
    // 仅匹配 [A-Za-z0-9_] 组成的变量名
    expect(extractVariables('{{a-b}} {{good}}')).toEqual(['good'])
  })
})

describe('fillTemplate', () => {
  it('用给定值替换占位', () => {
    expect(fillTemplate('java -Xmx{{ram}}G -jar {{jar}}', { ram: '4', jar: 'paper.jar' })).toBe(
      'java -Xmx4G -jar paper.jar',
    )
  })

  it('同名占位全部替换', () => {
    expect(fillTemplate('{{x}}-{{x}}', { x: 'v' })).toBe('v-v')
  })

  it('容忍占位内空白', () => {
    expect(fillTemplate('hi {{ who }}', { who: 'bob' })).toBe('hi bob')
  })

  it('未提供值的占位保持原样（预览未填态）', () => {
    expect(fillTemplate('{{a}} {{b}}', { a: '1' })).toBe('1 {{b}}')
  })

  it('空值视为已填，替换为空串', () => {
    expect(fillTemplate('x{{a}}y', { a: '' })).toBe('xy')
  })

  it('无占位原样返回', () => {
    expect(fillTemplate('plain', {})).toBe('plain')
  })
})

describe('validateVariableValues', () => {
  it('全部已填则无错误', () => {
    expect(validateVariableValues(['a', 'b'], { a: '1', b: '2' })).toEqual({})
  })

  it('缺失变量报必填 messageKey', () => {
    const errs = validateVariableValues(['a', 'b'], { a: '1' })
    expect(errs.b).toBe('templates.market.variableRequired')
    expect(errs.a).toBeUndefined()
  })

  it('纯空白视为未填', () => {
    const errs = validateVariableValues(['a'], { a: '   ' })
    expect(errs.a).toBe('templates.market.variableRequired')
  })

  it('无变量返回空错误集', () => {
    expect(validateVariableValues([], {})).toEqual({})
  })
})

describe('parseMaxHeapMb', () => {
  it('解析 -Xmx4G 为 4096 MB', () => {
    expect(parseMaxHeapMb('java -Xmx4G -jar paper.jar')).toBe(4096)
  })

  it('解析 -Xmx2048M 为 2048 MB', () => {
    expect(parseMaxHeapMb('java -Xmx2048M -jar s.jar')).toBe(2048)
  })

  it('解析小写 -Xmx512m', () => {
    expect(parseMaxHeapMb('java -Xmx512m -jar s.jar')).toBe(512)
  })

  it('解析 -Xmx1024k 向上取整为 1 MB', () => {
    expect(parseMaxHeapMb('java -Xmx1024K -jar s.jar')).toBe(1)
  })

  it('无 -Xmx 返回 null', () => {
    expect(parseMaxHeapMb('java -jar paper.jar nogui')).toBeNull()
  })
})

describe('formatRamRequirement', () => {
  it('优先用 startCommand 中的 -Xmx 推断（向上取整到 GB + “+”）', () => {
    expect(formatRamRequirement({ startCommand: 'java -Xmx4G -jar s.jar', type: 'minecraft_java' })).toBe('4G+')
  })

  it('-Xmx1536M 向上取整为 2G+', () => {
    expect(formatRamRequirement({ startCommand: 'java -Xmx1536M -jar s.jar', type: 'minecraft_java' })).toBe('2G+')
  })

  it('无 -Xmx 时按类型给默认建议', () => {
    expect(formatRamRequirement({ startCommand: 'forge run', type: 'forge' })).toBe('6G+')
    expect(formatRamRequirement({ startCommand: 'velocity', type: 'velocity' })).toBe('1G+')
    expect(formatRamRequirement({ startCommand: 'x', type: 'minecraft_java' })).toBe('2G+')
  })

  it('完全无信息（generic 且无 Xmx）返回 null', () => {
    expect(formatRamRequirement({ startCommand: 'do something', type: 'generic' })).toBeNull()
  })
})

describe('deriveMarketMeta', () => {
  it('Paper 类（mc java）→ 立方图标 + 主色调 + Java 21 建议', () => {
    const m = deriveMarketMeta({
      name: 'Paper 1.21',
      type: 'minecraft_java',
      startCommand: 'java -Xmx4G -jar paper-1.21.jar nogui',
      description: '',
    })
    expect(m.tone).toBe('primary')
    expect(m.icon).toBe('cube')
    expect(m.java).toBe('Java 21')
    expect(m.ram).toBe('4G+')
    expect(m.typeLabel).toBe('Minecraft Java')
  })

  it('velocity → 代理图标 + info 调 + Java 17', () => {
    const m = deriveMarketMeta({ name: 'proxy', type: 'velocity', startCommand: 'java -jar velocity.jar', description: '' })
    expect(m.icon).toBe('route')
    expect(m.tone).toBe('info')
    expect(m.java).toBe('Java 17')
    expect(m.ram).toBe('1G+')
  })

  it('forge → 模组图标 + warning 调 + Java 17 + 6G+', () => {
    const m = deriveMarketMeta({ name: 'modded', type: 'forge', startCommand: 'run.sh', description: '' })
    expect(m.icon).toBe('flame')
    expect(m.tone).toBe('warning')
    expect(m.ram).toBe('6G+')
  })

  it('未知类型回退 generic（包装图标 + neutral 文案用原 type）', () => {
    const m = deriveMarketMeta({ name: 'x', type: 'custom_thing', startCommand: 'cmd', description: '' })
    expect(m.icon).toBe('package')
    expect(m.tone).toBe('primary')
    expect(m.typeLabel).toBe('custom_thing')
    expect(m.java).toBeNull()
    expect(m.ram).toBeNull()
  })

  it('类型识别大小写不敏感且容忍连字符（Minecraft-Java）', () => {
    const m = deriveMarketMeta({ name: 'x', type: 'Minecraft-Java', startCommand: 'java -jar s.jar', description: '' })
    expect(m.icon).toBe('cube')
    expect(m.typeLabel).toBe('Minecraft Java')
  })
})

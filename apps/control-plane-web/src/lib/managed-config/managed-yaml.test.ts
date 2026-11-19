import { describe, expect, it } from 'vitest'

import type { ManagedKeyMeta } from '@/api/managedConfig'
import {
  parseManagedYaml,
  stringifyManagedYaml,
  validateManagedYaml,
} from '@/lib/managed-config/managed-yaml'

/**
 * YAML 编解码测试。
 *
 * 盯住的是四处「写错了会让用户丢配置」的行为：
 * 1. 注释必须来自目录（键名/说明/档位/默认值），否则等于手写了一堆会过期的文案；
 * 2. 目录为空时**不能**把所有键丢掉（catalog 未到达的那一帧曾经把配置清空）；
 * 3. 目录未覆盖的键必须原样保留（静默丢键比不加注释危险得多）；
 * 4. 重复键必须报错（YAML 会静默取后者，用户改了半天只有一处生效）。
 */

function metaOf(overrides: Partial<ManagedKeyMeta> & Pick<ManagedKeyMeta, 'key'>): ManagedKeyMeta {
  return {
    kind: 'int',
    group: 'collect',
    tier: 'safe',
    title: '采集周期',
    description: '多久采集一次指标。',
    ...overrides,
  }
}

describe('stringifyManagedYaml', () => {
  it('按目录元数据生成注释：中文名、档位、说明、默认值都在', () => {
    const items = [
      metaOf({ key: 'collect-period-ticks', title: '指标采集周期', tier: 'guarded', default: 20 }),
    ]
    const text = stringifyManagedYaml({ 'collect-period-ticks': 10 }, items)

    expect(text).toContain('# 指标采集周期（危险档）')
    expect(text).toContain('# 多久采集一次指标。')
    expect(text).toContain('# 默认 20')
    // 数字保持裸字面量；只有字符串才加引号（否则 YAML 会把数值读成字符串）。
    expect(text).toContain('collect-period-ticks: 10')
  })

  it('目录为空时仍输出配置内容，不能把键丢掉', () => {
    // 这是 catalog 尚未到达的那一帧：编辑器必须显示真实内容，而不是空串。
    const text = stringifyManagedYaml({ 'collect-period-ticks': 10 }, [])
    expect(text).toContain('collect-period-ticks')
    expect(text).toContain('10')
  })

  it('目录未覆盖的键原样保留并标注来源不明', () => {
    const items = [metaOf({ key: 'collect-period-ticks' })]
    const text = stringifyManagedYaml({ 'collect-period-ticks': 10, 'future-key': 'x' }, items)

    expect(text).toContain('future-key')
    expect(text).toContain('不在权威目录中')
  })

  it('嵌套对象与标量数组按 YAML 块输出', () => {
    const items = [metaOf({ key: 'alert.rules', kind: 'object', title: '告警规则' })]
    // doc 是**嵌套**对象（点分键是编辑器内部口径，落库与下发都是嵌套形态）。
    const text = stringifyManagedYaml({ alert: { rules: { 'tps-warn': { threshold: 18 } } } }, items)

    // 多段键必须展开成父级层级：`alert:` → `rules:` → `tps-warn:` → 叶子。
    expect(text).toContain('alert:')
    expect(text).toContain('  rules:')
    expect(text).toContain('    tps-warn:')
    expect(text).toContain('threshold: 18')
  })

  it('字符串一律加引号，避免 on/yes/1s 被 YAML 隐式转型', () => {
    const items = [metaOf({ key: 'level', kind: 'string' })]
    const text = stringifyManagedYaml({ level: 'on' }, items)
    expect(text).toContain('level: "on"')
  })
})

describe('parseManagedYaml / validateManagedYaml', () => {
  it('空文本视为清空该层（合法意图，不是错误）', () => {
    const result = parseManagedYaml('   ')
    expect(result.error).toBe('')
    expect(result.value).toEqual({})
  })

  it('全是注释的文档等价于空配置', () => {
    const result = parseManagedYaml('# 只有注释\n# 没有内容\n')
    expect(result.error).toBe('')
    expect(result.value).toEqual({})
  })

  it('JSON 文本也能解析（YAML 是 JSON 超集，历史数据是 JSON）', () => {
    const result = parseManagedYaml('{"collect-period-ticks":10}')
    expect(result.error).toBe('')
    expect(result.value).toEqual({ 'collect-period-ticks': 10 })
  })

  it('语法错误带行号，供界面跳到出错位置', () => {
    // 缩进错乱：第二行多缩进但没有父键。
    const result = validateManagedYaml('collect:\n    - a\n  b: 1\n')
    expect(result.error).not.toBe('')
    expect(result.line).toBeGreaterThanOrEqual(2)
  })

  it('顶层是列表时报错，而不是悄悄当成配置', () => {
    const result = parseManagedYaml('- a\n- b\n')
    expect(result.error).toContain('映射')
  })

  it('同一层级重复键必须报错（YAML 默认静默取后者）', () => {
    const result = validateManagedYaml('collect: 1\ncollect: 2\n')
    expect(result.error).toContain('重复键')
  })
})

describe('往返保真', () => {
  it('序列化 → 解析后值不变', () => {
    const items = [
      metaOf({ key: 'collect-period-ticks' }),
      metaOf({ key: 'alert.rules', kind: 'object', title: '告警规则' }),
      metaOf({ key: 'worlds', kind: 'stringList', title: '世界白名单' }),
    ]
    const doc = {
      'collect-period-ticks': 40,
      'alert.rules': { 'tps-warn': { threshold: 18, enabled: true } },
      worlds: ['world', 'world_nether'],
    }

    const text = stringifyManagedYaml(doc, items)
    const back = parseManagedYaml(text)
    expect(back.error).toBe('')
    expect(back.value).toEqual(doc)
  })
})

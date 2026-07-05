import { describe, it, expect } from 'vitest'

import { buildNodeOptions, initialWizardNodeId } from './instance-wizard-options'

const labels = { online: '在线', offline: '离线', starting: '启动中', maintenance: '维护中' }

describe('buildNodeOptions（FIX-8 创建实例节点下拉）', () => {
  it('列出全部节点，离线/启动中节点不再被过滤掉', () => {
    const opts = buildNodeOptions(
      [
        { id: 1, name: 'n1', status: 1 },
        { id: 2, name: 'n2', status: 0 },
        { id: 3, name: 'n3', status: 2 },
      ],
      labels,
    )
    expect(opts).toHaveLength(3)
    expect(opts.map((o) => o.value)).toEqual(['1', '2', '3'])
  })

  it('标签标注状态与维护态', () => {
    const opts = buildNodeOptions(
      [
        { id: 1, name: 'on', status: 1 },
        { id: 2, name: 'off', status: 0 },
        { id: 3, name: 'starting', status: 2 },
        { id: 4, name: 'cordon', status: 1, maintenance: true },
      ],
      labels,
    )
    expect(opts[0].label).toContain('在线')
    expect(opts[1].label).toContain('离线')
    expect(opts[2].label).toContain('启动中')
    expect(opts[3].label).toContain('维护中')
  })

  it('无节点返回空数组', () => {
    expect(buildNodeOptions(undefined, labels)).toHaveLength(0)
    expect(buildNodeOptions([], labels)).toHaveLength(0)
  })
})

describe('initialWizardNodeId（FR-268 节点作用域联动）', () => {
  it('URL node 参数优先于页眉节点作用域', () => {
    expect(initialWizardNodeId('1', 2)).toBe('1')
  })

  it('无 URL node 参数时使用页眉节点作用域', () => {
    expect(initialWizardNodeId(null, 2)).toBe('2')
  })

  it('无 URL node 参数且全部节点作用域时保持空值', () => {
    expect(initialWizardNodeId(null, null)).toBe('')
  })
})

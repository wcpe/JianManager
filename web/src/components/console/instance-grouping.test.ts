import { describe, it, expect } from 'vitest'
import {
  envOf,
  freeTagsOf,
  collectEnvs,
  collectTags,
  groupInstances,
} from './instance-grouping'
import type { InstanceInfo } from '@/api/instances'

function inst(id: number, nodeId: number, tags: string[] | null, status = 'RUNNING'): InstanceInfo {
  return {
    id,
    uuid: `u-${id}`,
    nodeId,
    name: `inst-${id}`,
    type: 'mc',
    role: 'backend',
    processType: 'daemon',
    status,
    startCommand: '',
    workDir: '',
    serverPort: 0,
    autoStart: false,
    autoRestart: false,
    tags,
    createdAt: '',
  }
}

describe('envOf / freeTagsOf', () => {
  it('提取首个 env 标签', () => {
    expect(envOf(inst(1, 1, ['survival', 'env:prod']))).toBe('prod')
    expect(envOf(inst(1, 1, ['env:dev', 'env:prod']))).toBe('dev')
  })
  it('无 env 标签返回空串', () => {
    expect(envOf(inst(1, 1, ['survival']))).toBe('')
    expect(envOf(inst(1, 1, null))).toBe('')
  })
  it('自由标签剔除 env: 前缀', () => {
    expect(freeTagsOf(inst(1, 1, ['env:prod', 'survival', 'eu']))).toEqual(['survival', 'eu'])
    expect(freeTagsOf(inst(1, 1, null))).toEqual([])
  })
})

describe('collectEnvs / collectTags', () => {
  const list = [
    inst(1, 1, ['env:prod', 'survival']),
    inst(2, 1, ['env:dev', 'survival']),
    inst(3, 2, ['env:prod', 'lobby']),
    inst(4, 2, null),
  ]
  it('收集去重排序的环境', () => {
    expect(collectEnvs(list)).toEqual(['dev', 'prod'])
  })
  it('收集去重排序的自由标签（不含 env）', () => {
    expect(collectTags(list)).toEqual(['lobby', 'survival'])
  })
})

describe('groupInstances', () => {
  const list = [
    inst(1, 1, ['env:prod'], 'RUNNING'),
    inst(2, 2, ['env:dev'], 'STOPPED'),
    inst(3, 1, null, 'RUNNING'),
  ]

  it('none 维度返回单一分组', () => {
    const g = groupInstances(list, 'none')
    expect(g).toHaveLength(1)
    expect(g[0].instances).toHaveLength(3)
  })

  it('按节点分组', () => {
    const g = groupInstances(list, 'node')
    expect(g.map((x) => x.key)).toEqual(['1', '2'])
    expect(g[0].instances.map((i) => i.id)).toEqual([1, 3])
  })

  it('按环境分组，未分环境排末尾', () => {
    const g = groupInstances(list, 'env')
    // dev / prod 字典序在前，空 key（未分环境）恒末尾
    expect(g.map((x) => x.key)).toEqual(['dev', 'prod', ''])
  })

  it('按状态分组', () => {
    const g = groupInstances(list, 'status')
    expect(g.map((x) => x.key)).toEqual(['RUNNING', 'STOPPED'])
  })
})

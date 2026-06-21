import { describe, it, expect } from 'vitest'
import {
  groupInstancesByNode,
  statusDotKind,
  treeBranchKey,
  toTreeBranches,
} from './instance-tree'
import type { InstanceInfo } from '@/api/instances'
import type { NodeInfo } from '@/api/nodes'

function inst(id: number, nodeId: number, status = 'RUNNING'): InstanceInfo {
  return {
    id,
    uuid: `u-${id}`,
    nodeId,
    name: `inst-${id}`,
    type: 'mc',
    processType: 'daemon',
    status,
    startCommand: '',
    workDir: '',
    autoStart: false,
    autoRestart: false,
    tags: null,
    createdAt: '',
  }
}

function node(id: number, name: string): NodeInfo {
  return {
    id,
    uuid: `nu-${id}`,
    name,
    host: '127.0.0.1',
    grpcPort: 0,
    wsPort: 0,
    status: 1,
    os: 'linux',
    arch: 'amd64',
    cpuCores: 0,
    memoryMb: 0,
    diskTotalMb: 0,
    cpuUsage: 0,
    memoryUsage: 0,
    diskUsage: 0,
    networkBytesSent: 0,
    networkBytesRecv: 0,
    lastHeartbeat: null,
    createdAt: '',
  }
}

describe('groupInstancesByNode', () => {
  it('groups instances under their node, preserving node order', () => {
    const nodes = [node(1, 'node-a'), node(2, 'node-b')]
    const instances = [inst(10, 2), inst(11, 1), inst(12, 1)]

    const groups = groupInstancesByNode(instances, nodes)

    expect(groups.map((g) => g.nodeId)).toEqual([1, 2])
    expect(groups[0].nodeName).toBe('node-a')
    expect(groups[0].instances.map((i) => i.id)).toEqual([11, 12])
    expect(groups[1].instances.map((i) => i.id)).toEqual([10])
  })

  it('keeps nodes with no instances as empty groups', () => {
    const nodes = [node(1, 'node-a'), node(2, 'node-b')]
    const groups = groupInstancesByNode([inst(10, 1)], nodes)

    expect(groups).toHaveLength(2)
    expect(groups[1].instances).toEqual([])
  })

  it('collects orphan instances (unknown node) into a trailing group', () => {
    const nodes = [node(1, 'node-a')]
    const groups = groupInstancesByNode([inst(10, 1), inst(11, 99)], nodes)

    expect(groups).toHaveLength(2)
    const orphan = groups[groups.length - 1]
    expect(orphan.nodeId).toBe(-1)
    expect(orphan.nodeName).toBeUndefined()
    expect(orphan.instances.map((i) => i.id)).toEqual([11])
  })

  it('returns empty array when there are no nodes and no instances', () => {
    expect(groupInstancesByNode([], [])).toEqual([])
  })
})

describe('treeBranchKey', () => {
  it('namespaces by dim so tree keys never collide with nav-group keys', () => {
    // 导航组 key 形如 'instances'/'monitor'，树分支恒以 tree: 前缀隔离
    expect(treeBranchKey('node', '1')).toBe('tree:node:1')
    expect(treeBranchKey('status', 'RUNNING')).toBe('tree:status:RUNNING')
    expect(treeBranchKey('env', 'prod')).toBe('tree:env:prod')
  })

  it('maps the empty (ungrouped/orphan) key to a stable __none__ placeholder', () => {
    expect(treeBranchKey('env', '')).toBe('tree:env:__none__')
    expect(treeBranchKey('node', '')).toBe('tree:node:__none__')
  })

  it('produces distinct keys for the same group value under different dims', () => {
    // 同一原始值 '1' 在不同维度下折叠态互不影响
    expect(treeBranchKey('node', '1')).not.toBe(treeBranchKey('status', '1'))
  })

  it('is stable for the same (dim, key) pair across calls', () => {
    expect(treeBranchKey('node', '2')).toBe(treeBranchKey('node', '2'))
  })
})

describe('toTreeBranches', () => {
  it('attaches a branchKey to each group without reordering', () => {
    const groups = [
      { key: '2', instances: [inst(10, 2)] },
      { key: '1', instances: [inst(11, 1), inst(12, 1)] },
    ]
    const branches = toTreeBranches('node', groups)

    expect(branches.map((b) => b.key)).toEqual(['2', '1'])
    expect(branches.map((b) => b.branchKey)).toEqual(['tree:node:2', 'tree:node:1'])
    expect(branches[1].instances.map((i) => i.id)).toEqual([11, 12])
  })

  it('keeps the original instance arrays by reference (no copy)', () => {
    const members = [inst(10, 1)]
    const branches = toTreeBranches('status', [{ key: 'RUNNING', instances: members }])
    expect(branches[0].instances).toBe(members)
  })

  it('handles an empty group key via the __none__ placeholder', () => {
    const branches = toTreeBranches('env', [{ key: '', instances: [inst(10, 1)] }])
    expect(branches[0].branchKey).toBe('tree:env:__none__')
  })
})

describe('statusDotKind', () => {
  it('maps statuses to the right visual kind', () => {
    expect(statusDotKind('RUNNING')).toBe('running')
    expect(statusDotKind('STARTING')).toBe('transitioning')
    expect(statusDotKind('STOPPING')).toBe('transitioning')
    expect(statusDotKind('CRASHED')).toBe('crashed')
    expect(statusDotKind('STOPPED')).toBe('stopped')
    expect(statusDotKind('UNKNOWN')).toBe('stopped')
  })
})

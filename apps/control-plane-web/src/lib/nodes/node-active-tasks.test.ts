import { describe, expect, it } from 'vitest'
import {
  activeInGroup,
  activeSameKind,
  activeTaskLabel,
  canForceStop,
  exclusiveGroupOf,
} from './node-active-tasks'
import type { Task } from '@/lib/tasks/task-status'

function task(over: Partial<Task> & Pick<Task, 'kind'>): Task {
  return {
    id: 1,
    taskId: 't-1',
    nodeId: 1,
    state: 'running',
    progress: 40,
    title: '任务',
    detail: '',
    error: '',
    result: '',
    cancelRequested: false,
    createdBy: 1,
    createdAt: '',
    updatedAt: '',
    ...over,
  }
}

describe('exclusiveGroupOf', () => {
  it('JDK 与 Node.js 同组（都写节点级托管运行时目录）', () => {
    expect(exclusiveGroupOf('jdk_install')).toBe('runtime_install')
    expect(exclusiveGroupOf('runtime_install')).toBe('runtime_install')
  })

  it('全局包独立成组（写 global 子目录）', () => {
    expect(exclusiveGroupOf('pkg_install')).toBe('pkg_install')
  })

  it('备份/快照等不参与互斥', () => {
    expect(exclusiveGroupOf('backup_create')).toBe('')
    expect(exclusiveGroupOf('snapshot_create')).toBe('')
  })
})

describe('activeInGroup', () => {
  it('JDK 在跑时，Node.js 的提交也看到占用（同组）', () => {
    const tasks = [task({ kind: 'jdk_install' })]
    expect(activeInGroup(tasks, exclusiveGroupOf('runtime_install'))?.kind).toBe('jdk_install')
  })

  it('全局包在跑时，JDK 不受影响', () => {
    const tasks = [task({ kind: 'pkg_install' })]
    expect(activeInGroup(tasks, exclusiveGroupOf('jdk_install'))).toBeNull()
  })

  it('空组永远不占用', () => {
    const tasks = [task({ kind: 'jdk_install' })]
    expect(activeInGroup(tasks, '')).toBeNull()
  })
})

describe('activeSameKind', () => {
  it('只认同 kind：JDK 在跑不算 Node.js 的"相同任务"', () => {
    const tasks = [task({ kind: 'jdk_install' })]
    expect(activeSameKind(tasks, 'runtime_install')).toBeNull()
    expect(activeSameKind(tasks, 'jdk_install')?.kind).toBe('jdk_install')
  })
})

describe('activeTaskLabel', () => {
  it('取消中优先于百分比', () => {
    expect(activeTaskLabel(task({ kind: 'jdk_install', cancelRequested: true, progress: 80 }))).toBe('取消中…')
  })

  it('pending 显示排队中而不是 0%', () => {
    expect(activeTaskLabel(task({ kind: 'jdk_install', state: 'pending', progress: 0 }))).toBe('排队中')
  })

  it('running 显示百分比', () => {
    expect(activeTaskLabel(task({ kind: 'jdk_install', progress: 63 }))).toBe('63%')
  })
})

describe('canForceStop', () => {
  it('pending 与 running 都能停', () => {
    expect(canForceStop(task({ kind: 'jdk_install', state: 'pending' }))).toBe(true)
    expect(canForceStop(task({ kind: 'jdk_install', state: 'running' }))).toBe(true)
  })

  it('已在取消中的任务不能再点（否则会重复下发取消）', () => {
    expect(canForceStop(task({ kind: 'jdk_install', cancelRequested: true }))).toBe(false)
  })

  it('终态任务不可停', () => {
    expect(canForceStop(task({ kind: 'jdk_install', state: 'succeeded' }))).toBe(false)
    expect(canForceStop(task({ kind: 'jdk_install', state: 'failed' }))).toBe(false)
    expect(canForceStop(task({ kind: 'jdk_install', state: 'canceled' }))).toBe(false)
  })
})

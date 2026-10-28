import { describe, expect, it, vi } from 'vitest'

import { buildContextMenuActions } from '@/components/views/instances/InstanceContextMenu'
import type { InstanceInfo } from '@/api/instances'

/**
 * 右键菜单的动作集合（用户提出的需求）。
 *
 * 只测纯函数 `buildContextMenuActions`：它决定「哪些动作出现、哪些禁用、以什么顺序排」，
 * 是这块功能唯一的判断逻辑。菜单本身的定位/Esc 关闭由页面级用例覆盖。
 */
const t = (key: string, fallback: string) => fallback

/** 造一个最小实例（只带菜单判据用到的字段）。 */
function makeInstance(over: Partial<InstanceInfo> = {}): InstanceInfo {
  return {
    id: 1,
    uuid: 'u-1',
    name: 'survival-1',
    status: 'STOPPED',
    processType: 'direct',
    role: 'backend',
    nodeId: 1,
    tags: [],
    ...over,
  } as InstanceInfo
}

function actionsFor(inst: InstanceInfo) {
  return buildContextMenuActions({
    inst,
    t,
    onStart: vi.fn(),
    onStop: vi.fn(),
    onRestart: vi.fn(),
    onEditConfig: vi.fn(),
    onTags: vi.fn(),
    onLimits: vi.fn(),
    onProxy: vi.fn(),
    onClone: vi.fn(),
    onDelete: vi.fn(),
  })
}

const keys = (inst: InstanceInfo) => actionsFor(inst).map((a) => a.key)

describe('右键菜单动作集合', () => {
  it('停止态：只给「启动」，不给「停止 / 重启」', () => {
    const k = keys(makeInstance({ status: 'STOPPED' }))
    expect(k).toContain('start')
    expect(k).not.toContain('stop')
    expect(k).not.toContain('restart')
  })

  it('运行态：给「停止」与「重启」，不给「启动」', () => {
    const k = keys(makeInstance({ status: 'RUNNING' }))
    expect(k).toContain('stop')
    expect(k).toContain('restart')
    expect(k).not.toContain('start')
  })

  it('删除恒在末尾且标红（破坏性动作不与常用动作混排）', () => {
    const actions = actionsFor(makeInstance())
    const last = actions[actions.length - 1]
    expect(last.key).toBe('delete')
    expect(last.danger).toBe(true)
  })

  it('所有非删除动作都排在删除之前', () => {
    const actions = actionsFor(makeInstance({ status: 'RUNNING' }))
    const deleteIndex = actions.findIndex((a) => a.key === 'delete')
    for (const action of actions) {
      if (action.key === 'delete') continue
      expect(action.danger).toBeFalsy()
    }
    expect(deleteIndex).toBe(actions.length - 1)
  })

  it('运行中的实例：克隆入口保留但禁用并给出原因（不是藏起来）', () => {
    const clone = actionsFor(makeInstance({ status: 'RUNNING' })).find((a) => a.key === 'clone')
    expect(clone).toBeDefined()
    expect(clone?.disabled).toBe(true)
    expect(clone?.disabledHint).toBeTruthy()
  })

  it('已停止的实例：克隆可用且无禁用提示', () => {
    const clone = actionsFor(makeInstance({ status: 'STOPPED' })).find((a) => a.key === 'clone')
    expect(clone?.disabled).toBeFalsy()
    expect(clone?.disabledHint).toBeUndefined()
  })

  it('资源限额只对 docker 进程给入口', () => {
    expect(keys(makeInstance({ processType: 'docker' }))).toContain('limits')
    expect(keys(makeInstance({ processType: 'direct' }))).not.toContain('limits')
  })

  it('管理后端只对代理角色给入口', () => {
    expect(keys(makeInstance({ role: 'proxy' }))).toContain('proxy')
    expect(keys(makeInstance({ role: 'backend' }))).not.toContain('proxy')
  })

  it('每个动作都有 key 与文案（渲染层不用再做兜底）', () => {
    for (const action of actionsFor(makeInstance({ processType: 'docker', role: 'proxy' }))) {
      expect(action.key).toBeTruthy()
      expect(action.label).toBeTruthy()
    }
  })
})

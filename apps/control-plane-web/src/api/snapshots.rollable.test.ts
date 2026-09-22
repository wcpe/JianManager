import { describe, expect, it } from 'vitest'

import { isSnapshotRollable, type InstanceSnapshot } from './snapshots'

/**
 * B-1 前端契约：快照「可回滚」判定必须同时看状态与底链可用性。
 *
 * 后端缺陷现场：快照的底层备份被保留策略裁掉后，快照行的 state 仍是 completed，
 * 于是列表显示「可回滚」，点下去才报 record not found。修复后后端在列表/详情里回填
 * notRollableReason，前端据此禁用按钮并展示原因——本测试锁定这条契约，
 * 防止有人把判定改回「只看 state」（那会让缺陷以另一种形态回归）。
 */
function snap(over: Partial<InstanceSnapshot> = {}): InstanceSnapshot {
  return {
    id: 1,
    uuid: 's1',
    instanceId: 3,
    name: '快照',
    kind: 'manual',
    state: 'completed',
    rootBackupId: 902,
    binaryName: '',
    binarySha256: '',
    configHash: '',
    configSummary: '',
    triggeredBy: 1,
    triggeredByRollbackId: 0,
    sizeMb: 0,
    failureReason: '',
    note: '',
    createdAt: '2026-07-13T02:00:00Z',
    updatedAt: '2026-07-13T02:00:00Z',
    ...over,
  }
}

describe('isSnapshotRollable（含 B-1 底链校验）', () => {
  it('completed 且底链可用 → 可回滚', () => {
    expect(isSnapshotRollable(snap())).toBe(true)
    expect(isSnapshotRollable(snap({ state: 'rolled_back', rootBackupState: 'ok' }))).toBe(true)
  })

  it.each(['pending', 'running', 'failed'])('%s 状态不可回滚', (state) => {
    expect(isSnapshotRollable(snap({ state }))).toBe(false)
  })

  it('底链缺失时代码判定为不可回滚（即使 state=completed）', () => {
    const missing = snap({
      rootBackupState: 'missing',
      notRollableReason: '底层备份 #902 已不存在（可能被人工删除或保留策略清理），该快照不可回滚',
    })
    expect(isSnapshotRollable(missing)).toBe(false)
  })

  it('只要有 notRollableReason 就必须判不可回滚（原因即拒绝依据）', () => {
    expect(isSnapshotRollable(snap({ notRollableReason: '快照未关联底层备份（归档未完成）' }))).toBe(false)
  })
})

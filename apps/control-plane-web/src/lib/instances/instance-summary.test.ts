import { describe, it, expect } from 'vitest'
import { summarizeInstances, type InstanceStatusCounts } from './instance-summary'
import type { InstanceInfo } from '@/api/instances'

/** 构造最小实例对象（只填汇总用到的字段）。 */
function inst(status: string): InstanceInfo {
  return {
    id: Math.random(),
    uuid: '',
    nodeId: 1,
    name: 'x',
    type: 'paper',
    role: 'universal',
    processType: 'direct',
    status,
    startCommand: '',
    workDir: '',
    serverPort: 0,
    autoStart: false,
    autoRestart: false,
    tags: '',
    createdAt: '',
  }
}

describe('summarizeInstances', () => {
  it('空集合全为 0', () => {
    const c: InstanceStatusCounts = summarizeInstances([])
    expect(c).toEqual({ total: 0, running: 0, stopped: 0, crashed: 0 })
  })

  it('按状态计数：运行/停止/崩溃，过渡态归入对应桶', () => {
    const c = summarizeInstances([
      inst('RUNNING'),
      inst('RUNNING'),
      inst('STOPPED'),
      inst('CRASHED'),
      inst('STARTING'), // 过渡态计入 running 桶（活动中）
      inst('STOPPING'), // 过渡态计入 running 桶（活动中）
    ])
    expect(c.total).toBe(6)
    expect(c.running).toBe(4) // 2 RUNNING + STARTING + STOPPING
    expect(c.stopped).toBe(1)
    expect(c.crashed).toBe(1)
  })

  it('未知状态只计入 total，不进任何桶', () => {
    const c = summarizeInstances([inst('WEIRD')])
    expect(c.total).toBe(1)
    expect(c.running).toBe(0)
    expect(c.stopped).toBe(0)
    expect(c.crashed).toBe(0)
  })
})

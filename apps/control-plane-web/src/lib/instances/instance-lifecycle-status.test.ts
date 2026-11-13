import { describe, expect, it } from 'vitest'

import { instanceLifecyclePresentation } from './instance-lifecycle-status'

describe('instanceLifecyclePresentation', () => {
  it.each([
    ['操作失败，状态待心跳收敛: 启动失败: 上一代进程未在预算内退出', 'instances.lifecycle.operationFailed'],
    ['启动失败：上一代进程未在预算内退出', 'instances.lifecycle.startFailed'],
  ])('明确操作失败优先于保留的 STARTING 状态：%s', (statusReason, labelKey) => {
    const instance = { status: 'STARTING', statusReason }
    const result = instanceLifecyclePresentation(instance)
    expect(result.kind).toBe('failed')
    expect(result.tone).toBe('danger')
    expect(result.labelKey).toBe(labelKey)
    expect(result.transitioning).toBe(false)
    expect(result.detail).toBe(statusReason)
    expect(instance.status).toBe('STARTING')
  })
  it('不把巡检、配额和泛化待确认文本误称启动失败或 RPC 待确认', () => {
    for (const statusReason of ['配额超限已停止（内存）', '需要人工待确认后再操作', '进程侧证据显示实例仍在运行（心跳清单暂缺）']) {
      const result = instanceLifecyclePresentation({ status: 'STOPPED', statusReason })
      expect(result.kind).toBe('notice')
      expect(result.tone).toBe('warning')
      expect(result.detail).toBe(statusReason)
    }
  })

  it('HTTP 未返回时说明提交中，不改写也不声明后端已进入 STARTING', () => {
    const instance = { status: 'STOPPED', statusReason: '之前的启动错误' }
    const result = instanceLifecyclePresentation(instance, { pendingAction: 'start' })
    expect(result.kind).toBe('submitting')
    expect(result.labelFallback).toBe('启动请求提交中')
    expect(result.detail).toBe('正在提交请求，等待控制面受理')
    expect(instance.status).toBe('STOPPED')
  })
  it('把 RPC/传输取消的待确认结果显示为琥珀待收敛，而不是启动失败', () => {
    const result = instanceLifecyclePresentation({
      status: 'STARTING',
      statusReason: '操作结果待确认，状态查询/心跳收敛: Worker 未连接',
    })

    expect(result.kind).toBe('awaiting_confirmation')
    expect(result.tone).toBe('warning')
    expect(result.detail).toBe('操作结果待确认，状态查询/心跳收敛: Worker 未连接')
  })

  it('把真实启动失败原因保持为明确的红色失败', () => {
    const result = instanceLifecyclePresentation({
      status: 'CRASHED',
      statusReason: '启动失败：上一代进程未在预算内退出',
    })

    expect(result.kind).toBe('failed')
    expect(result.tone).toBe('danger')
    expect(result.detail).toBe('启动失败：上一代进程未在预算内退出')
  })

  it('没有后端原因时只说明正在收敛，不编造进度或 READY', () => {
    const result = instanceLifecyclePresentation({ status: 'STARTING', statusReason: '' })

    expect(result.kind).toBe('starting')
    expect(result.tone).toBe('warning')
    expect(result.detail).toContain('等待')
    expect(result.detail).not.toMatch(/\d+%|READY|就绪/)
  })

  it('卡片和列表使用相同的搭建中语义', () => {
    const result = instanceLifecyclePresentation({
      status: 'STOPPED',
      statusReason: '搭建中：正在下载核心（完成前请勿启动）',
    }, { provisioning: true })

    expect(result.kind).toBe('provisioning')
    expect(result.tone).toBe('warning')
    expect(result.detail).toContain('搭建中：正在下载核心')
  })
})

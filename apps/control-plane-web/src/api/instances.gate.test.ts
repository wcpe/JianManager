import { describe, expect, it } from 'vitest'

import { isProvisioningInstance } from './instances'

// FR-323 启动闸缺口修复：导入/克隆在途实例 statusReason 标「导入中/克隆中」，
// 前端禁启判定须与后端闸同信号源——三种长操作前缀一视同仁。
describe('isProvisioningInstance（长操作在途禁启判定）', () => {
  it.each([
    '搭建中：正在下载核心（完成前请勿启动）',
    '导入中：正在搬迁目录（完成前请勿启动）',
    '克隆中：正在复制工作目录（完成前请勿启动）',
  ])('STOPPED + 前缀 %s → 禁启', (statusReason) => {
    expect(isProvisioningInstance({ status: 'STOPPED', statusReason })).toBe(true)
  })

  it('普通失败原因（如启动失败）不判在途', () => {
    expect(isProvisioningInstance({ status: 'STOPPED', statusReason: '搭建未完成：下载超时' })).toBe(false)
    expect(isProvisioningInstance({ status: 'STOPPED', statusReason: 'Invalid or corrupt jarfile' })).toBe(false)
  })

  it('reason 为空 / 非 STOPPED 状态不判在途', () => {
    expect(isProvisioningInstance({ status: 'STOPPED', statusReason: '' })).toBe(false)
    expect(isProvisioningInstance({ status: 'RUNNING', statusReason: '导入中：正在搬迁目录' })).toBe(false)
  })
})

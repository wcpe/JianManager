import { describe, expect, it } from 'vitest'

import { isProvisioningInstance, isRebuildingInstance } from './instances'

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

/**
 * FR-342 损毁重建在途判定（与 `isProvisioningInstance` 同族：读后端写入的中文前缀）。
 *
 * 判据从 `InstanceConsolePage` 内联收敛到此处，前端只此一处认「重建中」这个词。
 * 注意其脆弱性：后端一旦改词（本地化 / 改措辞）这里会静默失效——见函数注释。
 */
describe('isRebuildingInstance（损毁重建在途判定）', () => {
  it.each([
    '重建中：正在重新下载核心（完成前请勿启动）',
    '重建中：正在重新获取二进制（完成前请勿启动）',
    '重建中：正在重新供给代理核心与配置（完成前请勿启动）',
  ])('DAMAGED + 前缀 %s → 判定为重建在途', (statusReason) => {
    expect(isRebuildingInstance({ status: 'DAMAGED', statusReason })).toBe(true)
  })

  it('非 DAMAGED 状态不判重建在途（即使 reason 带前缀）', () => {
    expect(isRebuildingInstance({ status: 'STOPPED', statusReason: '重建中：正在重新下载核心' })).toBe(false)
    expect(isRebuildingInstance({ status: 'RUNNING', statusReason: '重建中：正在重新下载核心' })).toBe(false)
  })

  it('DAMAGED 但 reason 是别的失败原因时不判在途', () => {
    expect(isRebuildingInstance({ status: 'DAMAGED', statusReason: '核心文件缺失' })).toBe(false)
    expect(isRebuildingInstance({ status: 'DAMAGED', statusReason: '' })).toBe(false)
    expect(isRebuildingInstance({ status: 'DAMAGED', statusReason: undefined })).toBe(false)
  })

  it('不把「搭建中」误判成重建在途（两个判据互不串味）', () => {
    expect(isRebuildingInstance({ status: 'DAMAGED', statusReason: '搭建中：正在下载核心' })).toBe(false)
  })
})

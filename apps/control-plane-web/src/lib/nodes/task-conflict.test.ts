import { describe, expect, it } from 'vitest'
import { conflictText, parseTaskConflict, tasksHref } from './task-conflict'

/** 构造一个 axios 风格错误。 */
const axiosErr = (status: number, data: unknown) => ({ response: { status, data } })

describe('parseTaskConflict', () => {
  it('识别 TASK_EXCLUSIVE_BUSY 并取出占用者（前端据此展示"谁在占着"）', () => {
    const c = parseTaskConflict(
      axiosErr(409, {
        error: 'TASK_EXCLUSIVE_BUSY',
        message: '该节点已有同类任务在跑：安装 JDK Temurin 21（running，进度 40%）',
        existingTask: { taskId: 'task-abc', title: '安装 JDK Temurin 21', progress: 40, state: 'running' },
      }),
    )
    expect(c).not.toBeNull()
    expect(c!.taskId).toBe('task-abc')
    expect(c!.taskTitle).toBe('安装 JDK Temurin 21')
    expect(c!.progress).toBe(40)
  })

  it('别的 409 不算任务占用（必须按 error 码判定，不能只看状态码）', () => {
    expect(parseTaskConflict(axiosErr(409, { error: 'OTHER_CONFLICT', message: 'x' }))).toBeNull()
  })

  it('普通 500 / 网络错误返回 null（走原有错误路径）', () => {
    expect(parseTaskConflict(axiosErr(500, { message: 'boom' }))).toBeNull()
    expect(parseTaskConflict(new Error('network'))).toBeNull()
    expect(parseTaskConflict(undefined)).toBeNull()
  })

  it('缺 existingTask 时仍识别为占用（降级到后端 message）', () => {
    const c = parseTaskConflict(axiosErr(409, { error: 'TASK_EXCLUSIVE_BUSY', message: '已有任务在跑' }))
    expect(c).not.toBeNull()
    expect(c!.taskId).toBeUndefined()
    expect(conflictText(c!)).toBe('已有任务在跑')
  })
})

describe('tasksHref / conflictText', () => {
  it('有 taskId 时深链定位到那一条任务', () => {
    expect(tasksHref({ message: 'x', taskId: 'abc 123' })).toBe('/tasks?task=abc%20123')
    expect(tasksHref({ message: 'x' })).toBe('/tasks')
  })

  it('文案写出占用者与进度，并给出两条出路（等它完成 / 强制停止）', () => {
    const txt = conflictText({ message: 'm', taskTitle: '安装 Node.js 22', progress: 65 })
    expect(txt).toContain('安装 Node.js 22')
    expect(txt).toContain('65%')
    expect(txt).toContain('强制停止')
  })

  it('没有进度信息时不硬造百分比', () => {
    const txt = conflictText({ message: 'm', taskTitle: '安装 JDK Temurin 21' })
    expect(txt).toContain('安装 JDK Temurin 21')
    expect(txt).not.toContain('%')
  })
})

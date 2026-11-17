import { describe, expect, it } from 'vitest'
import { conflictText, parseTaskConflict, tasksHref, tasksHrefForNode } from './task-conflict'

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

describe('tasksHrefForNode', () => {
  it('带节点筛选进任务中心（安装完成后落地用）', () => {
    expect(tasksHrefForNode(7)).toBe('/tasks?nodeId=7')
  })

  it('同时带 taskId 时展开具体那一条', () => {
    // nodeId 在前、task 在后，便于人读；参数顺序不影响解析，但断言固定住避免无意改动。
    expect(tasksHrefForNode(7, 'abc-123')).toBe('/tasks?nodeId=7&task=abc-123')
  })

  it('taskId 会被 URL 编码，特殊字符不会把查询串拆坏', () => {
    const href = tasksHrefForNode(1, 'a b&c=d')
    // 断言"解析回来的值正确"，而不是"编码成哪种字面量"：
    // URLSearchParams 把空格编成 `+`，encodeURIComponent 编成 `%20`，两者解出来等价。
    // 钉死字面量会让这条用例在换了序列化实现时无意义地红。
    const params = new URLSearchParams(href.split('?')[1])
    expect(params.get('task')).toBe('a b&c=d')
    expect(params.get('nodeId')).toBe('1')
  })

  it('不传 taskId 时不出现空的 task 参数', () => {
    expect(tasksHrefForNode(1, '')).toBe('/tasks?nodeId=1')
  })
})

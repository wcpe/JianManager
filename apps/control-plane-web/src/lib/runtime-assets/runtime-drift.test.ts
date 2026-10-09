import { describe, it, expect } from 'vitest'

import { hasRuntimeDrift, runtimeDriftOf } from './runtime-drift'

/**
 * FR-471：「运行态漂移」判定与归一。
 * 唯一判据是 runtimeDriftPid > 0——0 与缺省同义（后端对无漂移实例写 0），
 * 用真值判断会把 0 误当「有漂移」，故判定单点收敛在此。
 */
describe('hasRuntimeDrift（运行态漂移判定，FR-471）', () => {
  it('PID > 0 判为漂移', () => {
    expect(hasRuntimeDrift({ runtimeDriftPid: 41237 })).toBe(true)
    expect(hasRuntimeDrift({ runtimeDriftPid: 1 })).toBe(true)
  })

  it('PID = 0 / 缺省 / undefined 均判为无漂移', () => {
    expect(hasRuntimeDrift({ runtimeDriftPid: 0 })).toBe(false)
    expect(hasRuntimeDrift({})).toBe(false)
    expect(hasRuntimeDrift({ runtimeDriftPid: undefined })).toBe(false)
  })
})

describe('runtimeDriftOf（漂移信息归一，FR-471）', () => {
  it('有漂移时给出 PID 与去空白的命令行', () => {
    expect(runtimeDriftOf({ runtimeDriftPid: 88, runtimeDriftCmdline: '  java -jar s.jar  ' })).toEqual({
      pid: 88,
      cmdline: 'java -jar s.jar',
    })
  })

  it('无漂移时返回 undefined；cmdline 为空串则省略该字段', () => {
    expect(runtimeDriftOf({ runtimeDriftPid: 0, runtimeDriftCmdline: 'java -jar s.jar' })).toBeUndefined()
    expect(runtimeDriftOf({ runtimeDriftPid: 9, runtimeDriftCmdline: '   ' })).toEqual({ pid: 9, cmdline: undefined })
  })
})

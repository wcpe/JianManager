import { describe, it, expect } from 'vitest'
import { canRunDanger, Role } from './danger'
import { decodeJwt } from '@/lib/auth/jwt'

describe('canRunDanger', () => {
  it('组成员被拒绝组范围与平台范围危险操作', () => {
    expect(canRunDanger(Role.Member, 'group')).toBe(false)
    expect(canRunDanger(Role.Member, 'platform')).toBe(false)
  })

  it('组管理员可执行组范围，但被拒平台范围', () => {
    expect(canRunDanger(Role.GroupAdmin, 'group')).toBe(true)
    expect(canRunDanger(Role.GroupAdmin, 'platform')).toBe(false)
  })

  it('组运维（role=2）不得执行组范围危险操作', () => {
    // 回归：早前判定是 `role >= 1`，于是 2 与 3 都被当成“组管理员及以上”。
    // 而它们的权限种子不含 instance.delete / instance.write。
    expect(canRunDanger(Role.GroupOperator, 'group')).toBe(false)
    expect(canRunDanger(Role.GroupOperator, 'platform')).toBe(false)
  })

  it('组只读（role=3）不得执行任何危险操作', () => {
    // 最高危：只读账号点「删除实例」时，前端门禁必须拦住，不能只靠后端 403。
    expect(canRunDanger(Role.GroupViewer, 'group')).toBe(false)
    expect(canRunDanger(Role.GroupViewer, 'platform')).toBe(false)
  })

  it('未知角色值一律拒绝（fail-closed）', () => {
    expect(canRunDanger(5, 'group')).toBe(false)
    expect(canRunDanger(99, 'group')).toBe(false)
    expect(canRunDanger(-1, 'platform')).toBe(false)
  })

  it('平台管理员可执行所有范围', () => {
    expect(canRunDanger(Role.PlatformAdmin, 'group')).toBe(true)
    expect(canRunDanger(Role.PlatformAdmin, 'platform')).toBe(true)
  })

  it('未登录（role 为 null）一律被拒', () => {
    expect(canRunDanger(null, 'group')).toBe(false)
    expect(canRunDanger(null, 'platform')).toBe(false)
  })
})

/** 构造一个未签名的测试 JWT（仅 payload 有效）。 */
function makeJwt(payload: Record<string, unknown>): string {
  const b64url = (obj: Record<string, unknown>) =>
    btoa(JSON.stringify(obj)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
  return `${b64url({ alg: 'HS256', typ: 'JWT' })}.${b64url(payload)}.sig`
}

describe('decodeJwt', () => {
  it('解码 role/username/userId 声明', () => {
    const token = makeJwt({ userId: 7, username: 'admin', role: 10 })
    expect(decodeJwt(token)).toMatchObject({ userId: 7, username: 'admin', role: 10 })
  })

  it('token 缺失或格式错误返回 null', () => {
    expect(decodeJwt(null)).toBeNull()
    expect(decodeJwt(undefined)).toBeNull()
    expect(decodeJwt('not-a-jwt')).toBeNull()
    expect(decodeJwt('a.!!!.c')).toBeNull()
  })
})

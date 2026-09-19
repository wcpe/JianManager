import { beforeEach, describe, expect, it } from 'vitest'
import { usePermissionsStore } from './permissions'
import { useAuthStore } from './auth'
import { ALL_PERMISSION_NODE_IDS } from '@/lib/roles'

describe('permissions store（FR-432）', () => {
  beforeEach(() => {
    usePermissionsStore.getState().reset()
    useAuthStore.setState({ role: null })
  })

  it('未加载且非平台管理员 → hasPerm 为 false；JWT role=10 未加载可放行；加载后空集合拒绝', () => {
    const store = usePermissionsStore.getState()
    expect(store.loaded).toBe(false)
    expect(store.hasPerm('instance.operate')).toBe(false)

    // JWT 声明超管时，未加载也可放行（避免首帧误禁）
    useAuthStore.setState({ role: 10 })
    expect(usePermissionsStore.getState().hasPerm('instance.operate')).toBe(true)

    useAuthStore.setState({ role: 0 })
    usePermissionsStore.setState({ loaded: true, isPlatformAdmin: false, nodes: new Set() })
    expect(usePermissionsStore.getState().hasPerm('instance.operate')).toBe(false)
  })

  it('平台管理员恒有权限', () => {
    usePermissionsStore.setState({
      loaded: true,
      isPlatformAdmin: true,
      nodes: new Set(),
    })
    expect(usePermissionsStore.getState().hasPerm('rbac.manage')).toBe(true)
    expect(usePermissionsStore.getState().hasPerm('terminal.access')).toBe(true)
  })

  it('setFromAuthMe 写入 nodes / roleKey', () => {
    usePermissionsStore.getState().setFromAuthMe({
      userId: 1,
      username: 'admin',
      role: 10,
      roleKey: 'platform_admin',
      isPlatformAdmin: true,
      nodes: ALL_PERMISSION_NODE_IDS,
    })
    const s = usePermissionsStore.getState()
    expect(s.loaded).toBe(true)
    expect(s.isPlatformAdmin).toBe(true)
    expect(s.roleKey).toBe('platform_admin')
    expect(s.hasPerm('instance.read')).toBe(true)
  })

  it('auth/me 失败回落：JWT role===10 → 平台管理员', () => {
    useAuthStore.setState({ role: 10, isAuthenticated: true })
    usePermissionsStore.setState({
      nodes: new Set(),
      roleKey: null,
      isPlatformAdmin: true,
      loaded: true,
    })
    expect(usePermissionsStore.getState().hasPerm('anything')).toBe(true)
  })
})

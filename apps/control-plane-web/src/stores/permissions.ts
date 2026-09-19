import { create } from 'zustand'
import api from '@/api/client'
import { isPlatformAdmin as isAdminRole } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth'

/** GET /api/v1/auth/me 响应（FR-432）。 */
export interface AuthMeResponse {
  userId: number
  username: string
  role: number
  roleKey: string
  isPlatformAdmin: boolean
  nodes: string[]
}

interface PermissionsState {
  /** 当前用户有效权限节点集合。auth/me 未成功前为空 Set。 */
  nodes: Set<string>
  /** 当前用户角色 key（member / group_admin / …）。 */
  roleKey: string | null
  isPlatformAdmin: boolean
  /** 是否已尝试过拉取 /auth/me（成功或失败回落均视为 true）。 */
  loaded: boolean
  /** 拉取 /auth/me；失败回落 JWT role===10 + 空集合。 */
  loadFromAuthMe: () => Promise<void>
  /** 直接写入（登录成功回调 / 测试）。 */
  setFromAuthMe: (data: AuthMeResponse) => void
  /** 登出清空。 */
  reset: () => void
  /** 能力判断：平台管理员恒 true；未加载时仅 JWT role=10 乐观放行，其余拒绝。 */
  hasPerm: (node: string) => boolean
}

export const usePermissionsStore = create<PermissionsState>((set, get) => ({
  nodes: new Set<string>(),
  roleKey: null,
  isPlatformAdmin: false,
  loaded: false,

  setFromAuthMe: (data) => {
    set({
      nodes: new Set(data.nodes ?? []),
      roleKey: data.roleKey ?? null,
      isPlatformAdmin: Boolean(data.isPlatformAdmin) || isAdminRole(data.role),
      loaded: true,
    })
  },

  loadFromAuthMe: async () => {
    try {
      const { data } = await api.get<AuthMeResponse>('/auth/me')
      get().setFromAuthMe(data)
    } catch {
      const role = useAuthStore.getState().role
      set({
        nodes: new Set<string>(),
        roleKey: null,
        isPlatformAdmin: isAdminRole(role),
        loaded: true,
      })
    }
  },

  reset: () => {
    set({
      nodes: new Set<string>(),
      roleKey: null,
      isPlatformAdmin: false,
      loaded: false,
    })
  },

  hasPerm: (node) => {
    const { nodes, isPlatformAdmin, loaded, roleKey } = get()
    if (isPlatformAdmin) return true
    // 未加载完成：非管理员不可乐观放行（否则清空权限后侧栏仍显示全部路由）
    if (!loaded) {
      const role = useAuthStore.getState().role
      if (isAdminRole(role)) return true
      return false
    }
    if (nodes.size === 0 && roleKey !== 'platform_admin') return false
    return nodes.has(node)
  },
}))

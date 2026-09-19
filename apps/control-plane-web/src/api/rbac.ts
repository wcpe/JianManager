import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

/** 静态权限目录（与后端 PermissionCatalog 对齐）。 */
export interface PermNodeDef {
  id: string
  label: string
  risk?: boolean
}

export interface PermDomain {
  domain: string
  label: string
  nodes: PermNodeDef[]
}

export interface RbacCatalogResponse {
  domains: PermDomain[]
}

/** 角色模板。 */
export interface RbacRole {
  id: number
  key: string
  name: string
  description?: string
  isSystem: boolean
  nodes: string[]
  createdAt: string
  updatedAt: string
}

export interface RbacRolesResponse {
  items: RbacRole[]
}

export interface CreateRoleRequest {
  name: string
  description?: string
  nodes?: string[]
}

export interface UpdateRoleRequest {
  name?: string
  description?: string
  nodes?: string[]
}

/** 用户有效权限（角色模板 ⊕ 覆盖）。 */
export interface UserPermissionOverride {
  node: string
  effect: 'allow' | 'deny'
}

export interface UserPermissionsResponse {
  userId: number
  roleKey: string
  roleId?: number
  nodes: string[]
  overrides: UserPermissionOverride[]
  isPlatformAdmin: boolean
}

export interface SetUserRoleRequest {
  roleId: number
}

export interface SetUserOverridesRequest {
  overrides: UserPermissionOverride[]
}

const rbacKeys = {
  catalog: ['rbac', 'catalog'] as const,
  roles: ['rbac', 'roles'] as const,
  userPermissions: (userId: number) => ['rbac', 'user-permissions', userId] as const,
}

/** GET /api/v1/rbac/catalog */
export function useRbacCatalog() {
  return useQuery({
    queryKey: rbacKeys.catalog,
    queryFn: async () => {
      const { data } = await api.get<RbacCatalogResponse>('/rbac/catalog')
      return data
    },
  })
}

/** GET /api/v1/rbac/roles */
export function useRbacRoles() {
  return useQuery({
    queryKey: rbacKeys.roles,
    queryFn: async () => {
      const { data } = await api.get<RbacRolesResponse>('/rbac/roles')
      return data.items ?? []
    },
  })
}

export function useCreateRbacRole() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (body: CreateRoleRequest) => {
      const { data } = await api.post<RbacRole>('/rbac/roles', body)
      return data
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: rbacKeys.roles })
    },
  })
}

export function useUpdateRbacRole() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({ id, ...body }: UpdateRoleRequest & { id: number }) => {
      const { data } = await api.patch<RbacRole>(`/rbac/roles/${id}`, body)
      return data
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: rbacKeys.roles })
    },
  })
}

export function useDeleteRbacRole() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api.delete(`/rbac/roles/${id}`),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: rbacKeys.roles })
    },
  })
}

/** PUT /api/v1/rbac/roles/:id/permissions */
export function useSetRolePermissions() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, nodes }: { id: number; nodes: string[] }) =>
      api.put(`/rbac/roles/${id}/permissions`, { nodes }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: rbacKeys.roles })
    },
  })
}

/** GET /api/v1/rbac/users/:id/permissions */
export function useUserPermissions(userId: number | null, enabled = true) {
  return useQuery({
    queryKey: rbacKeys.userPermissions(userId ?? -1),
    enabled: enabled && userId != null,
    queryFn: async () => {
      const { data } = await api.get<UserPermissionsResponse>(`/rbac/users/${userId}/permissions`)
      return data
    },
  })
}

/** PUT /api/v1/rbac/users/:id/role */
export function useSetUserRole() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ userId, roleId }: SetUserRoleRequest & { userId: number }) =>
      api.put(`/rbac/users/${userId}/role`, { roleId }),
    onSuccess: (_data, vars) => {
      void qc.invalidateQueries({ queryKey: rbacKeys.userPermissions(vars.userId) })
      void qc.invalidateQueries({ queryKey: rbacKeys.roles })
    },
  })
}

/** PUT /api/v1/rbac/users/:id/overrides */
export function useSetUserOverrides() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ userId, overrides }: SetUserOverridesRequest & { userId: number }) =>
      api.put(`/rbac/users/${userId}/overrides`, { overrides }),
    onSuccess: (_data, vars) => {
      void qc.invalidateQueries({ queryKey: rbacKeys.userPermissions(vars.userId) })
    },
  })
}

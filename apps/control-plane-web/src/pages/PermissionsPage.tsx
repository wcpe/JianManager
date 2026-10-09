// 视图已回迁应用侧（原 ADR-097 迁包已撤销）；本层只做目录/角色/用户取数、选中态与深链、两个保存动作、权限门禁与 toast。
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useUsers } from '@/api/users'
import { useRbacCatalog, useRbacRoles, useSetRolePermissions, useSetUserOverrides, useSetUserRole, useUserPermissions } from '@/api/rbac'
import { usePermissionsStore } from '@/stores/permissions'
import { PermissionsPageView } from '@/components/views/permissions/PermissionsPageView'
import type { PermissionSelection } from '@/components/views/permissions/PermissionsPageView'

/**
 * 深链初始选择：只在挂载时读一次 `?user=<id>`（`?role` 不参与，与迁包前一致）。
 * 选择变更不回写 URL，故 URL 只是入口、不是真源。
 */
function readDeepLinkedSelection(): PermissionSelection {
  try {
    const qUser = new URLSearchParams(window.location.search).get('user')
    if (qUser) {
      const id = Number(qUser)
      if (Number.isInteger(id) && id > 0) return { kind: 'user', id }
    }
  } catch {
    /* non-DOM */
  }
  return null
}

/**
 * 权限管理页容器（ADR-097 b 范式）：三栏工作区的展示与草稿交共享视图，
 * 本层只负责目录/角色/用户取数、选中态（含 `?user=<id>` 深链一次读取）、两个保存动作与 toast。
 *
 * 受控边界：`selection` 留在这里——它是 `useUserPermissions` 的查询键，一变就触发取数，
 * 且深链解析属路由层；视图内只留不触发取数的纯 UI 状态（搜索词、连选范围、草稿、弹窗开合）。
 * 鉴权门禁（`rbac.read` / `rbac.manage`）在应用侧判定后以布尔注入，包内不读登录态。
 * 保存时机沿用迁包前语义：显式点保存 → 确认弹窗 → 提交；失败弹错误 toast 且不关弹窗。
 * 保留同路径默认导出，路由表无需改动。
 */
export default function PermissionsPage() {
  const { t } = useTranslation()
  const hasPerm = usePermissionsStore((s) => s.hasPerm)
  const viewerIsPlatformAdmin = usePermissionsStore((s) => s.isPlatformAdmin)

  // 深链初始选择：useState 初始化读 URL（此后只由视图点击驱动，不回写）。
  const [selection, setSelection] = useState<PermissionSelection>(readDeepLinkedSelection)

  const { data: catalog, isLoading: catalogLoading } = useRbacCatalog()
  const { data: roles = [], isLoading: rolesLoading } = useRbacRoles()
  const { data: users = [] } = useUsers()

  const selectedUserId = selection?.kind === 'user' ? selection.id : null
  const { data: userPerms } = useUserPermissions(selectedUserId, selection?.kind === 'user')

  const setRolePerms = useSetRolePermissions()
  const setUserOverrides = useSetUserOverrides()
  const setUserRole = useSetUserRole()

  return (
    <PermissionsPageView
      catalog={catalog?.domains}
      catalogLoading={catalogLoading}
      roles={roles}
      rolesLoading={rolesLoading}
      users={users}
      selection={selection}
      onSelectionChange={setSelection}
      userPermissions={userPerms}
      canRead={viewerIsPlatformAdmin || hasPerm('rbac.read')}
      canManage={viewerIsPlatformAdmin || hasPerm('rbac.manage')}
      saving={setRolePerms.isPending || setUserOverrides.isPending}
      onSaveRolePermissions={async (roleId, nodes) => {
        try {
          await setRolePerms.mutateAsync({ id: roleId, nodes })
          toast.success(t('permissions.saved'))
          return true
        } catch {
          toast.error(t('common.error'))
          return false
        }
      }}
      onSaveUserPermissions={async ({ userId, overrides, roleId }) => {
        try {
          await setUserOverrides.mutateAsync({ userId, overrides })
        } catch {
          toast.error(t('common.error'))
          return false
        }
        // 覆盖保存成功后才改绑角色模板：改绑是随后的独立请求，失败不额外提示（与迁包前一致）。
        if (roleId !== null) setUserRole.mutate({ userId, roleId })
        toast.success(t('permissions.saved'))
        return true
      }}
    />
  )
}

/**
 * @file UsersPageView：用户管理页（用户列表 + 邀请面板 + 新建/编辑/删除对话框）的受控视图，
 *       取数、五个写动作与 toast 由应用容器负责。
 * @input views/CreateUserDialog（新建用户视图）、views/CreateInvitationDialog（签发邀请视图）、
 *        views/EditUserDialogView（编辑用户视图）、views/DangerConfirm（删除二次确认）、
 *        views/config-explorer/ConfigRow（卡片行/开关/视图切换）、lib/roles（平台管理员判定）、
 *        Button/Panel/StatusBadge/Table/布局层原语、翻译上下文
 * @output UsersPageView、UsersPageViewProps、UserRow、UserInvitationRow、CreateUserPayload、UsersPermissionsLinkArgs
 * @sync apps/control-plane-web/src/pages/UsersPage.tsx、apps/control-plane-web/src/pages/UsersPage.dom.test.tsx
 * @since FR-502（组件受控化迁包；原页 FR-199 身份访问域，写入口可见性为 FR-432 user.manage）
 */
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Shield, UserRound } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { PageHeader, PageShell } from '@jianmanager/ui/components/layout'
import { Panel } from '@jianmanager/ui/components/panel'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import {
  Table,
  TableBody,
  TableCard,
  TableCardFooter,
  TableCardHeader,
  TableCell,
  TableEmptyRow,
  TableHead,
  TableHeader,
  TableRow,
  TableSkeletonRows,
} from '@jianmanager/ui/components/table'
import CreateUserDialogView, { type CreateUserNotice } from '@/components/views/users/CreateUserDialog'
import CreateInvitationDialogView from '@/components/views/users/CreateInvitationDialog'
import DangerConfirm from '@/components/views/common/DangerConfirm'
import { EditUserDialogView } from '@/components/views/users/EditUserDialogView'
import type { EditUserValues } from '@/components/views/users/EditUserDialogView'
import { ConfigRow, ConfigSwitch, ConfigViewToggle } from '@/components/views/config-explorer/ConfigRow'
import type { ConfigView } from '@/components/views/config-explorer/ConfigRow'
import { isPlatformAdmin } from '@/lib/shared/roles'

/**
 * 用户行（本视图渲染所需的最小字段集）。
 *
 * 刻意只声明用到的字段，而非照搬应用侧 `@/api/users` 的 `UserInfo`：容器直接传 API
 * 返回的完整对象也结构兼容，无需把该 API 类型迁进包。
 */
export interface UserRow {
  id: number
  username: string
  /** 数值角色：0 组成员 / 1 组管理员 / 2 组运维 / 3 组只读 / 10 平台管理员。 */
  role: number
  /** 0 = 启用，其余（1）= 禁用。 */
  status: number
  createdAt: string
}

/** 邀请行（本视图渲染所需的最小字段集，同 `UserRow` 的取舍）。 */
export interface UserInvitationRow {
  id: number
  email: string
  expiresAt: string
  /** 是否已被管理员撤销。 */
  revoked: boolean
  /** 是否已被使用（已使用的不再提供撤销按钮）。 */
  used: boolean
}

/** 新建用户的提交载荷（与包内 CreateUserDialog 视图 `onCreate` 的入参同构）。 */
export interface CreateUserPayload {
  username: string
  password: string
  role: number
  status: number
}

/** 权限页跳转插槽参数（`to` 由本视图拼好，外壳负责转成路由跳转或原生锚点）。 */
export interface UsersPermissionsLinkArgs {
  /** 目标路径，形如 `/permissions?user=<id>`。 */
  to: string
  /** 链接 title（沿用原页 `permissions.openForUser` 文案）。 */
  title: string
  /** 链接内容（盾牌图标 + 「权限」文案）。 */
  children: ReactNode
}

/**
 * 受控边界（ADR-097 b 范式）：**不取数、不发请求、不弹 toast**。
 * - 用户列表与邀请列表经 props 注入（容器调 `useUsers` / `useUserInvitations`）；
 * - 五个写动作（新建用户、签发邀请、更新用户、删除用户、撤销邀请）以回调上报，由容器
 *   执行 mutation 并决定成功/失败文案；两个「返回 Promise」的动作失败请抛错，视图内对话框
 *   据此回显服务端 message；
 * - **留本组件**的纯 UI 状态：三个对话框的开合与编辑目标、待删除确认目标、卡片/列表视图切换；
 * - **归容器**的受控状态：无——本页无过滤/分页等触发取数的状态（列表口径由容器查询决定）；
 * - **鉴权状态一律由容器注入**：`canManageUsers` 决定写入口是否可见（FR-432：平台管理员或
 *   持有 `user.manage`），`dangerAllowed` 决定平台级删除是否放行。包内不读登录态；
 * - 跳转 `/permissions?user=<id>` 走 `renderPermissionsLink` 插槽（缺省渲染原生 `<a href>`），
 *   包内不依赖 react-router。
 */
export interface UsersPageViewProps {
  /** 用户列表；容器取数后注入（缺省或空数组渲染空态）。 */
  users?: UserRow[]
  /** 列表加载态（渲染骨架行）。 */
  isLoading?: boolean
  /** 邀请列表；容器取数后注入（写入口可见时展示）。 */
  invitations?: UserInvitationRow[]
  /** 是否展示用户写入口（创建/邀请/权限/编辑/删除）；缺省不展示，与后端 `user.manage` 门禁同向。 */
  canManageUsers?: boolean
  /** 状态开关在途：禁用全部行的开关（与迁包前一致，不区分行）。 */
  updating?: boolean
  /** 撤销邀请在途：禁用撤销按钮。 */
  revoking?: boolean
  /** 危险操作（删除用户）是否放行：应用侧读角色等级后注入（组件库不持鉴权状态）。 */
  dangerAllowed: boolean
  /** 提示通道：新建用户成功的提示文案由容器弹（包内不弹 toast）。 */
  notify: CreateUserNotice
  /** 新建用户；失败请抛错，对话框取服务端 message 作内联提示。 */
  onCreateUser: (payload: CreateUserPayload) => Promise<void>
  /** 签发邀请；失败请抛错，对话框取服务端 message 作内联提示。 */
  onCreateInvitation: (email: string) => Promise<{ invitationUrl: string }>
  /** 启用/停用切换上报（容器执行更新并决定提示文案，翻转口径留在容器）。 */
  onToggleStatus: (user: UserRow) => void
  /** 更新用户（角色与可选重置密码）；失败请抛错，对话框取服务端 message 作内联提示。 */
  onUpdateUser: (id: number, values: EditUserValues) => Promise<void>
  /** 删除已确认的用户（二次确认已在本视图内完成）。 */
  onDeleteUser: (id: number) => void
  /** 撤销未使用的邀请（容器执行 DELETE 并决定提示文案）。 */
  onRevokeInvitation: (id: number) => void
  /** 权限页跳转插槽；缺省渲染原生 `<a href>`（组件博物馆无路由时可用）。 */
  renderPermissionsLink?: (args: UsersPermissionsLinkArgs) => ReactNode
}

/**
 * 用户管理页（FR-199 身份访问域）：用户列表支持卡片/列表两种视图，行内可启停、编辑、删除，
 * 并可跳转该用户的权限页；平台管理员另有邀请签发与撤销面板。
 */
export function UsersPageView({
  users,
  isLoading = false,
  invitations,
  canManageUsers = false,
  updating = false,
  revoking = false,
  dangerAllowed,
  notify,
  onCreateUser,
  onCreateInvitation,
  onToggleStatus,
  onUpdateUser,
  onDeleteUser,
  onRevokeInvitation,
  renderPermissionsLink,
}: UsersPageViewProps) {
  const { t } = useTranslation()
  // 纯 UI 状态：三个对话框的开合/编辑目标、待删除确认目标、卡片-列表视图切换。
  const [showCreate, setShowCreate] = useState(false)
  const [showInvite, setShowInvite] = useState(false)
  const [editUser, setEditUser] = useState<UserRow | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<{ id: number; username: string } | null>(null)
  const [view, setView] = useState<ConfigView>('list')

  const roleLabel = (role: number): string => {
    switch (role) {
      case 0:
        return t('users.member')
      case 1:
        return t('users.groupAdmin')
      case 2:
        return t('users.groupOperator')
      case 3:
        return t('users.groupViewer')
      case 10:
        return t('users.platformAdmin')
      default:
        return t('users.roleUnknown', { role })
    }
  }

  // 平台管理员主色 pill，其余中性，作角色 pill 着色。
  const roleTone = (role: number) => (isPlatformAdmin(role) ? 'info' : 'neutral')

  // 权限页链接：包内只拼路径，跳转实现由插槽决定（缺省原生锚点，等价于原先的 <Link>）。
  const permissionsLink = (userId: number): ReactNode => {
    const title = t('permissions.openForUser')
    const children = (
      <>
        <Shield className="mr-1 size-3" />
        {t('permissions.title')}
      </>
    )
    const to = `/permissions?user=${userId}`
    return renderPermissionsLink ? (
      renderPermissionsLink({ to, title, children })
    ) : (
      <a href={to} title={title}>
        {children}
      </a>
    )
  }

  const totalUsers = users?.length ?? 0
  const enabledUsers = (users ?? []).filter((u) => u.status === 0).length

  return (
    // 全量对齐：标题上移到页面级 PageHeader（页名只出现一次、且在页面最上面），
    // 卡片头只留计数与操作（TableCardHeader.title 已改为可选）。
    <PageShell data-page="users">
      <PageHeader title={t('users.title')} />
      <TableCard>
        <TableCardHeader
          count={t('users.cardCount', { total: totalUsers })}
          actions={
            <>
              <ConfigViewToggle view={view} onChange={setView} cardLabel={t('common.cardView')} listLabel={t('common.listView')} />
              {canManageUsers && <Button variant="outline" onClick={() => setShowInvite(true)}>{t('users.inviteUser')}</Button>}
              {canManageUsers && <Button onClick={() => setShowCreate(true)}>+ {t('users.createUser')}</Button>}
            </>
          }
        />

        {isLoading ? (
          <Table appearance="refined">
            <TableHeader>
              <TableRow>
                <TableHead>{t('users.username')}</TableHead>
                <TableHead>{t('users.role')}</TableHead>
                <TableHead>{t('users.status')}</TableHead>
                <TableHead align="right">{t('users.createdAt')}</TableHead>
                <TableHead align="right">{t('users.actions')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableSkeletonRows rows={4} cols={5} />
            </TableBody>
          </Table>
        ) : !users || users.length === 0 ? (
          <Table appearance="refined">
            <TableHeader>
              <TableRow>
                <TableHead>{t('users.username')}</TableHead>
                <TableHead>{t('users.role')}</TableHead>
                <TableHead>{t('users.status')}</TableHead>
                <TableHead align="right">{t('users.createdAt')}</TableHead>
                <TableHead align="right">{t('users.actions')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableEmptyRow colSpan={5} icon={<UserRound />} title={t('users.empty')} description={t('users.emptyHint')} />
            </TableBody>
          </Table>
        ) : view === 'card' ? (
          <div className="flex flex-col gap-2.5 p-3">
            {users.map((u) => (
              <ConfigRow
                key={u.id}
                icon={<UserRound className="size-[18px]" />}
                tone={u.status === 0 ? 'primary' : 'neutral'}
                title={u.username}
                subtitle={`${roleLabel(u.role)} · ${new Date(u.createdAt).toLocaleDateString()}`}
                trailing={
                  <>
                    <StatusBadge level={roleTone(u.role)} label={roleLabel(u.role)} dot={false} />
                    <StatusBadge
                      level={u.status === 0 ? 'success' : 'neutral'}
                      label={u.status === 0 ? t('users.enabled') : t('users.disabled')}
                    />
                    <ConfigSwitch
                      checked={u.status === 0}
                      disabled={updating}
                      onChange={() => onToggleStatus(u)}
                      label={t('users.status')}
                      onLabel={t('users.enabled')}
                      offLabel={t('users.disabled')}
                    />
                    {canManageUsers && (
                      <Button variant="ghost" size="xs" asChild data-testid={`users-permissions-link-${u.id}`}>
                        {permissionsLink(u.id)}
                      </Button>
                    )}
                    <Button variant="ghost" size="xs" onClick={() => setEditUser(u)}>
                      {t('common.edit')}
                    </Button>
                    <Button
                      variant="ghost"
                      size="xs"
                      className="text-status-danger hover:text-status-danger"
                      onClick={() => setDeleteTarget({ id: u.id, username: u.username })}
                    >
                      {t('common.delete')}
                    </Button>
                  </>
                }
              />
            ))}
          </div>
        ) : (
          <Table appearance="refined">
            <TableHeader>
              <TableRow>
                <TableHead>{t('users.username')}</TableHead>
                <TableHead>{t('users.role')}</TableHead>
                <TableHead>{t('users.status')}</TableHead>
                <TableHead align="right">{t('users.createdAt')}</TableHead>
                <TableHead align="right">{t('users.actions')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {users.map((u) => (
                <TableRow key={u.id}>
                  <TableCell className="font-medium text-foreground">{u.username}</TableCell>
                  <TableCell>
                    <StatusBadge level={roleTone(u.role)} label={roleLabel(u.role)} dot={false} />
                  </TableCell>
                  <TableCell>
                    <ConfigSwitch
                      checked={u.status === 0}
                      disabled={updating}
                      onChange={() => onToggleStatus(u)}
                      label={t('users.status')}
                      onLabel={t('users.enabled')}
                      offLabel={t('users.disabled')}
                    />
                  </TableCell>
                  <TableCell align="right" className="text-muted-foreground tabular-nums">{new Date(u.createdAt).toLocaleDateString()}</TableCell>
                  <TableCell align="right">
                    <div className="flex justify-end gap-1">
                      {canManageUsers && (
                        <Button variant="ghost" size="xs" asChild data-testid={`users-permissions-link-${u.id}`}>
                          {permissionsLink(u.id)}
                        </Button>
                      )}
                      <Button variant="ghost" size="xs" onClick={() => setEditUser(u)}>
                        {t('common.edit')}
                      </Button>
                      <Button
                        variant="ghost"
                        size="xs"
                        onClick={() => setDeleteTarget({ id: u.id, username: u.username })}
                        className="text-status-danger hover:text-status-danger"
                      >
                        {t('common.delete')}
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}

        <TableCardFooter>
          {t('users.cardSummary', { total: totalUsers, enabled: enabledUsers })}
        </TableCardFooter>
      </TableCard>

      {/* 三个对话框均为已回迁应用侧的受控视图：开关留本组件（与列表同屏的本地 UI 状态），
          写动作经回调交由容器执行（成功提示与列表失效都在容器侧）。 */}
      <CreateUserDialogView
        open={showCreate}
        onClose={() => setShowCreate(false)}
        onCreate={onCreateUser}
        notify={notify}
      />
      <CreateInvitationDialogView
        open={showInvite}
        onClose={() => setShowInvite(false)}
        onCreate={onCreateInvitation}
      />

      {canManageUsers && (
        <Panel>
          <h2 className="text-sm font-semibold">{t('users.invitations')}</h2>
          {!invitations || invitations.length === 0 ? (
            <p className="mt-3 text-sm text-muted-foreground">{t('users.invitationsEmpty')}</p>
          ) : (
            <div className="mt-3 space-y-2">
              {invitations.map((invitation) => (
                <div key={invitation.id} className="flex items-center justify-between gap-3 rounded border px-3 py-2 text-sm">
                  <div className="min-w-0">
                    <p className="truncate font-medium">{invitation.email}</p>
                    <p className="text-xs text-muted-foreground">{t('users.invitationExpiresAt', { date: new Date(invitation.expiresAt).toLocaleString() })}</p>
                  </div>
                  {invitation.revoked ? (
                    <span className="text-xs text-muted-foreground">{t('users.invitationRevoked')}</span>
                  ) : !invitation.used && (
                    <Button variant="ghost" size="xs" onClick={() => onRevokeInvitation(invitation.id)} disabled={revoking}>
                      {t('users.revokeInvitation')}
                    </Button>
                  )}
                </div>
              ))}
            </div>
          )}
        </Panel>
      )}

      {/* 删除二次确认：scope 固定 platform（与原页一致，不降级）、是否放行由容器注入。 */}
      <DangerConfirm
        open={deleteTarget !== null}
        title={t('danger.deleteUserTitle', { name: deleteTarget?.username ?? '' })}
        description={t('danger.deleteUserDesc')}
        confirmLabel={t('common.delete')}
        confirmText={deleteTarget?.username}
        scope="platform"
        allowed={dangerAllowed}
        onConfirm={() => {
          if (deleteTarget) onDeleteUser(deleteTarget.id)
          setDeleteTarget(null)
        }}
        onCancel={() => setDeleteTarget(null)}
      />

      {/* 编辑目标以 id 作 key，切换用户时表单重置（沿用迁包前的调用约定）。 */}
      {editUser && (
        <EditUserDialogView
          key={editUser.id}
          user={editUser}
          submitting={updating}
          onClose={() => setEditUser(null)}
          onSubmit={(values) => onUpdateUser(editUser.id, values)}
        />
      )}
    </PageShell>
  )
}

export default UsersPageView

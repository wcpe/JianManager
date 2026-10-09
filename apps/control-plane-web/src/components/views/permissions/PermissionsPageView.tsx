/**
 * @file PermissionsPageView：权限管理三栏页（对象选择 / 权限树编辑 / 实时生效预览）的受控视图，
 *       目录与列表取数、选中态深链、两个保存动作与 toast 由应用容器负责。
 * @input lib/permission-explain（节点与域的大白话说明）、lib/roles（用户行的角色 i18n 键）、
 *        Button/Input/Panel/Badge/Checkbox/Dialog/Select 原语、PageShell 布局、翻译上下文
 * @output PermissionsPageView、PermissionsPageViewProps、PermissionSelection、PermissionOverride、
 *         PermissionsRoleRow、PermissionsUserRow、PermissionsCatalogDomain、PermissionsCatalogNodeDef、
 *         PermissionsCatalogNode、PermissionsCatalogGroup、PermissionsUserPermissions、
 *         SaveUserPermissionsArgs、PermissionsToggleEvent
 * @sync apps/control-plane-web/src/pages/PermissionsPage.tsx、apps/control-plane-web/src/pages/PermissionsPage.dom.test.tsx
 * @since FR-502（组件受控化迁包；原页 FR-432 权限管理三栏页）
 */
import { useCallback, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { CheckCircle2, Diff, Link2, UsersRound } from 'lucide-react'
import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { Input } from '@jianmanager/ui/components/input'
import { PageHeader, PageShell } from '@jianmanager/ui/components/layout'
import { Panel } from '@jianmanager/ui/components/panel'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@jianmanager/ui/components/select'
import { cn } from '@jianmanager/ui'
import { permDomainExplain, permExplain } from '@/lib/permissions/permission-explain'
import { ROLE_LABEL_KEY } from '@/lib/shared/roles'

/** 选中对象：角色模板或用户；null 表示未选择。 */
export type PermissionSelection =
  | { kind: 'role'; id: number }
  | { kind: 'user'; id: number }
  | null

/** 用户覆盖项：allow 叠加、deny 剔除（deny 永胜）。 */
export interface PermissionOverride {
  node: string
  effect: 'allow' | 'deny'
}

/**
 * 角色模板行（本视图渲染所需的最小字段集）。
 *
 * 刻意只声明用到的字段，而非照搬应用侧 `@/api/rbac` 的 `RbacRole`：容器直接传 API 返回的
 * 完整对象也结构兼容，无需把该 API 类型迁进包（同 `UserRow` 的取舍）。
 */
export interface PermissionsRoleRow {
  id: number
  /** 角色 key；`platform_admin` 走超级管理员锁定（树与覆盖均不可编辑）。 */
  key: string
  name: string
  description?: string
  /** 系统内置角色（渲染 `permissions.systemRole` 角标）。 */
  isSystem: boolean
  /** 模板节点 id 列表。 */
  nodes: string[]
  /**
   * 角色最近更新时间。它是编辑器重置 key 的一部分：模板被别处改过（updatedAt 变）时，
   * 正在编辑的草稿跟着回到新基线，避免用旧草稿覆盖新模板。
   */
  updatedAt: string
}

/** 用户行（本视图渲染所需的最小字段集）。 */
export interface PermissionsUserRow {
  id: number
  username: string
  /** 数值角色：`role === 10`（平台管理员）的权限树锁定。 */
  role: number
}

/** 目录里的权限节点（与后端目录同构）。 */
export interface PermissionsCatalogNodeDef {
  id: string
  label: string
  /** 高危节点（渲染 `permissions.risk` 角标与琥珀色左边框）。 */
  risk?: boolean
}

/** 权限目录域（容器取数后注入；静态目录，不随选中对象变化）。 */
export interface PermissionsCatalogDomain {
  domain: string
  label: string
  nodes: PermissionsCatalogNodeDef[]
}

/** 展平后的节点：附带所属域，中栏据此按域分组渲染。 */
export interface PermissionsCatalogNode extends PermissionsCatalogNodeDef {
  domain: string
  domainLabel: string
}

/** 按域分组后的可见节点组（已应用搜索词过滤）。 */
export interface PermissionsCatalogGroup {
  domain: string
  domainLabel: string
  nodes: PermissionsCatalogNode[]
}

/** 选中用户的有效权限（角色模板节点 ⊕ 覆盖）。 */
export interface PermissionsUserPermissions {
  /** 当前绑定的角色模板 id；未绑定时缺省。 */
  roleId?: number
  /** 角色模板展开出的节点。 */
  nodes: string[]
  /** 用户覆盖项。 */
  overrides: PermissionOverride[]
}

/** 用户保存载荷：覆盖与可选的角色改绑。 */
export interface SaveUserPermissionsArgs {
  userId: number
  /** 本次要写入的覆盖全量集合（替换语义，与后端 PUT 一致）。 */
  overrides: PermissionOverride[]
  /** 本次要改绑的角色模板 id；null 表示角色不变。 */
  roleId: number | null
}

/** 节点点击/键盘事件里本视图关心的修饰键（Shift 连选、Ctrl/⌘ 多选）。 */
export interface PermissionsToggleEvent {
  shiftKey: boolean
  ctrlKey: boolean
  metaKey: boolean
}

/** 待保存变更：工具栏角标、右栏摘要与确认弹窗共用同一份计算。 */
interface PendingDiff {
  added: string[]
  removed: string[]
  roleChange: { from: string; to: string } | null
  overrides: PermissionOverride[]
  count: number
}

/**
 * 受控边界（ADR-097 b 范式）：**不取数、不发请求、不弹 toast、不读路由**。
 * - 目录、角色列表、用户列表与选中用户的有效权限经 props 注入（容器调 `useRbacCatalog` /
 *   `useRbacRoles` / `useUsers` / `useUserPermissions`）；
 * - **归容器的受控状态**：`selection`——它是 `useUserPermissions` 的查询键，一变就触发取数，
 *   且深链 `?user=<id>` 的解析与 URL 回写属路由层，故由容器持有并通过 `onSelectionChange` 上报；
 * - **留本组件的纯 UI 状态**：搜索词（只对静态目录做本地过滤，不触发取数）、Shift 连选范围、
 *   未保存的节点/覆盖草稿与绑定角色草稿、保存确认弹窗开合；
 * - **鉴权一律由容器注入**：`canRead` 决定是否渲染禁止态、`canManage` 决定树与覆盖是否可改；
 *   对象级锁定（`platform_admin` 角色 / `platform_admin` 用户）由本视图叠加——它取决于目录数据
 *   本身（`role.key` / `user.role`），且要与锁定提示同屏渲染，故不拆成第二个布尔 props；
 * - 保存时机沿用迁包前语义：**显式保存**（草稿攒在本地，点保存先弹确认，确认后才提交），
 *   不是勾选即存；两个保存回调返回是否成功，视图据此决定是否关窗（失败保留草稿与弹窗）。
 */
export interface PermissionsPageViewProps {
  /** 权限目录（容器 `useRbacCatalog` 的 `domains`）；缺省视为空目录。 */
  catalog?: PermissionsCatalogDomain[]
  /** 目录加载态：与 `rolesLoading` 一起决定左栏是否显示 `common.loading`。 */
  catalogLoading?: boolean
  /** 角色模板列表；容器取数后注入。 */
  roles?: PermissionsRoleRow[]
  /** 角色列表加载态。 */
  rolesLoading?: boolean
  /** 用户列表；沿用既有 `/users` 全量口径（千级用户须服务端搜索时应由容器改口径，视图不自取数）。 */
  users?: PermissionsUserRow[]
  /** 当前选中对象；受控（容器持有，见上方受控边界）。 */
  selection: PermissionSelection
  /** 选中对象变更上报（容器写入自身状态，需要时回写 URL）。 */
  onSelectionChange: (selection: PermissionSelection) => void
  /** 选中用户的有效权限；仅在选中用户且该用户权限已取回时有值（缺省表示尚未取回）。 */
  userPermissions?: PermissionsUserPermissions | null
  /** 是否允许读取本页（平台管理员或 `rbac.read`）：为 false 时渲染禁止态且不取任何数据。 */
  canRead: boolean
  /** 是否允许编辑权限（平台管理员或 `rbac.manage`）：为 false 时树只读、保存按钮禁用。 */
  canManage: boolean
  /** 保存在途：禁用保存按钮并显示 `common.saving`（沿用迁包前「按 mutation 在途」口径）。 */
  saving?: boolean
  /** 保存角色模板节点；返回是否成功——成功才关确认弹窗（失败提示由容器弹）。 */
  onSaveRolePermissions: (roleId: number, nodes: string[]) => Promise<boolean>
  /** 保存用户覆盖（覆盖写入成功后才改绑角色）；返回是否成功——成功才关确认弹窗。 */
  onSaveUserPermissions: (args: SaveUserPermissionsArgs) => Promise<boolean>
}

/** 按搜索词展平目录（命中节点 id 或节点名）。 */
function flattenCatalog(domains: PermissionsCatalogDomain[] | undefined, filter: string): PermissionsCatalogNode[] {
  const q = filter.trim().toLowerCase()
  const out: PermissionsCatalogNode[] = []
  for (const d of domains ?? []) {
    for (const n of d.nodes) {
      if (q && !n.id.toLowerCase().includes(q) && !n.label.toLowerCase().includes(q)) continue
      out.push({ id: n.id, label: n.label, risk: n.risk, domain: d.domain, domainLabel: d.label })
    }
  }
  return out
}

/** 保持目录顺序按域分组（Map 的插入序即首次出现序）。 */
function groupByDomain(nodes: PermissionsCatalogNode[]): PermissionsCatalogGroup[] {
  const map = new Map<string, PermissionsCatalogGroup>()
  for (const n of nodes) {
    const bucket = map.get(n.domain) ?? { domain: n.domain, domainLabel: n.domainLabel, nodes: [] }
    bucket.nodes.push(n)
    map.set(n.domain, bucket)
  }
  return [...map.values()]
}

/** 有效集合 = 角色模板节点 ⊕ 用户覆盖；deny 永胜（与后端算法一致）。 */
function applyOverrides(base: string[], overrides: PermissionOverride[]): Set<string> {
  const set = new Set(base)
  for (const o of overrides) if (o.effect === 'allow') set.add(o.node)
  for (const o of overrides) if (o.effect === 'deny') set.delete(o.node)
  return set
}

/**
 * 左栏：对象选择（角色模板 + 用户）。
 *
 * 本组件只吃注入数据与一个选中回调，无本地状态：列表口径（全量 / 搜索窗口）由容器决定，
 * 以免把候选集裁剪逻辑挪进包内。
 */
function PermissionsCatalogPanel({
  roles = [],
  users = [],
  rolesLoading = false,
  catalogLoading = false,
  selection,
  onSelectionChange,
}: {
  roles?: PermissionsRoleRow[]
  users?: PermissionsUserRow[]
  rolesLoading?: boolean
  catalogLoading?: boolean
  selection: PermissionSelection
  onSelectionChange: (selection: PermissionSelection) => void
}) {
  const { t } = useTranslation()

  return (
    <Panel
      className="min-h-0 overflow-hidden"
      bodyClassName="min-h-0 flex-1 overflow-y-auto p-2"
      title={t('permissions.objects')}
      icon={<UsersRound className="size-3.5" />}
    >
      <div className="mb-1 px-1 text-[11px] font-semibold uppercase tracking-wide text-muted-foreground">
        {t('permissions.roles')}
      </div>
      {(rolesLoading || catalogLoading) && <p className="px-1 text-xs text-muted-foreground">{t('common.loading')}</p>}
      {roles.map((role) => {
        const active = selection?.kind === 'role' && selection.id === role.id
        return (
          <button
            key={role.id}
            type="button"
            data-testid={`permissions-role-${role.key}`}
            onClick={() => onSelectionChange({ kind: 'role', id: role.id })}
            className={cn(
              'mb-1 w-full rounded-lg border px-2.5 py-2 text-left transition-colors',
              active
                ? 'border-primary bg-primary/12 shadow-sm'
                : 'border-border bg-card hover:border-primary/40 hover:bg-accent/40',
            )}
          >
            <div className="flex items-center gap-1.5">
              <span className={cn('truncate text-[13px]', active ? 'font-semibold text-primary' : 'font-medium')}>{role.name}</span>
              {role.key === 'platform_admin' && (
                <Badge className="bg-amber-100 text-[10px] text-amber-800">{t('permissions.superAdmin')}</Badge>
              )}
              {role.isSystem && role.key !== 'platform_admin' && (
                <Badge variant="outline" className="text-[10px]">{t('permissions.systemRole')}</Badge>
              )}
            </div>
            <div className="mt-0.5 line-clamp-2 text-[11px] leading-snug text-muted-foreground">{role.description}</div>
          </button>
        )
      })}

      <div className="mb-1 mt-3 px-1 text-[11px] font-semibold uppercase tracking-wide text-muted-foreground">
        {t('permissions.users')}
      </div>
      {users.map((u) => {
        const active = selection?.kind === 'user' && selection.id === u.id
        return (
          <button
            key={u.id}
            type="button"
            data-testid={`permissions-user-${u.id}`}
            onClick={() => onSelectionChange({ kind: 'user', id: u.id })}
            className={cn(
              'mb-1 w-full rounded-lg border px-2.5 py-2 text-left transition-colors',
              active
                ? 'border-primary bg-primary/12 shadow-sm'
                : 'border-border bg-card hover:border-primary/40 hover:bg-accent/40',
            )}
          >
            <div className={cn('truncate text-[13px]', active ? 'font-semibold text-primary' : 'font-medium')}>{u.username}</div>
            <div className="text-[11px] text-muted-foreground">{t(ROLE_LABEL_KEY[u.role] ?? 'users.roleUnknown', { role: u.role })}</div>
          </button>
        )
      })}
    </Panel>
  )
}

/**
 * 中栏：权限树编辑（按域分组的节点卡 + 工具栏）。
 *
 * 只吃「已展平/已分组/已折算成开集」的展示输入与回调：连选范围、Shift 逻辑、草稿读写都在
 * 页组件里，本组件不持有任何草稿状态，也就不可能与右栏预览的口径分叉。
 */
function PermissionsMatrixPanel({
  objectTitle,
  selectionKind,
  groups,
  checkedIds,
  rangeIds,
  overrideMap,
  filter,
  onFilterChange,
  onToggleNode,
  onSetDomainNodes,
  onInvertDomainNodes,
  onSelectAllVisible,
  onClearRange,
  onSave,
  saveDisabled,
  saving,
  pendingCount,
  locked,
}: {
  /** 中栏标题：当前对象名（未选择时为 `permissions.noSelection`）。 */
  objectTitle: string
  /** 当前选中对象种类；null 表示未选择。 */
  selectionKind: 'role' | 'user' | null
  /** 已过滤并按域分组的可见节点。 */
  groups: PermissionsCatalogGroup[]
  /** 选中对象当前「开」着的节点集合（角色 = 草稿；用户 = 覆盖 ⊕ 角色模板）。 */
  checkedIds: Set<string>
  /** Shift 连选高亮范围。 */
  rangeIds: Set<string>
  /** 用户对象下每个被改写节点的覆盖效果（仅用户态渲染角标）。 */
  overrideMap: Map<string, 'allow' | 'deny'>
  /** 搜索词（本地过滤，只影响可见节点）。 */
  filter: string
  onFilterChange: (value: string) => void
  onToggleNode: (nodeId: string, event: PermissionsToggleEvent) => void
  onSetDomainNodes: (nodes: PermissionsCatalogNode[], on: boolean) => void
  onInvertDomainNodes: (nodes: PermissionsCatalogNode[]) => void
  onSelectAllVisible: () => void
  onClearRange: () => void
  onSave: () => void
  saveDisabled: boolean
  saving: boolean
  pendingCount: number
  /** 超级管理员锁定：树与覆盖只读，顶部改显锁定说明。 */
  locked: boolean
}) {
  const { t } = useTranslation()
  const hasSelection = selectionKind !== null

  return (
    <Panel
      className="min-h-0 overflow-hidden"
      bodyClassName="min-h-0 flex-1 overflow-hidden p-0"
      title={objectTitle}
      actions={
        <div className="flex flex-wrap items-center gap-1.5">
          <Input
            data-testid="permissions-search"
            value={filter}
            onChange={(e) => onFilterChange(e.target.value)}
            placeholder={t('permissions.searchPlaceholder')}
            aria-label={t('permissions.searchPlaceholder')}
            className="h-7 w-40 text-xs"
          />
          <Button size="sm" variant="outline" className="h-7" onClick={onSelectAllVisible} disabled={!hasSelection}>
            {t('permissions.selectAllVisible')}
          </Button>
          <Button size="sm" variant="ghost" className="h-7" onClick={onClearRange}>
            {t('permissions.clearRange')}
          </Button>
          <Button
            size="sm"
            className="h-7"
            onClick={onSave}
            disabled={saveDisabled}
            data-testid="permissions-save"
          >
            {saving
              ? t('common.saving')
              : pendingCount > 0
                ? `${t('permissions.save')} (${pendingCount})`
                : t('permissions.save')}
          </Button>
        </div>
      }
    >
      <div className="flex h-full min-h-0 flex-col">
        <div className="flex-none border-b border-border/60 px-2 py-1.5">
          {locked ? (
            <p className="rounded-md border border-amber-200 bg-amber-50 px-2 py-1.5 text-[12px] font-medium text-amber-900" data-testid="permissions-super-locked">
              {t('permissions.superAdminLocked')}
            </p>
          ) : (
            <p className="text-[11px] text-muted-foreground">
              {hasSelection ? t('permissions.clickToToggle') : t('permissions.hintSelect')}
            </p>
          )}
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto px-2 py-2">
          {!hasSelection && <p className="p-3 text-sm text-muted-foreground">{t('permissions.hintSelect')}</p>}
          {hasSelection &&
            groups.map((group) => {
              const onCount = group.nodes.filter((n) => checkedIds.has(n.id)).length
              const allOn = onCount === group.nodes.length && group.nodes.length > 0
              return (
                <section
                  key={group.domain}
                  className="mb-3 overflow-hidden rounded-lg border border-border bg-card"
                  data-testid={`permissions-domain-${group.domain}`}
                >
                  <header className="flex flex-wrap items-center gap-2 border-b border-border bg-muted/60 px-3 py-2">
                    <Checkbox
                      data-testid={`permissions-domain-check-${group.domain}`}
                      // 分组勾选框是唯一没有可见标签的可聚焦控件：把域标题作为可访问名。
                      aria-label={t(`permissions.domain.${group.domain}`, group.domainLabel)}
                      checked={allOn}
                      className="size-4"
                      onCheckedChange={(v) => onSetDomainNodes(group.nodes, v === true)}
                    />
                    <div className="min-w-0 flex-1">
                      <div className="text-[14px] font-semibold leading-tight">
                        {t(`permissions.domain.${group.domain}`, group.domainLabel)}
                      </div>
                      <div className="mt-0.5 text-[12px] leading-snug text-muted-foreground">
                        {permDomainExplain(group.domain, group.domainLabel)}
                      </div>
                    </div>
                    <div className="flex items-center gap-1">
                      <Button size="sm" variant="outline" className="h-7" onClick={() => onSetDomainNodes(group.nodes, true)}>
                        {t('permissions.onAll')}
                      </Button>
                      <Button size="sm" variant="outline" className="h-7" onClick={() => onSetDomainNodes(group.nodes, false)}>
                        {t('permissions.offAll')}
                      </Button>
                      <Button
                        size="sm"
                        variant="secondary"
                        className="h-7"
                        data-testid={`permissions-domain-invert-${group.domain}`}
                        title={t('permissions.invertHint')}
                        onClick={() => onInvertDomainNodes(group.nodes)}
                      >
                        {t('permissions.invert')}
                      </Button>
                      <span className="ml-1 rounded-full bg-primary/10 px-2 py-0.5 text-[11px] font-semibold tabular-nums text-primary">
                        {onCount}/{group.nodes.length}
                      </span>
                    </div>
                  </header>
                  <div className="grid gap-2 p-2 sm:grid-cols-2 xl:grid-cols-3">
                    {group.nodes.map((node) => {
                      const on = checkedIds.has(node.id)
                      const inRange = rangeIds.has(node.id)
                      return (
                        <div
                          key={node.id}
                          role="checkbox"
                          aria-checked={on}
                          tabIndex={0}
                          data-testid={`permissions-node-${node.id}`}
                          data-on={on ? 'true' : 'false'}
                          data-range={inRange ? 'true' : 'false'}
                          onClick={(e) => onToggleNode(node.id, e)}
                          onKeyDown={(e) => {
                            if (e.key === 'Enter' || e.key === ' ') {
                              e.preventDefault()
                              onToggleNode(node.id, e)
                            }
                          }}
                          className={cn(
                            'relative flex min-h-[84px] cursor-pointer select-none rounded-lg border p-3 transition-colors',
                            inRange && 'ring-2 ring-primary/40',
                            on
                              ? 'border-primary bg-primary/15 shadow-sm'
                              : 'border-dashed border-border bg-muted/20 hover:border-primary/40 hover:bg-accent/50',
                            node.risk && (on ? 'border-l-4 border-l-amber-500' : 'border-l-4 border-l-amber-400/70'),
                          )}
                        >
                          {/* 开/关状态条 */}
                          <div
                            className={cn(
                              'absolute inset-y-0 left-0 w-1 rounded-l-lg',
                              on ? 'bg-primary' : 'bg-transparent',
                            )}
                          />
                          <div className="flex w-full items-start gap-2.5 pl-1">
                            <Checkbox checked={on} tabIndex={-1} className="mt-0.5 size-4 shrink-0 pointer-events-none" />
                            <div className="min-w-0 flex-1">
                              <div className="flex flex-wrap items-center gap-1.5">
                                <span
                                  className={cn(
                                    'text-[14px] font-bold leading-snug',
                                    on ? 'text-primary-ink text-primary' : 'text-foreground/80',
                                  )}
                                >
                                  {node.label}
                                </span>
                                <span
                                  className={cn(
                                    'rounded px-1.5 py-0.5 text-[10px] font-bold',
                                    on ? 'bg-primary text-primary-foreground' : 'bg-muted text-muted-foreground',
                                  )}
                                >
                                  {on ? t('permissions.statusOn') : t('permissions.statusOff')}
                                </span>
                                {node.risk && (
                                  <span className="rounded bg-amber-100 px-1.5 py-0.5 text-[10px] font-bold text-amber-800">
                                    {t('permissions.risk')}
                                  </span>
                                )}
                                {selectionKind === 'user' && overrideMap.has(node.id) && (
                                  <span
                                    data-testid={`permissions-override-${node.id}`}
                                    className={cn(
                                      'rounded px-1.5 py-0.5 text-[10px] font-bold',
                                      overrideMap.get(node.id) === 'allow'
                                        ? 'bg-primary/20 text-primary'
                                        : 'bg-destructive/15 text-destructive',
                                    )}
                                  >
                                    {overrideMap.get(node.id) === 'allow'
                                      ? t('permissions.overrideAllow')
                                      : t('permissions.overrideDeny')}
                                  </span>
                                )}
                              </div>
                              <div
                                className={cn(
                                  'mt-1 text-[12px] leading-snug',
                                  on ? 'text-foreground/75' : 'text-muted-foreground',
                                )}
                              >
                                {permExplain(node.id, node.label)}
                              </div>
                              <code className="mt-1 block font-mono text-[10px] text-muted-foreground/60">{node.id}</code>
                            </div>
                          </div>
                        </div>
                      )
                    })}
                  </div>
                </section>
              )
            })}
        </div>
      </div>
    </Panel>
  )
}

/**
 * 右栏：实时生效预览 + 绑定角色模板 + 变更摘要 + 覆盖清单 + 快捷键说明。
 *
 * 全是展示：绑定角色下拉只上报新值，改绑时要连带做的「按新模板回填节点草稿」留在页组件
 * （草稿是页级状态），避免同一份初值在两处各写一遍。
 */
function PermissionsEffectivePanel({
  selectionKind,
  previewCount,
  roles = [],
  currentRoleId,
  bindRoleId,
  onBindRoleChange,
  overrides,
  pendingDiff,
}: {
  /** 当前选中对象种类；null 表示未选择。 */
  selectionKind: 'role' | 'user' | null
  /** 生效节点数（角色 = 草稿节点数；用户 = 覆盖 ⊕ 模板后的有效集大小）。 */
  previewCount: number
  /** 角色模板列表（绑定下拉的候选与名称回显）。 */
  roles?: PermissionsRoleRow[]
  /** 选中用户当前绑定的角色 id（`permissions.bindRoleCurrent` 与变更摘要用）。 */
  currentRoleId?: number
  /** 绑定下拉的当前值（字符串形态，与 Radix Select 一致）；空串表示不显示选择。 */
  bindRoleId: string
  onBindRoleChange: (roleId: string) => void
  /** 待保存的用户覆盖草稿。 */
  overrides: PermissionOverride[]
  /** 待保存变更摘要。 */
  pendingDiff: PendingDiff
}) {
  const { t } = useTranslation()

  return (
    <Panel
      className="min-h-0 overflow-hidden"
      bodyClassName="min-h-0 flex-1 overflow-y-auto p-3"
      title={t('permissions.livePreview')}
    >
      <div data-testid="permissions-preview-count" className="text-3xl font-bold tabular-nums leading-none text-primary">
        {previewCount}
      </div>
      <div className="mt-1 text-[12px] text-muted-foreground">{t('permissions.effectiveNodes')}</div>

      {selectionKind === 'user' && (
        <div className="mt-4 rounded-lg border border-border bg-muted/30 p-3">
          <div className="flex items-center gap-1.5 text-[12px] font-semibold">
            <Link2 className="size-3.5 text-primary" />
            {t('permissions.bindRole')}
          </div>
          <p className="mt-1 text-[11px] leading-snug text-muted-foreground">{t('permissions.bindRoleHint')}</p>
          <div className="mt-2 text-[11px] text-muted-foreground">
            {t('permissions.bindRoleCurrent', {
              name: roles.find((r) => r.id === currentRoleId)?.name ?? '—',
            })}
          </div>
          <Select value={bindRoleId} onValueChange={onBindRoleChange}>
            <SelectTrigger data-testid="permissions-bind-role" className="mt-2 h-9 text-[13px]">
              <SelectValue placeholder={t('permissions.bindRole')} />
            </SelectTrigger>
            <SelectContent>
              {roles.map((r) => (
                <SelectItem key={r.id} value={String(r.id)}>
                  {r.name}
                  {r.isSystem ? ` · ${t('permissions.systemRole')}` : ''}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          {pendingDiff.roleChange ? (
            <div className="mt-2 rounded bg-amber-50 px-2 py-1 text-[11px] font-medium text-amber-800">
              {t('permissions.diffRoleFromTo', pendingDiff.roleChange)}
            </div>
          ) : (
            <div className="mt-2 text-[11px] text-muted-foreground">{t('permissions.bindRoleUnchanged')}</div>
          )}
        </div>
      )}

      <div className="mt-4">
        <div className="flex items-center gap-1.5 text-[12px] font-semibold">
          <Diff className="size-3.5" />
          {t('permissions.confirmSave')}
        </div>
        <div data-testid="permissions-pending-summary" className="mt-2 space-y-2 text-[12px]">
          {pendingDiff.count === 0 ? (
            <p className="text-muted-foreground">{t('permissions.noPendingChanges')}</p>
          ) : (
            <>
              {pendingDiff.added.length > 0 && (
                <div>
                  <div className="text-[11px] font-semibold text-primary">{t('permissions.diffAdded')} · {pendingDiff.added.length}</div>
                  <ul className="mt-0.5 space-y-0.5">
                    {pendingDiff.added.slice(0, 8).map((n) => (
                      <li key={n} className="truncate font-mono text-[11px] text-foreground/80">+ {n}</li>
                    ))}
                    {pendingDiff.added.length > 8 && <li className="text-[11px] text-muted-foreground">…</li>}
                  </ul>
                </div>
              )}
              {pendingDiff.removed.length > 0 && (
                <div>
                  <div className="text-[11px] font-semibold text-destructive">{t('permissions.diffRemoved')} · {pendingDiff.removed.length}</div>
                  <ul className="mt-0.5 space-y-0.5">
                    {pendingDiff.removed.slice(0, 8).map((n) => (
                      <li key={n} className="truncate font-mono text-[11px] text-foreground/80">− {n}</li>
                    ))}
                    {pendingDiff.removed.length > 8 && <li className="text-[11px] text-muted-foreground">…</li>}
                  </ul>
                </div>
              )}
            </>
          )}
        </div>
      </div>

      <div className="mt-4">
        <div className="text-[11px] font-semibold uppercase tracking-wide text-muted-foreground">
          {t('permissions.overrides')}
        </div>
        <div data-testid="permissions-override-list" className="mt-1 space-y-1">
          {selectionKind !== 'user' || overrides.length === 0 ? (
            <p className="text-[12px] text-muted-foreground">{t('permissions.noOverrides')}</p>
          ) : (
            overrides.map((o) => (
              <div
                key={o.node}
                className="flex items-center justify-between gap-2 rounded border border-border px-2 py-1 text-[11px]"
              >
                <code className="truncate font-mono">{o.node}</code>
                <span className={o.effect === 'allow' ? 'font-semibold text-primary' : 'font-semibold text-destructive'}>
                  {o.effect === 'allow' ? t('permissions.overrideAllow') : t('permissions.overrideDeny')}
                </span>
              </div>
            ))
          )}
        </div>
      </div>

      <div className="mt-4 rounded-lg border border-border bg-muted/20 p-2 text-[11px] text-muted-foreground">
        <div className="mb-1 font-semibold text-foreground">{t('permissions.shortcuts')}</div>
        <ul className="space-y-0.5">
          <li>{t('permissions.shortcutToggle')}</li>
          <li>{t('permissions.shortcutRange')}</li>
          <li>{t('permissions.shortcutMulti')}</li>
          <li>{t('permissions.shortcutSelectAll')}</li>
          <li>{t('permissions.shortcutEsc')}</li>
        </ul>
      </div>
    </Panel>
  )
}

/**
 * FR-432 权限管理三栏页：对象选择 / 权限树编辑 / 实时生效预览。
 * 树整行可点；Shift 连选、Ctrl/⌘ 多选、Ctrl/⌘+A 全选可见、Esc 清范围高亮。
 */
export function PermissionsPageView({
  catalog,
  catalogLoading = false,
  roles = [],
  rolesLoading = false,
  users = [],
  selection,
  onSelectionChange,
  userPermissions,
  canRead,
  canManage,
  saving = false,
  onSaveRolePermissions,
  onSaveUserPermissions,
}: PermissionsPageViewProps) {
  const { t } = useTranslation()

  // 纯 UI 状态：搜索词（本地过滤静态目录，不触发取数）、Shift 连选范围、确认弹窗开合。
  const [treeFilter, setTreeFilter] = useState('')
  const [rangeAnchor, setRangeAnchor] = useState<string | null>(null)
  const [rangeEnd, setRangeEnd] = useState<string | null>(null)
  const [confirmOpen, setConfirmOpen] = useState(false)

  const selectedUserId = selection?.kind === 'user' ? selection.id : null
  const selectedRoleId = selection?.kind === 'role' ? selection.id : null
  const roleTarget = useMemo(() => roles.find((r) => r.id === selectedRoleId) ?? null, [roles, selectedRoleId])
  const userTarget = useMemo(() => users.find((u) => u.id === selectedUserId) ?? null, [users, selectedUserId])

  // 树草稿随选择对象 remount 初始化（props 变化时调整 state 的官方模式）：
  // editorKey 同时覆盖「换了对象」与「同一对象的基线变了」（模板 updatedAt / 用户有效权限口径）。
  const editorKey = selection
    ? `${selection.kind}:${selection.id}:${roleTarget?.updatedAt ?? ''}:${userPermissions ? 'u' + userPermissions.roleId + ':' + userPermissions.nodes.length + ':' + userPermissions.overrides.length : 'pend'}`
    : 'none'
  const editorInitialNodes = useMemo(() => {
    if (selection?.kind === 'role' && roleTarget) return new Set(roleTarget.nodes)
    if (selection?.kind === 'user' && userPermissions) return new Set(userPermissions.nodes)
    return new Set<string>()
  }, [selection, roleTarget, userPermissions])
  const editorInitialOverrides = useMemo(() => {
    if (selection?.kind === 'user' && userPermissions) return userPermissions.overrides.map((o) => ({ ...o }))
    return [] as PermissionOverride[]
  }, [selection, userPermissions])
  const editorInitialBindRoleId = useMemo(() => {
    if (selection?.kind === 'user' && userPermissions?.roleId != null) return String(userPermissions.roleId)
    return ''
  }, [selection, userPermissions])

  const [draftNodes, setDraftNodes] = useState<Set<string>>(new Set())
  const [draftOverrides, setDraftOverrides] = useState<PermissionOverride[]>([])
  const [bindRoleId, setBindRoleId] = useState<string>('')
  const [draftKey, setDraftKey] = useState(editorKey)
  if (draftKey !== editorKey) {
    setDraftKey(editorKey)
    setDraftNodes(editorInitialNodes)
    setDraftOverrides(editorInitialOverrides)
    setBindRoleId(editorInitialBindRoleId)
    setRangeAnchor(null)
    setRangeEnd(null)
  }

  const visibleNodes = useMemo(() => flattenCatalog(catalog, treeFilter), [catalog, treeFilter])
  const domainGroups = useMemo(() => groupByDomain(visibleNodes), [visibleNodes])
  const flatVisibleIds = useMemo(() => visibleNodes.map((n) => n.id), [visibleNodes])

  const rangeIds = useMemo(() => {
    if (!rangeAnchor || !rangeEnd) return new Set<string>()
    const a = flatVisibleIds.indexOf(rangeAnchor)
    const b = flatVisibleIds.indexOf(rangeEnd)
    if (a < 0 || b < 0) return new Set<string>()
    const [lo, hi] = a <= b ? [a, b] : [b, a]
    return new Set(flatVisibleIds.slice(lo, hi + 1))
  }, [rangeAnchor, rangeEnd, flatVisibleIds])

  const effectiveForUser = useMemo(() => {
    if (selection?.kind !== 'user' || !userPermissions) return null
    return applyOverrides(
      roles.find((r) => r.id === userPermissions.roleId)?.nodes ?? [],
      draftOverrides.length ? draftOverrides : userPermissions.overrides,
    )
  }, [selection, userPermissions, roles, draftOverrides])

  const overrideMap = useMemo(() => {
    const m = new Map<string, 'allow' | 'deny'>()
    for (const o of draftOverrides) m.set(o.node, o.effect)
    return m
  }, [draftOverrides])

  // 节点的「开」态折算一次传给中栏：角色看草稿节点、用户看覆盖 ⊕ 模板（deny 永胜）。
  const checkedIds = useMemo(() => {
    if (selection?.kind === 'role') return draftNodes
    const set = new Set<string>()
    for (const n of visibleNodes) {
      if (overrideMap.has(n.id)) {
        if (overrideMap.get(n.id) === 'allow') set.add(n.id)
      } else if (effectiveForUser?.has(n.id)) {
        set.add(n.id)
      }
    }
    return set
  }, [selection, draftNodes, visibleNodes, overrideMap, effectiveForUser])

  const toggleNode = useCallback(
    (nodeId: string, event: PermissionsToggleEvent) => {
      if (!selection) return
      if (selection.kind === 'role') {
        setDraftNodes((prev) => {
          const next = new Set(prev)
          if (event.shiftKey && rangeAnchor) {
            const a = flatVisibleIds.indexOf(rangeAnchor)
            const b = flatVisibleIds.indexOf(nodeId)
            if (a >= 0 && b >= 0) {
              const [lo, hi] = a <= b ? [a, b] : [b, a]
              for (const id of flatVisibleIds.slice(lo, hi + 1)) next.add(id)
            } else if (next.has(nodeId)) next.delete(nodeId)
            else next.add(nodeId)
          } else if (next.has(nodeId)) {
            next.delete(nodeId)
          } else {
            next.add(nodeId)
          }
          return next
        })
        setRangeAnchor(nodeId)
        setRangeEnd(nodeId)
        return
      }
      // 用户：勒选写 allow/deny 覆盖
      setDraftOverrides((prev) => {
        const base = effectiveForUser ?? new Set<string>()
        const currentlyOn = base.has(nodeId)
        const existing = prev.find((o) => o.node === nodeId)
        const next = prev.filter((o) => o.node !== nodeId)
        if (event.shiftKey && rangeAnchor) {
          const a = flatVisibleIds.indexOf(rangeAnchor)
          const b = flatVisibleIds.indexOf(nodeId)
          if (a >= 0 && b >= 0) {
            const [lo, hi] = a <= b ? [a, b] : [b, a]
            for (const id of flatVisibleIds.slice(lo, hi + 1)) {
              const on = base.has(id)
              const filtered = next.filter((o) => o.node !== id)
              if (on) filtered.push({ node: id, effect: 'deny' })
              else filtered.push({ node: id, effect: 'allow' })
              next.length = 0
              next.push(...filtered)
            }
            return next
          }
        }
        if (existing) {
          if (existing.effect === 'allow') next.push({ node: nodeId, effect: 'deny' })
          else next.push({ node: nodeId, effect: 'allow' })
        } else {
          next.push({ node: nodeId, effect: currentlyOn ? 'deny' : 'allow' })
        }
        return next
      })
      setRangeAnchor(nodeId)
      setRangeEnd(nodeId)
    },
    [selection, rangeAnchor, flatVisibleIds, effectiveForUser],
  )

  const selectAllVisible = useCallback(() => {
    if (!selection) return
    if (selection.kind === 'role') {
      setDraftNodes((prev) => new Set([...prev, ...flatVisibleIds]))
    } else {
      setDraftOverrides((prev) => {
        const map = new Map(prev.map((o) => [o.node, o]))
        for (const id of flatVisibleIds) map.set(id, { node: id, effect: 'allow' })
        return [...map.values()]
      })
    }
  }, [selection, flatVisibleIds])

  const clearRange = useCallback(() => {
    setRangeAnchor(null)
    setRangeEnd(null)
  }, [])

  const invertDomainNodes = useCallback(
    (domainNodes: PermissionsCatalogNode[]) => {
      if (!selection) return
      const ids = domainNodes.map((n) => n.id)
      if (selection.kind === 'role') {
        setDraftNodes((prev) => {
          const next = new Set(prev)
          for (const id of ids) {
            if (next.has(id)) next.delete(id)
            else next.add(id)
          }
          return next
        })
      } else {
        const base = effectiveForUser ?? new Set<string>()
        setDraftOverrides((prev) => {
          const map = new Map(prev.map((o) => [o.node, o]))
          for (const id of ids) {
            const on = map.has(id) ? map.get(id)!.effect === 'allow' : base.has(id)
            map.set(id, { node: id, effect: on ? 'deny' : 'allow' })
          }
          return [...map.values()]
        })
      }
    },
    [selection, effectiveForUser],
  )

  const setDomainNodes = useCallback(
    (domainNodes: PermissionsCatalogNode[], on: boolean) => {
      if (!selection) return
      const ids = domainNodes.map((n) => n.id)
      if (selection.kind === 'role') {
        setDraftNodes((prev) => {
          const next = new Set(prev)
          for (const id of ids) {
            if (on) next.add(id)
            else next.delete(id)
          }
          return next
        })
      } else {
        setDraftOverrides((prev) => {
          const map = new Map(prev.map((o) => [o.node, o]))
          for (const id of ids) map.set(id, { node: id, effect: on ? 'allow' : 'deny' })
          return [...map.values()]
        })
      }
    },
    [selection],
  )

  const handleKeyDown = useCallback(
    (event: React.KeyboardEvent) => {
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'a') {
        event.preventDefault()
        selectAllVisible()
      } else if (event.key === 'Escape') {
        clearRange()
      }
    },
    [selectAllVisible, clearRange],
  )

  // 改绑角色模板时连带把节点草稿换成新模板的节点（迁包前语义：绑定即预览新模板）。
  const handleBindRoleChange = useCallback(
    (roleId: string) => {
      setBindRoleId(roleId)
      const role = roles.find((r) => String(r.id) === roleId)
      if (role) setDraftNodes(new Set(role.nodes))
    },
    [roles],
  )

  const previewCount = selection?.kind === 'role' ? draftNodes.size : effectiveForUser?.size ?? draftNodes.size

  const superAdminLocked =
    (selection?.kind === 'role' && roleTarget?.key === 'platform_admin') ||
    (selection?.kind === 'user' && userTarget?.role === 10)

  // 可编辑 = 容器注入的权限门禁 ∧ 非超级管理员对象（对象级锁定由本视图判定，见受控边界）。
  const canEdit = Boolean(selection) && canManage && !superAdminLocked

  /** 待保存变更（用于工具栏角标 + 确认弹窗）。 */
  const pendingDiff = useMemo(() => {
    if (!selection) {
      return { added: [] as string[], removed: [] as string[], roleChange: null as null | { from: string; to: string }, overrides: [] as PermissionOverride[], count: 0 }
    }
    if (selection.kind === 'role') {
      const base = new Set(roleTarget?.nodes ?? [])
      const added = [...draftNodes].filter((n) => !base.has(n)).sort()
      const removed = [...base].filter((n) => !draftNodes.has(n)).sort()
      return { added, removed, roleChange: null, overrides: [], count: added.length + removed.length }
    }
    const added: string[] = []
    const removed: string[] = []
    const baseEffective = new Set(userPermissions?.nodes ?? [])
    const nextEffective = effectiveForUser ?? new Set<string>()
    for (const n of nextEffective) if (!baseEffective.has(n)) added.push(n)
    for (const n of baseEffective) if (!nextEffective.has(n)) removed.push(n)
    const roleChange =
      bindRoleId !== '' && userPermissions && Number(bindRoleId) !== userPermissions.roleId
        ? {
            from: roles.find((r) => r.id === userPermissions.roleId)?.name ?? String(userPermissions.roleId ?? '—'),
            to: roles.find((r) => String(r.id) === bindRoleId)?.name ?? bindRoleId,
          }
        : null
    const origOv = new Map((userPermissions?.overrides ?? []).map((o) => [o.node, o.effect]))
    const overrides = draftOverrides.filter((o) => origOv.get(o.node) !== o.effect)
    return {
      added: added.sort(),
      removed: removed.sort(),
      roleChange,
      overrides,
      count: added.length + removed.length + overrides.length + (roleChange ? 1 : 0),
    }
  }, [selection, roleTarget, draftNodes, userPermissions, effectiveForUser, bindRoleId, roles, draftOverrides])

  const applySave = useCallback(async () => {
    if (!selection) return
    if (selection.kind === 'role') {
      const ok = await onSaveRolePermissions(selection.id, [...draftNodes])
      if (ok) setConfirmOpen(false)
      return
    }
    // 角色改绑条件与变更摘要同源：绑定值非空且与当前模板不同才提交改绑。
    const reboundRoleId =
      bindRoleId !== '' && userPermissions && Number(bindRoleId) !== userPermissions.roleId
        ? Number(bindRoleId)
        : null
    const ok = await onSaveUserPermissions({ userId: selection.id, overrides: draftOverrides, roleId: reboundRoleId })
    if (ok) setConfirmOpen(false)
  }, [selection, draftNodes, draftOverrides, bindRoleId, userPermissions, onSaveRolePermissions, onSaveUserPermissions])

  const handleSaveClick = useCallback(() => {
    if (!selection || pendingDiff.count === 0) return
    setConfirmOpen(true)
  }, [selection, pendingDiff.count])

  const saveDisabled = !canEdit || pendingDiff.count === 0 || saving
  const objectTitle =
    selection?.kind === 'role'
      ? roleTarget?.name ?? t('permissions.roles')
      : selection?.kind === 'user'
        ? userTarget?.username ?? t('permissions.users')
        : t('permissions.noSelection')

  if (!canRead) {
    return (
      // 全量对齐：无权限态是普通内容页，套标准骨架。
      <PageShell data-page="permissions">
        <PageHeader title={t('permissions.title')} />
        <Panel>
          <p className="text-sm text-muted-foreground">{t('permissions.forbidden')}</p>
        </Panel>
      </PageShell>
    )
  }

  return (
    // 全量对齐：正常态是**三栏工作区**（角色/用户列表 → 权限项 → 生效预览），
    // 顶栏承载「标题 + 当前对象 + 变更角标」，属工作区顶栏而非页面级 PageHeader
    // （它随选中对象变化，不是静态页名）。故用 tool 变体：全占满、无外层留白，
    // 滚动收口到各栏自身——与节点页那类工作区型页面同处理。
    <PageShell
      variant="tool"
      data-page="permissions"
      onKeyDown={handleKeyDown}
    >
      {/* 紧凑顶栏：标题 + 当前对象 + 变更角标；保存在中栏工具区 */}
      <div className="flex flex-none items-center gap-2 border-b border-border bg-card/80 px-3 py-2">
        <h1 className="text-[15px] font-semibold tracking-tight">{t('permissions.title')}</h1>
        <span className="text-muted-foreground">/</span>
        <span className="truncate text-[13px] font-medium">{objectTitle}</span>
        {pendingDiff.count > 0 ? (
          <Badge className="bg-amber-100 text-[11px] text-amber-800" data-testid="permissions-pending">
            {t('permissions.pendingChanges', { count: pendingDiff.count })}
          </Badge>
        ) : selection ? (
          <Badge variant="outline" className="text-[11px] text-muted-foreground">
            {t('permissions.noPendingChanges')}
          </Badge>
        ) : null}
        <div className="grow" />
        <span className="hidden text-[11px] text-muted-foreground md:inline">{t('permissions.clickToToggle')}</span>
      </div>

      <div className="grid min-h-0 flex-1 gap-2 overflow-hidden p-2 lg:grid-cols-[230px_minmax(0,1fr)_280px]">
        <PermissionsCatalogPanel
          roles={roles}
          users={users}
          rolesLoading={rolesLoading}
          catalogLoading={catalogLoading}
          selection={selection}
          onSelectionChange={onSelectionChange}
        />

        <PermissionsMatrixPanel
          objectTitle={objectTitle}
          selectionKind={selection?.kind ?? null}
          groups={domainGroups}
          checkedIds={checkedIds}
          rangeIds={rangeIds}
          overrideMap={overrideMap}
          filter={treeFilter}
          onFilterChange={setTreeFilter}
          onToggleNode={toggleNode}
          onSetDomainNodes={setDomainNodes}
          onInvertDomainNodes={invertDomainNodes}
          onSelectAllVisible={selectAllVisible}
          onClearRange={clearRange}
          onSave={handleSaveClick}
          saveDisabled={saveDisabled}
          saving={saving}
          pendingCount={pendingDiff.count}
          locked={superAdminLocked}
        />

        <PermissionsEffectivePanel
          selectionKind={selection?.kind ?? null}
          previewCount={previewCount}
          roles={roles}
          currentRoleId={userPermissions?.roleId}
          bindRoleId={bindRoleId}
          onBindRoleChange={handleBindRoleChange}
          overrides={draftOverrides}
          pendingDiff={pendingDiff}
        />
      </div>

      {/* 保存确认：展示变更列表；确认后由容器提交，成功才关窗（失败保留草稿与弹窗）。 */}
      <Dialog open={confirmOpen} onOpenChange={setConfirmOpen}>
        <DialogContent data-testid="permissions-confirm-dialog" className="max-w-lg">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <CheckCircle2 className="size-4 text-primary" />
              {t('permissions.confirmSave')}
            </DialogTitle>
            <DialogDescription>{t('permissions.confirmSaveDesc')}</DialogDescription>
          </DialogHeader>
          <div className="max-h-72 space-y-3 overflow-y-auto text-[13px]">
            {pendingDiff.roleChange && (
              <div className="rounded-md border border-amber-200 bg-amber-50 p-2">
                <div className="text-[12px] font-semibold text-amber-900">{t('permissions.diffRoleChange')}</div>
                <div className="mt-0.5 text-[12px]">{t('permissions.diffRoleFromTo', pendingDiff.roleChange)}</div>
              </div>
            )}
            {pendingDiff.added.length > 0 && (
              <div>
                <div className="text-[12px] font-semibold text-primary">{t('permissions.diffAdded')} · {pendingDiff.added.length}</div>
                <ul className="mt-1 max-h-28 overflow-y-auto rounded border border-border p-2 font-mono text-[11px]">
                  {pendingDiff.added.map((n) => (
                    <li key={n}>+ {n}</li>
                  ))}
                </ul>
              </div>
            )}
            {pendingDiff.removed.length > 0 && (
              <div>
                <div className="text-[12px] font-semibold text-destructive">{t('permissions.diffRemoved')} · {pendingDiff.removed.length}</div>
                <ul className="mt-1 max-h-28 overflow-y-auto rounded border border-border p-2 font-mono text-[11px]">
                  {pendingDiff.removed.map((n) => (
                    <li key={n}>− {n}</li>
                  ))}
                </ul>
              </div>
            )}
            {pendingDiff.overrides.length > 0 && (
              <div>
                <div className="text-[12px] font-semibold">{t('permissions.diffOverrides')}</div>
                <ul className="mt-1 rounded border border-border p-2 text-[11px]">
                  {pendingDiff.overrides.map((o) => (
                    <li key={o.node}>
                      {o.effect === 'allow' ? '+' : '−'} {o.node}（{o.effect === 'allow' ? t('permissions.overrideAllow') : t('permissions.overrideDeny')}）
                    </li>
                  ))}
                </ul>
              </div>
            )}
            {pendingDiff.count === 0 && <p className="text-muted-foreground">{t('permissions.noPendingChanges')}</p>}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setConfirmOpen(false)}>
              {t('common.cancel')}
            </Button>
            <Button
              onClick={applySave}
              disabled={pendingDiff.count === 0 || saving}
              data-testid="permissions-confirm-save"
            >
              {saving ? t('common.saving') : t('permissions.confirmApply')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </PageShell>
  )
}

export default PermissionsPageView

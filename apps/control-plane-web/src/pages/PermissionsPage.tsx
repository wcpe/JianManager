import { useCallback, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Shield, UsersRound, Link2, Diff, CheckCircle2 } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { Panel } from '@jianmanager/ui/components/panel'
import { Badge } from '@jianmanager/ui/components/badge'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@jianmanager/ui/components/select'
import { cn } from '@jianmanager/ui'
import { useUsers } from '@/api/users'
import {
  useRbacCatalog,
  useRbacRoles,
  useSetRolePermissions,
  useSetUserOverrides,
  useSetUserRole,
  useUserPermissions,
  type PermDomain,
  type UserPermissionOverride,
} from '@/api/rbac'
import { permDomainExplain, permExplain } from '@/lib/permission-explain'
import { usePermissionsStore } from '@/stores/permissions'
import { ROLE_LABEL_KEY } from '@/lib/roles'

type Selection =
  | { kind: 'role'; id: number }
  | { kind: 'user'; id: number }
  | null

interface FlatNode {
  id: string
  label: string
  risk?: boolean
  domain: string
  domainLabel: string
}

function flattenCatalog(domains: PermDomain[] | undefined, filter: string): FlatNode[] {
  const q = filter.trim().toLowerCase()
  const out: FlatNode[] = []
  for (const d of domains ?? []) {
    for (const n of d.nodes) {
      if (q && !n.id.toLowerCase().includes(q) && !n.label.toLowerCase().includes(q)) continue
      out.push({ id: n.id, label: n.label, risk: n.risk, domain: d.domain, domainLabel: d.label })
    }
  }
  return out
}

function groupByDomain(nodes: FlatNode[]): { domain: string; domainLabel: string; nodes: FlatNode[] }[] {
  const map = new Map<string, { domain: string; domainLabel: string; nodes: FlatNode[] }>()
  for (const n of nodes) {
    const bucket = map.get(n.domain) ?? { domain: n.domain, domainLabel: n.domainLabel, nodes: [] }
    bucket.nodes.push(n)
    map.set(n.domain, bucket)
  }
  return [...map.values()]
}

/** 有效集合 = 角色模板节点 ⊕ 用户覆盖；deny 永胜（与后端算法一致）。 */
function applyOverrides(base: string[], overrides: UserPermissionOverride[]): Set<string> {
  const set = new Set(base)
  for (const o of overrides) if (o.effect === 'allow') set.add(o.node)
  for (const o of overrides) if (o.effect === 'deny') set.delete(o.node)
  return set
}

/**
 * FR-432 权限管理三栏页：对象选择 / 权限树编辑 / 实时生效预览。
 * 树整行可点；Shift 连选、Ctrl/⌘ 多选、Ctrl/⌘+A 全选可见、Esc 清范围高亮。
 */
export default function PermissionsPage() {
  const { t } = useTranslation()
  const hasPerm = usePermissionsStore((s) => s.hasPerm)
  const viewerIsPlatformAdmin = usePermissionsStore((s) => s.isPlatformAdmin)

  // 深链初始选择：useState 初始化读 URL
  const [selection, setSelection] = useState<Selection>(() => {
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
  })
  const [treeFilter, setTreeFilter] = useState('')
  const [rangeAnchor, setRangeAnchor] = useState<string | null>(null)
  const [rangeEnd, setRangeEnd] = useState<string | null>(null)

  const { data: catalog, isLoading: catalogLoading } = useRbacCatalog()
  const { data: roles = [], isLoading: rolesLoading } = useRbacRoles()
  const { data: users = [] } = useUsers()

  const selectedUserId = selection?.kind === 'user' ? selection.id : null
  const selectedRoleId = selection?.kind === 'role' ? selection.id : null

  const { data: userPerms } = useUserPermissions(selectedUserId, selection?.kind === 'user')
  const setRolePerms = useSetRolePermissions()
  const setUserOverrides = useSetUserOverrides()
  const setUserRole = useSetUserRole()

  const roleTarget = useMemo(() => roles.find((r) => r.id === selectedRoleId) ?? null, [roles, selectedRoleId])
  const userTarget = useMemo(() => users.find((u) => u.id === selectedUserId) ?? null, [users, selectedUserId])

  // 树草稿随选择对象 remount 初始化
  const editorKey = selection
    ? `${selection.kind}:${selection.id}:${roleTarget?.updatedAt ?? ''}:${userPerms ? 'u'+userPerms.roleId + ':' + userPerms.nodes.length + ':' + userPerms.overrides.length : 'pend'}`
    : 'none'
  const editorInitialNodes = useMemo(() => {
    if (selection?.kind === 'role' && roleTarget) return new Set(roleTarget.nodes)
    if (selection?.kind === 'user' && userPerms) return new Set(userPerms.nodes)
    return new Set<string>()
  }, [selection, roleTarget, userPerms])
  const editorInitialOverrides = useMemo(() => {
    if (selection?.kind === 'user' && userPerms) return userPerms.overrides.map((o) => ({ ...o }))
    return [] as UserPermissionOverride[]
  }, [selection, userPerms])
  const editorInitialBindRoleId = useMemo(() => {
    if (selection?.kind === 'user' && userPerms?.roleId != null) return String(userPerms.roleId)
    return ''
  }, [selection, userPerms])

  const [draftNodes, setDraftNodes] = useState<Set<string>>(new Set())
  const [draftOverrides, setDraftOverrides] = useState<UserPermissionOverride[]>([])
  const [bindRoleId, setBindRoleId] = useState<string>('')
  // 官方「props 变化时调整 state」模式
  const [draftKey, setDraftKey] = useState(editorKey)
  if (draftKey !== editorKey) {
    setDraftKey(editorKey)
    setDraftNodes(editorInitialNodes)
    setDraftOverrides(editorInitialOverrides)
    setBindRoleId(editorInitialBindRoleId)
    setRangeAnchor(null)
    setRangeEnd(null)
  }

  const visibleNodes = useMemo(() => flattenCatalog(catalog?.domains, treeFilter), [catalog, treeFilter])
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
    if (selection?.kind !== 'user' || !userPerms) return null
    return applyOverrides(
      roles.find((r) => r.id === userPerms.roleId)?.nodes ?? [],
      draftOverrides.length ? draftOverrides : userPerms.overrides,
    )
  }, [selection, userPerms, roles, draftOverrides])

  const overrideMap = useMemo(() => {
    const m = new Map<string, 'allow' | 'deny'>()
    for (const o of draftOverrides) m.set(o.node, o.effect)
    return m
  }, [draftOverrides])

  const toggleNode = useCallback(
    (nodeId: string, event: { shiftKey: boolean; ctrlKey: boolean; metaKey: boolean }) => {
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
    (domainNodes: FlatNode[]) => {
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
    (domainNodes: FlatNode[], on: boolean) => {
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

  const previewCount = selection?.kind === 'role' ? draftNodes.size : effectiveForUser?.size ?? draftNodes.size
  const canEdit =
    Boolean(selection) &&
    (viewerIsPlatformAdmin || hasPerm('rbac.manage')) &&
    // 超级管理员 / 平台管理员用户：树与覆盖均锁定
    !(selection?.kind === 'role' && selection.id && roleTarget?.key === 'platform_admin') &&
    !(selection?.kind === 'user' && userTarget?.role === 10)

  const superAdminLocked =
    (selection?.kind === 'role' && roleTarget?.key === 'platform_admin') ||
    (selection?.kind === 'user' && userTarget?.role === 10)

  /** 待保存变更（用于工具栏角标 + 确认弹窗）。 */
  const pendingDiff = useMemo(() => {
    if (!selection) return { added: [] as string[], removed: [] as string[], roleChange: null as null | { from: string; to: string }, overrides: [] as UserPermissionOverride[], count: 0 }
    if (selection.kind === 'role') {
      const base = new Set(roleTarget?.nodes ?? [])
      const added = [...draftNodes].filter((n) => !base.has(n)).sort()
      const removed = [...base].filter((n) => !draftNodes.has(n)).sort()
      return { added, removed, roleChange: null, overrides: [], count: added.length + removed.length }
    }
    const added: string[] = []
    const removed: string[] = []
    const baseEffective = new Set(userPerms?.nodes ?? [])
    const nextEffective = effectiveForUser ?? new Set<string>()
    for (const n of nextEffective) if (!baseEffective.has(n)) added.push(n)
    for (const n of baseEffective) if (!nextEffective.has(n)) removed.push(n)
    const roleChange =
      bindRoleId && userPerms && Number(bindRoleId) !== userPerms.roleId
        ? {
            from: roles.find((r) => r.id === userPerms.roleId)?.name ?? String(userPerms.roleId ?? '—'),
            to: roles.find((r) => String(r.id) === bindRoleId)?.name ?? bindRoleId,
          }
        : null
    const origOv = new Map((userPerms?.overrides ?? []).map((o) => [o.node, o.effect]))
    const overrides = draftOverrides.filter((o) => origOv.get(o.node) !== o.effect)
    return {
      added: added.sort(),
      removed: removed.sort(),
      roleChange,
      overrides,
      count: added.length + removed.length + overrides.length + (roleChange ? 1 : 0),
    }
  }, [selection, roleTarget, draftNodes, userPerms, effectiveForUser, bindRoleId, roles, draftOverrides])

  const [confirmOpen, setConfirmOpen] = useState(false)

  const applySave = useCallback(() => {
    if (!selection) return
    if (selection.kind === 'role') {
      setRolePerms.mutate(
        { id: selection.id, nodes: [...draftNodes] },
        {
          onSuccess: () => {
            toast.success(t('permissions.saved'))
            setConfirmOpen(false)
          },
          onError: () => toast.error(t('common.error')),
        },
      )
    } else {
      setUserOverrides.mutate(
        { userId: selection.id, overrides: draftOverrides },
        {
          onSuccess: () => {
            if (bindRoleId && userPerms && Number(bindRoleId) !== userPerms.roleId) {
              setUserRole.mutate({ userId: selection.id, roleId: Number(bindRoleId) })
            }
            toast.success(t('permissions.saved'))
            setConfirmOpen(false)
          },
          onError: () => toast.error(t('common.error')),
        },
      )
    }
  }, [selection, draftNodes, draftOverrides, bindRoleId, userPerms, setRolePerms, setUserOverrides, setUserRole, t])

  const handleSaveClick = useCallback(() => {
    if (!selection || pendingDiff.count === 0) return
    setConfirmOpen(true)
  }, [selection, pendingDiff.count])

  const saveDisabled = !canEdit || pendingDiff.count === 0 || setRolePerms.isPending || setUserOverrides.isPending
  const objectTitle =
    selection?.kind === 'role'
      ? roleTarget?.name ?? t('permissions.roles')
      : selection?.kind === 'user'
        ? userTarget?.username ?? t('permissions.users')
        : t('permissions.noSelection')

  if (!viewerIsPlatformAdmin && !hasPerm('rbac.read')) {
    return (
      <div data-page="permissions" className="jm-page-stack">
        <Panel title={t('permissions.title')} icon={<Shield className="size-3.5" />}>
          <p className="text-sm text-muted-foreground">{t('permissions.forbidden')}</p>
        </Panel>
      </div>
    )
  }

  return (
    <div
      data-page="permissions"
      className="flex h-full min-h-0 w-full flex-col overflow-hidden"
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
        {/* 左栏 */}
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
                onClick={() => setSelection({ kind: 'role', id: role.id })}
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
                onClick={() => setSelection({ kind: 'user', id: u.id })}
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

        {/* 中栏：唯一滚动区 + 工具栏（搜索/保存） */}
        <Panel
          className="min-h-0 overflow-hidden"
          bodyClassName="min-h-0 flex-1 overflow-hidden p-0"
          title={objectTitle}
          actions={
            <div className="flex flex-wrap items-center gap-1.5">
              <Input
                data-testid="permissions-search"
                value={treeFilter}
                onChange={(e) => setTreeFilter(e.target.value)}
                placeholder={t('permissions.searchPlaceholder')}
                className="h-7 w-40 text-xs"
              />
              <Button size="sm" variant="outline" className="h-7" onClick={selectAllVisible} disabled={!selection}>
                {t('permissions.selectAllVisible')}
              </Button>
              <Button size="sm" variant="ghost" className="h-7" onClick={clearRange}>
                {t('permissions.clearRange')}
              </Button>
              <Button
                size="sm"
                className="h-7"
                onClick={handleSaveClick}
                disabled={saveDisabled}
                data-testid="permissions-save"
              >
                {setRolePerms.isPending || setUserOverrides.isPending
                  ? t('common.saving')
                  : pendingDiff.count > 0
                    ? `${t('permissions.save')} (${pendingDiff.count})`
                    : t('permissions.save')}
              </Button>
            </div>
          }
        >
          <div className="flex h-full min-h-0 flex-col">
            <div className="flex-none border-b border-border/60 px-2 py-1.5">
              {superAdminLocked ? (
                <p className="rounded-md border border-amber-200 bg-amber-50 px-2 py-1.5 text-[12px] font-medium text-amber-900" data-testid="permissions-super-locked">
                  {t('permissions.superAdminLocked')}
                </p>
              ) : (
                <p className="text-[11px] text-muted-foreground">
                  {selection ? t('permissions.clickToToggle') : t('permissions.hintSelect')}
                </p>
              )}
            </div>
            <div className="min-h-0 flex-1 overflow-y-auto px-2 py-2">
              {!selection && <p className="p-3 text-sm text-muted-foreground">{t('permissions.hintSelect')}</p>}
              {selection &&
                domainGroups.map((group) => {
                  const isOn = (id: string) =>
                    selection.kind === 'role'
                      ? draftNodes.has(id)
                      : overrideMap.has(id)
                        ? overrideMap.get(id) === 'allow'
                        : Boolean(effectiveForUser?.has(id))
                  const onCount = group.nodes.filter((n) => isOn(n.id)).length
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
                          checked={allOn}
                          className="size-4"
                          onCheckedChange={(v) => setDomainNodes(group.nodes, v === true)}
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
                          <Button size="sm" variant="outline" className="h-7" onClick={() => setDomainNodes(group.nodes, true)}>
                            {t('permissions.onAll')}
                          </Button>
                          <Button size="sm" variant="outline" className="h-7" onClick={() => setDomainNodes(group.nodes, false)}>
                            {t('permissions.offAll')}
                          </Button>
                          <Button
                            size="sm"
                            variant="secondary"
                            className="h-7"
                            data-testid={`permissions-domain-invert-${group.domain}`}
                            title={t('permissions.invertHint')}
                            onClick={() => invertDomainNodes(group.nodes)}
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
                          const on = isOn(node.id)
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
                              onClick={(e) => toggleNode(node.id, e)}
                              onKeyDown={(e) => {
                                if (e.key === 'Enter' || e.key === ' ') {
                                  e.preventDefault()
                                  toggleNode(node.id, e)
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
                                    {selection.kind === 'user' && overrideMap.has(node.id) && (
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

        {/* 右栏：生效 + 绑定角色 + 变更摘要 */}
        <Panel
          className="min-h-0 overflow-hidden"
          bodyClassName="min-h-0 flex-1 overflow-y-auto p-3"
          title={t('permissions.livePreview')}
        >
          <div data-testid="permissions-preview-count" className="text-3xl font-bold tabular-nums leading-none text-primary">
            {previewCount}
          </div>
          <div className="mt-1 text-[12px] text-muted-foreground">{t('permissions.effectiveNodes')}</div>

          {selection?.kind === 'user' && (
            <div className="mt-4 rounded-lg border border-border bg-muted/30 p-3">
              <div className="flex items-center gap-1.5 text-[12px] font-semibold">
                <Link2 className="size-3.5 text-primary" />
                {t('permissions.bindRole')}
              </div>
              <p className="mt-1 text-[11px] leading-snug text-muted-foreground">{t('permissions.bindRoleHint')}</p>
              <div className="mt-2 text-[11px] text-muted-foreground">
                {t('permissions.bindRoleCurrent', {
                  name: roles.find((r) => r.id === userPerms?.roleId)?.name ?? '—',
                })}
              </div>
              <Select
                value={bindRoleId}
                onValueChange={(v) => {
                  setBindRoleId(v)
                  const role = roles.find((r) => String(r.id) === v)
                  if (role) setDraftNodes(new Set(role.nodes))
                }}
              >
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
              {selection?.kind !== 'user' || draftOverrides.length === 0 ? (
                <p className="text-[12px] text-muted-foreground">{t('permissions.noOverrides')}</p>
              ) : (
                draftOverrides.map((o) => (
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
      </div>

      {/* 保存确认：展示变更列表 */}
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
              disabled={pendingDiff.count === 0 || setRolePerms.isPending || setUserOverrides.isPending}
              data-testid="permissions-confirm-save"
            >
              {setRolePerms.isPending || setUserOverrides.isPending ? t('common.saving') : t('permissions.confirmApply')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

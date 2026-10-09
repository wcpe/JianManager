/**
 * @file NetworksPageView：群组（Network 软标签）管理页的受控视图（列表 / 拓扑双视图 + 新建弹窗 + 详情成员管理），
 *       取数、写动作、toast 与路由由应用容器负责。
 * @input lib/topology（memberHealth / memberHealthFromStatus / MemberStatusCounts / MemberHealth）、
 *        lib/threshold（instanceStatusLevel / statusColorVar）、lib/form-validation（validateRequired）、
 *        lib/use-field-gate（useFieldGate）、lib/virtual-list（useVirtualRows）、lib/utils（cn）、
 *        views/DangerConfirm（删除群组 / 移除成员的二次确认）、Panel/Dialog/StatusBadge/Checkbox 等原语、翻译上下文
 * @output NetworksPageView、NetworksPageViewProps、NetworkView、NetworkSummaryView、NetworkMemberView、
 *         NetworkDetailView、NetworkCandidateView、NetworkNodeRef、NetworkDetailPanelView、
 *         NetworkDetailPanelViewProps、NetworkInstancePickerView、NetworkInstancePickerViewProps、
 *         NetworksInstancePickerArgs
 * @sync apps/control-plane-web/src/pages/NetworksPage.tsx（容器 + 拓扑 / 实例候选两个插槽的实现）、
 *        apps/control-plane-web/src/components/console/TopologyGraph.tsx（拓扑接线层，经 renderTopology 注入）、
 *        apps/control-plane-web/src/pages/NetworksPage.dom.test.tsx、NetworksPage.fr032.dom.test.tsx
 * @since FR-502（组件受控化迁包；原页 FR-032 群组关系模型、FR-145 可寻址双栏拓扑、FR-335 概要健康分布）
 */
import { useEffect, useMemo, useState, type FormEvent, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { GitBranch, List, Network } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { FieldError, FieldLabel } from '@jianmanager/ui/components/field-label'
import { Input } from '@jianmanager/ui/components/input'
import { ListSkeleton, PageHeader, PageShell } from '@jianmanager/ui/components/layout'
import { Panel } from '@jianmanager/ui/components/panel'
import { ScrollableDialogBody, scrollableDialogContentClass } from '@jianmanager/ui/components/scrollable-dialog'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import DangerConfirm from '@/components/views/DangerConfirm'
import { validateRequired } from '@jianmanager/ui/lib/form-validation'
import { instanceStatusLevel, statusColorVar } from '@jianmanager/ui/lib/threshold'
import {
  memberHealth,
  memberHealthFromStatus,
  type MemberHealth,
  type MemberStatusCounts,
} from '@jianmanager/ui/lib/topology'
import { useFieldGate } from '@jianmanager/ui/lib/use-field-gate'
import { cn } from '@jianmanager/ui/lib/utils'
import { useVirtualRows } from '@jianmanager/ui/lib/virtual-list'

/** 页面双视图：'list' 列表（成员健康分布）/ 'topology' 拓扑（proxy↔backend 注册关系）。 */
export type NetworkView = 'list' | 'topology'

/**
 * 列表项的最小字段集。
 *
 * 刻意只声明本视图用到的字段，而非照搬应用侧 `@/api/networks` 的 `NetworkSummary`：
 * 两者字段同形、结构互通，容器直接传 API 返回的完整对象也兼容，无需把该 API 类型搬进包
 * （同 `ScheduleLogRow`、`AlertEventFilter` 的取舍）。
 */
export interface NetworkSummaryView {
  id: number
  name: string
  description?: string
  memberCount: number
  /** 成员健康计数桶（FR-335）：列表行免详情请求直接渲染健康分布。 */
  memberStatus: MemberStatusCounts
}

/** 成员行的最小字段集（`instanceId` 作增删主键，`status` 经 instanceStatusLevel 归色）。 */
export interface NetworkMemberView {
  instanceId: number
  name: string
  role: string
  status: string
}

/** 详情（含成员）的最小字段集：本视图只用 id 无关的名称与成员列表（描述在列表行已展示）。 */
export interface NetworkDetailView {
  id: number
  name: string
  members: NetworkMemberView[]
}

/** 实例候选行的最小字段集（容器传 `useInstanceSearch` 的结果即结构兼容）。 */
export interface NetworkCandidateView {
  id: number
  name: string
  status: string
  nodeId: number
  /** 未标注角色时按 universal 展示（与成员行同一口径）。 */
  role?: string
  serverPort?: number
}

/** 节点最小投影：候选行显示所属节点名。 */
export interface NetworkNodeRef {
  id: number
  name: string
}

/**
 * 实例候选选择器插槽参数：本视图只声明「候选区需要什么、勾选怎么回传」，
 * 「何时发请求、防抖多久、一次取多少条」是外壳策略。
 *
 * 千级实例（大档 1200）必须走服务端搜索，不得一次拉全量再本地过滤——故候选窗口与总数
 * 不在这里，由外壳的实现自带（与 `SchedulesInstancePickerArgs` 同款取舍）。
 * 本插槽仅在详情面板挂载时被调用，故不需要 `enabled`：面板不在即不请求。
 */
export interface NetworksInstancePickerArgs {
  /** 已勾选的实例 id（多选：一次可加入多个）。 */
  selected: number[]
  /** 勾选变更上报。 */
  onToggle: (id: number, on: boolean) => void
  /** 已在组内的实例 id：候选需排除（成员数远小于实例数，客户端过滤代价可忽略）。 */
  memberIds: number[]
  /** 提交已勾选成员。本视图已把「成功才清空勾选」收在内部，故实现只需把它接到提交按钮上。 */
  onAdd: () => void
  /** 提交在途：禁用提交按钮。 */
  adding: boolean
}

/** 实例运行状态 → i18n 文案键（复用实例页既有键，FR-160 统一 StatusBadge）。 */
const STATUS_LABEL: Record<string, string> = {
  RUNNING: 'instances.running',
  STOPPED: 'instances.stopped',
  STARTING: 'instances.starting',
  STOPPING: 'instances.stopping',
  CRASHED: 'instances.crashed',
}

/**
 * 成员候选行的固定行高（px）。行盒用 `h-9` 钉死，行内 `text-sm`（20px）与 Checkbox（16px）
 * 都不超过它，未加任何行间距（间距已折进行高，理由见候选列表处的注释）。
 * 虚拟化按 `index × 该值` 定位，必须与真实渲染高度严格一致，否则滚动会累积漂移。
 */
const CAND_ROW_HEIGHT = 36

/** 视图切换按钮：工具栏内的分段控件（`aria-pressed` 标注点亮态）。 */
function ViewTab({
  active,
  onClick,
  icon,
  children,
}: {
  active: boolean
  onClick: () => void
  icon: ReactNode
  children: ReactNode
}) {
  return (
    <Button
      type="button"
      size="xs"
      variant={active ? 'default' : 'ghost'}
      onClick={onClick}
      aria-pressed={active}
      className={cn('h-7 px-2.5', active ? 'shadow-soft' : 'text-muted-foreground hover:text-foreground')}
    >
      {icon}
      {children}
    </Button>
  )
}

/** 群组列表：卡片化行 + 成员健康分布（FR-335：直接读概要内联的 memberStatus 计数，零详情请求）。 */
function NetworkListView({
  networks,
  onView,
  onDelete,
}: {
  networks: NetworkSummaryView[]
  onView: (id: number) => void
  onDelete: (network: NetworkSummaryView) => void
}) {
  const { t } = useTranslation()

  if (networks.length === 0) {
    return <p className="text-muted-foreground text-center py-8">{t('networks.empty')}</p>
  }

  return (
    <div className="space-y-2.5">
      {networks.map((n) => {
        const health = memberHealthFromStatus(n.memberStatus)
        return (
          <Panel key={n.id} hoverable className="px-0" bodyClassName="px-4 py-3">
            <div className="flex items-start justify-between gap-3">
              <button onClick={() => onView(n.id)} className="min-w-0 text-left group">
                <div className="flex items-center gap-2">
                  <span className="font-semibold group-hover:text-primary transition-colors">{n.name}</span>
                  <span className="text-xs text-muted-foreground">
                    {t('networks.memberCount', { count: n.memberCount })}
                  </span>
                </div>
                {n.description && <p className="mt-0.5 truncate text-sm text-muted-foreground">{n.description}</p>}
              </button>
              <div className="flex shrink-0 items-center gap-3">
                <Button size="xs" variant="ghost" className="text-primary hover:text-primary" onClick={() => onView(n.id)}>
                  {t('networks.manage')}
                </Button>
                <Button size="xs" variant="ghost" className="text-status-danger hover:text-status-danger" onClick={() => onDelete(n)}>
                  {t('common.delete')}
                </Button>
              </div>
            </div>
            <div className="mt-2.5">
              <NetworkHealthDistribution health={health} loading={false} />
            </div>
          </Panel>
        )
      })}
    </div>
  )
}

/** 成员健康分布条（运行/过渡/崩溃/停止分段着色 + 计数摘要）。 */
function NetworkHealthDistribution({ health, loading }: { health: MemberHealth | null; loading: boolean }) {
  const { t } = useTranslation()
  if (loading || !health) {
    return <div className="h-1.5 w-full animate-pulse rounded-full bg-muted" />
  }
  if (health.total === 0) {
    return <p className="text-xs text-muted-foreground">{t('networks.noMembers')}</p>
  }
  const segs: { value: number; className: string; label: string }[] = [
    { value: health.running, className: 'bg-status-success', label: t('networks.healthRunning') },
    { value: health.transitioning, className: 'bg-status-warning', label: t('networks.healthTransitioning') },
    { value: health.crashed, className: 'bg-status-danger', label: t('networks.healthCrashed') },
    { value: health.stopped, className: 'bg-muted-foreground/40', label: t('networks.healthStopped') },
  ]
  return (
    <div>
      <div className="flex h-1.5 w-full overflow-hidden rounded-full bg-muted">
        {segs.map((s, i) =>
          s.value > 0 ? (
            <div
              key={i}
              className={s.className}
              style={{ width: `${(s.value / health.total) * 100}%` }}
              title={`${s.label}: ${s.value}`}
            />
          ) : null,
        )}
      </div>
      <div className="mt-1 flex flex-wrap gap-x-3 gap-y-0.5 text-[11px] text-muted-foreground">
        {segs
          .filter((s) => s.value > 0)
          .map((s, i) => (
            <span key={i} className="inline-flex items-center gap-1">
              <span className={cn('size-1.5 rounded-full', s.className)} />
              {s.label} {s.value}
            </span>
          ))}
      </div>
    </div>
  )
}

/** 拓扑图例：状态色 + 启用/禁用连线说明。 */
function NetworkTopologyLegend() {
  const { t } = useTranslation()
  const items = [
    { className: 'bg-status-success', label: t('networks.healthRunning') },
    { className: 'bg-status-warning', label: t('networks.healthTransitioning') },
    { className: 'bg-status-danger', label: t('networks.healthCrashed') },
    { className: 'bg-muted-foreground/50', label: t('networks.topoDisabled') },
  ]
  return (
    <div className="mt-3 flex flex-wrap items-center gap-x-4 gap-y-1 border-t pt-3 text-[11px] text-muted-foreground">
      {items.map((it, i) => (
        <span key={i} className="inline-flex items-center gap-1.5">
          <span className={cn('size-2 rounded-full', it.className)} />
          {it.label}
        </span>
      ))}
    </div>
  )
}

/** 新建群组弹窗的受控契约：`open` 由本页持有，`creating` 由外壳决定，草稿与校验留本组件。 */
interface CreateNetworkModalViewProps {
  /** 创建在途：禁用提交按钮并显示创建中文案。 */
  creating?: boolean
  /** 关闭/取消回调（提交成功后本组件也会调用它）。 */
  onClose: () => void
  /** 提交新建；返回是否成功——成功才关窗（失败保留草稿，便于修正后重试）。 */
  onSubmit: (input: { name: string; description?: string }) => Promise<boolean>
}

/** 新建群组弹窗（名称必填 + 描述可选，FR-032）。 */
function CreateNetworkModalView({ creating = false, onClose, onSubmit }: CreateNetworkModalViewProps) {
  const { t } = useTranslation()
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const gate = useFieldGate()

  const nameError = validateRequired(name)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    gate.submit()
    if (nameError) return
    // 失败（false，容器已弹 toast）时不关窗、不清草稿，便于修正后重试。
    const ok = await onSubmit({ name, description: description || undefined })
    if (ok) onClose()
  }

  return (
    <Dialog open onOpenChange={(next) => { if (!next) onClose() }}>
      <DialogContent className={`${scrollableDialogContentClass} sm:max-w-md`}>
        <DialogHeader>
          <DialogTitle>{t('networks.create')}</DialogTitle>
        </DialogHeader>
        <form onSubmit={submit} className="flex min-h-0 flex-1 flex-col">
          <ScrollableDialogBody className="space-y-3">
            <div>
              <FieldLabel required>{t('networks.name')}</FieldLabel>
              <Input
                value={name}
                onChange={(e) => setName(e.target.value)}
                onBlur={() => gate.touch('name')}
                className="mt-1"
                placeholder="survival"
                aria-invalid={!!gate.show('name', nameError)}
              />
              <FieldError error={gate.show('name', nameError)} />
            </div>
            <div>
              <FieldLabel>{t('networks.description')}</FieldLabel>
              <Input
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                className="mt-1"
              />
            </div>
          </ScrollableDialogBody>
          <DialogFooter className="pt-4">
            <Button type="button" variant="outline" onClick={onClose}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" disabled={creating || !!nameError}>
              {creating ? t('common.creating') : t('common.create')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/**
 * 实例候选选择器（多选）：虚拟化勾选列表 + 服务端搜索。
 *
 * 【为什么不是 `InstancePicker`】后者是单选 Combobox，而本页要「勾选多个 → 加入所选 N」的批量语义，
 * 单选无法表达。两者共享同一取舍：候选一律走服务端搜索（千级实例不得一次拉全量），
 * 键入经 `onQueryChange` 上报，防抖与请求都在外壳——那是「何时发请求」这一应用侧策略。
 *
 * 候选窗口已由服务端按名排序并截断，本组件只做两件本地事：排除已入组成员、按 draft 关键字上报。
 */
export interface NetworkInstancePickerViewProps {
  /** 候选窗口（外壳取数注入）；缺省按空候选渲染。 */
  items?: NetworkCandidateView[]
  /** 候选总数（服务端返回），用于截断提示。 */
  total?: number
  /** 已勾选的实例 id（受控：勾选与「加入所选」计数同源，故由面板持有）。 */
  selected: number[]
  /** 勾选变更上报。 */
  onToggle: (id: number, on: boolean) => void
  /** 已入组实例 id：候选需排除（成员数远小于实例数，代价可忽略）。 */
  memberIds?: number[]
  /** 节点列表：候选行显示所属节点名，缺省回退 `#id`。 */
  nodes?: NetworkNodeRef[]
  /** 键入关键字上报（外壳做防抖后下发服务端 q）。 */
  onQueryChange: (keyword: string) => void
  /** 提交已勾选成员（底部提交栏）。 */
  onAdd: () => void
  /** 提交在途：禁用提交栏。 */
  adding?: boolean
}

/** 实例候选选择器：右栏「添加成员」区（标题 + 筛选框 + 虚拟化候选列表 + 底部提交栏）。 */
export function NetworkInstancePickerView({
  items,
  total,
  selected,
  onToggle,
  memberIds,
  nodes,
  onQueryChange,
  onAdd,
  adding = false,
}: NetworkInstancePickerViewProps) {
  const { t } = useTranslation()
  const [filter, setFilter] = useState('')

  const roleLabel = (role: string) => t(`networks.role_${role}`, { defaultValue: role })

  const nodeName = useMemo(() => {
    const m = new Map<number, string>()
    for (const n of nodes ?? []) m.set(n.id, n.name)
    return (id: number) => m.get(id) ?? `#${id}`
  }, [nodes])

  // 已入组成员不进候选（服务端只按 q 过滤，不认成员关系）。
  const list = useMemo(() => {
    const excluded = new Set(memberIds ?? [])
    return (items ?? []).filter((i) => !excluded.has(i.id))
  }, [items, memberIds])

  /** 服务端截断时提示引导继续键入缩小范围（按**服务端窗口**判定，与是否排除成员无关）。 */
  const truncated = total !== undefined && total > (items?.length ?? 0)

  // 已选态改 Set：候选行按 selected.includes(id) 判断，1200 行 × O(已选数) 会随勾选累积成热点。
  const selectedSet = useMemo(() => new Set(selected), [selected])

  /**
   * 候选列表虚拟化。实例数是千级（大档 1200），此前候选面板把整表铺进 DOM。
   *
   * 【为什么去掉原先的 space-y-1】它给相邻行加 4px 间距，而这 4px 不计入行高：
   * 第 N 行的真实偏移是 N×行高 + 4×(N−1)。等距虚拟化按 index×itemSize 定位，
   * 千行时累计偏差 4×999 ≈ 4000px，滚到底会明显漂移。故把间距折进行盒（h-9 = 36px）。
   */
  const { containerRef: candScrollRef, onScroll: onCandScroll, range: candRange, totalSize: candTotalSize } =
    useVirtualRows({ total: list.length, itemSize: CAND_ROW_HEIGHT, overscan: 8 })

  // 过滤收窄后回到顶部：候选骤短时，上一轮的 scrollOffset 会让窗口落在列表之外（空窗一帧）。
  useEffect(() => {
    if (candScrollRef.current) candScrollRef.current.scrollTop = 0
  }, [filter, candScrollRef])

  return (
    <div className="flex min-h-0 flex-col bg-card">
      <div className="flex shrink-0 items-center justify-between gap-2 px-4 pt-3 pb-2">
        <span className="text-xs font-semibold tracking-wide text-muted-foreground">
          {t('networks.addMembers')}
        </span>
        <Input
          value={filter}
          onChange={(e) => {
            setFilter(e.target.value)
            // 关键字原样上报：防抖与请求由外壳负责（千级实例必须走服务端搜索）。
            onQueryChange(e.target.value)
          }}
          placeholder={t('networks.filterCandidates')}
          className="h-7 w-36 text-xs"
        />
      </div>
      {truncated && (
        <p className="shrink-0 px-4 pb-1 text-[11px] text-muted-foreground">
          {t('common.searchTruncated', { shown: list.length, total: total ?? 0 })}
        </p>
      )}
      <div ref={candScrollRef} onScroll={onCandScroll} className="min-h-0 flex-1 overflow-y-auto px-2">
        {list.length === 0 ? (
          <p className="px-2 py-6 text-center text-xs text-muted-foreground">{t('networks.noCandidates')}</p>
        ) : (
          // 虚拟化：外层撑起总高，内层按 range.before 平移，只渲染窗口内的行。
          <div className="relative" style={{ height: candTotalSize }}>
            <ul style={{ transform: `translateY(${candRange.before}px)` }}>
              {list.slice(candRange.start, candRange.end).map((i) => {
                const on = selectedSet.has(i.id)
                return (
                  <li key={i.id} className="h-9">
                    <label
                      className={cn(
                        'flex h-full cursor-pointer items-center gap-2.5 rounded-lg px-2.5 transition-colors',
                        on ? 'bg-primary/10' : 'hover:bg-accent/60',
                      )}
                    >
                      {/* aria-label 落在 Checkbox（可聚焦元素）上，而非外层行容器。 */}
                      <Checkbox checked={on} onCheckedChange={(v) => onToggle(i.id, v === true)} aria-label={i.name} />
                      <span
                        className="size-1.5 shrink-0 rounded-full"
                        style={{ backgroundColor: statusColorVar(instanceStatusLevel(i.status)) }}
                      />
                      <span className="min-w-0 flex-1 truncate text-sm">{i.name}</span>
                      <span className="shrink-0 text-[11px] text-muted-foreground">
                        {roleLabel(i.role || 'universal')}
                        {' · '}
                        {nodeName(i.nodeId)}
                        {i.serverPort ? ` · :${i.serverPort}` : ''}
                      </span>
                    </label>
                  </li>
                )
              })}
            </ul>
          </div>
        )}
      </div>
      {/* 提交栏属于「候选」这一列（勾选数与按钮同源），故留在本组件而非详情面板。 */}
      <div className="flex shrink-0 justify-end border-t p-3">
        <Button
          type="button"
          size="xs"
          onClick={onAdd}
          disabled={selected.length === 0 || adding}
        >
          {t('networks.addSelected', { count: selected.length })}
        </Button>
      </div>
    </div>
  )
}

/** 详情面板的注入契约：详情数据与全部写动作经 props 注入，候选选择器经插槽注入。 */
export interface NetworkDetailPanelViewProps {
  /** 详情（容器按 detailId 取数注入）；未加载时缺省，成员区为空列表。 */
  detail?: NetworkDetailView
  /** 添加成员在途：禁用「加入所选」。 */
  adding?: boolean
  /** 移除成员在途：危险确认弹窗的确认按钮显示旋转。 */
  removing?: boolean
  /** 批量启停在途：禁用两个批量按钮。 */
  batchPending?: boolean
  /** 关闭详情面板（容器清理深链参数，故本组件不自行导航）。 */
  onClose: () => void
  /** 加入所选成员；返回是否成功——成功才清空已选（失败保留，便于重试）。 */
  onAddMembers: (instanceIds: number[]) => Promise<boolean>
  /** 移除成员；本组件在 Promise 落定后收起二次确认（与迁移前 onSettled 语义一致）。 */
  onRemoveMember: (instanceId: number) => Promise<void>
  /** 群组成员批量启停上报（容器执行 mutation 并决定提示文案）。 */
  onBatchAction: (action: 'start' | 'stop') => void
  /** 实例候选选择器插槽（千级实例须服务端搜索，实现由外壳注入）。 */
  renderInstancePicker: (args: NetworksInstancePickerArgs) => ReactNode
}

/**
 * 群组详情：可寻址双栏（左成员 / 右候选），消除原嵌套滚动模态（FR-145）。
 * 成员用 StatusBadge；候选含节点·状态·端口并可筛。
 *
 * 受控边界：`selected`（待加入的勾选集合）与 `removeTarget`（二次确认目标）是本组件内的纯 UI 状态；
 * 详情数据、写动作与候选取数全在外壳。弹窗开合由 `detailId`（容器按深链派生）决定，故本体不持 open。
 */
export function NetworkDetailPanelView({
  detail,
  adding = false,
  removing = false,
  batchPending = false,
  onClose,
  onAddMembers,
  onRemoveMember,
  onBatchAction,
  renderInstancePicker,
}: NetworkDetailPanelViewProps) {
  const { t } = useTranslation()
  const [selected, setSelected] = useState<number[]>([])
  const [removeTarget, setRemoveTarget] = useState<{ instanceId: number; name: string } | null>(null)

  const memberIds = useMemo(() => (detail?.members ?? []).map((m) => m.instanceId), [detail])

  const roleLabel = (role: string) => t(`networks.role_${role}`, { defaultValue: role })
  const statusLabel = (s: string) => (STATUS_LABEL[s] ? t(STATUS_LABEL[s]) : s)
  const health = detail ? memberHealth(detail.members) : null

  const toggleSel = (id: number, on: boolean) =>
    setSelected((prev) => (on ? [...prev, id] : prev.filter((x) => x !== id)))

  const addSelected = async () => {
    if (selected.length === 0) return
    const ok = await onAddMembers(selected)
    if (ok) setSelected([])
  }

  // 成员移除是可逆软操作（可重新加入），但会改变群组拓扑，故加二次确认。
  const confirmRemove = async () => {
    if (!removeTarget) return
    const target = removeTarget
    try {
      await onRemoveMember(target.instanceId)
    } finally {
      // 落定后收起确认框（成功/失败一致，失败时列表不变，用户可重试）。
      setRemoveTarget(null)
    }
  }

  return (
    <>
    <Dialog open onOpenChange={(next) => { if (!next) onClose() }}>
      <DialogContent
        className="flex max-h-[88vh] w-full max-w-4xl flex-col gap-0 overflow-hidden p-0"
        showCloseButton={false}
      >
        {/* 头部 */}
        <div className="flex shrink-0 items-center justify-between gap-3 border-b px-5 py-3.5">
          <div className="min-w-0">
            <DialogTitle className="truncate text-lg font-bold">{detail?.name}</DialogTitle>
            {health && (
              <p className="mt-0.5 text-xs text-muted-foreground">
                {t('networks.memberCount', { count: health.total })}
                {health.total > 0 && (
                  <>
                    {' · '}
                    <span className="text-status-success">{t('networks.healthRunning')} {health.running}</span>
                    {health.crashed > 0 && (
                      <span className="text-status-danger"> · {t('networks.healthCrashed')} {health.crashed}</span>
                    )}
                  </>
                )}
              </p>
            )}
          </div>
          <div className="flex shrink-0 items-center gap-2">
            <Button
              type="button"
              size="xs"
              variant="outline"
              onClick={() => onBatchAction('start')}
              disabled={batchPending}
            >
              {t('networks.batchStart')}
            </Button>
            <Button
              type="button"
              size="xs"
              variant="outline"
              onClick={() => onBatchAction('stop')}
              disabled={batchPending}
            >
              {t('networks.batchStop')}
            </Button>
            <Button type="button" size="xs" variant="ghost" onClick={onClose} className="text-muted-foreground">
              {t('common.close')}
            </Button>
          </div>
        </div>

        {/* 双栏：左成员 / 右候选 */}
        <div className="grid min-h-0 flex-1 grid-cols-1 gap-px overflow-hidden bg-border md:grid-cols-2">
          {/* 左：成员 */}
          <div className="flex min-h-0 flex-col bg-card">
            <div className="shrink-0 px-4 pt-3 pb-2 text-xs font-semibold tracking-wide text-muted-foreground">
              {t('networks.members')}
            </div>
            <div className="min-h-0 flex-1 overflow-y-auto px-2 pb-3">
              {detail && detail.members.length === 0 ? (
                <p className="px-2 py-6 text-center text-xs text-muted-foreground">{t('networks.noMembers')}</p>
              ) : (
                <ul className="space-y-1">
                  {detail?.members.map((m) => (
                    <li
                      key={m.instanceId}
                      className="flex items-center justify-between gap-2 rounded-lg px-2.5 py-2 hover:bg-accent/60"
                    >
                      <div className="flex min-w-0 items-center gap-2">
                        <StatusBadge
                          level={instanceStatusLevel(m.status)}
                          label={statusLabel(m.status)}
                          pulse={m.status === 'STARTING' || m.status === 'STOPPING'}
                        />
                        <span className="truncate text-sm font-medium">{m.name}</span>
                        <span className="shrink-0 text-[11px] text-muted-foreground">{roleLabel(m.role)}</span>
                      </div>
                      <Button
                        type="button"
                        size="xs"
                        variant="ghost"
                        className="shrink-0 text-status-danger hover:text-status-danger"
                        onClick={() => setRemoveTarget({ instanceId: m.instanceId, name: m.name })}
                      >
                        {t('networks.removeMember')}
                      </Button>
                    </li>
                  ))}
                </ul>
              )}
            </div>
          </div>

          {/* 右：候选（整列经插槽注入 —— 选择器实现自带服务端搜索；本组件只传勾选态、排除名单与提交动作） */}
          {renderInstancePicker({ selected, onToggle: toggleSel, memberIds, onAdd: () => void addSelected(), adding })}
        </div>
      </DialogContent>
    </Dialog>
      <DangerConfirm
        open={removeTarget !== null}
        title={t('networks.removeMemberConfirm', { name: removeTarget?.name ?? '' })}
        description={t('networks.removeMemberDesc')}
        confirmLabel={t('networks.removeMember')}
        pending={removing}
        onConfirm={confirmRemove}
        onCancel={() => setRemoveTarget(null)}
      />
    </>
  )
}

/**
 * 群组页的注入契约（ADR-097 a+b 范式）：**不取数、不发请求、不弹 toast、不读路由**。
 *
 * 受控边界：
 * - 数据经 props 注入（容器调 `useNetworks` / `useNetwork`）；
 * - 写动作以回调上报，mutation 与成功/失败文案由容器决定：`onCreate` 与 `onAddMembers` 回传
 *   `Promise<boolean>`，视图据返回值决定是否关窗 / 清空勾选（失败时保留草稿与勾选，便于重试）；
 *   `onRemoveMember` 回传 `Promise<void>`，视图在落定后收起二次确认（对齐原 `onSettled`）；
 * - **归容器**的受控状态：`view`（数据源切换，拓扑挂载与否）与 `detailId`（`useNetwork` 的查询键，
 *   并让详情可深链）——两者都由路由/查询参数派生，是「页面在看什么」这一导航语义；
 * - **留本视图**的纯 UI 状态：新建弹窗开合与其草稿、删除目标、详情面板内的勾选集合与移除确认目标；
 * - 两个插槽把「应用侧策略」留在外壳：`renderTopology`（拓扑接线层自行取数，包内不得引用它）、
 *   `renderInstancePicker`（候选走服务端搜索，防抖与请求属外壳）；
 * - 删除群组的二次确认 scope 固定 `platform` 且不注入 `allowed`，与迁包前逐字一致（不新增不放宽）。
 */
export interface NetworksPageViewProps {
  /** 当前视图（受控）：容器按路径 / `?view=` 派生（`/networks/topology` 亦为拓扑）。 */
  view: NetworkView
  /** 视图切换上报（容器据此导航并写查询参数，故点亮态由容器派生）。 */
  onViewChange: (view: NetworkView) => void
  /** 群组列表；容器取数后注入（缺省或空数组渲染空态）。 */
  networks?: NetworkSummaryView[]
  /** 列表加载态（渲染骨架）。 */
  isLoading?: boolean
  /** 当前打开的详情群组 id（深链 `?network=` 的取值）；null 表示不渲染详情面板。 */
  detailId: number | null
  /** 详情数据（容器按 `detailId` 取数注入）。 */
  detail?: NetworkDetailView
  /** 打开某群组详情（容器写查询参数，使详情可寻址）。 */
  onOpenDetail: (id: number) => void
  /** 关闭详情面板（容器清理查询参数）。 */
  onCloseDetail: () => void
  /** 创建在途：禁用新建弹窗的提交按钮。 */
  creating?: boolean
  /** 提交新建；返回是否成功——成功才关窗。 */
  onCreate: (input: { name: string; description?: string }) => Promise<boolean>
  /** 删除已确认的群组（二次确认已在本视图内完成）。 */
  onDelete: (network: NetworkSummaryView) => void
  /** 添加成员在途。 */
  addingMembers?: boolean
  /** 移除成员在途。 */
  removingMember?: boolean
  /** 成员批量启停在途。 */
  batchPending?: boolean
  /** 加入所选成员；返回是否成功——成功才清空勾选。 */
  onAddMembers: (instanceIds: number[]) => Promise<boolean>
  /** 移除成员（落定后本视图收起二次确认）。 */
  onRemoveMember: (instanceId: number) => Promise<void>
  /** 群组成员批量启停上报。 */
  onBatchAction: (action: 'start' | 'stop') => void
  /**
   * 拓扑图插槽。拓扑本体与它的接线层（取数）都在应用侧，包内不得引用它；
   * 本视图只提供 `Panel` 外壳、副标题与图例。
   */
  renderTopology: () => ReactNode
  /** 实例候选选择器插槽（详情面板右栏）。 */
  renderInstancePicker: (args: NetworksInstancePickerArgs) => ReactNode
}

/**
 * 群组（Network 软标签）管理页（FR-032 / FR-145 / ADR-007）：
 * 列表（成员健康分布）/ 拓扑（proxy↔backend 注册关系）两视图，详情可深链（?network=&view=）改为可寻址双栏。
 */
export function NetworksPageView({
  view,
  onViewChange,
  networks,
  isLoading = false,
  detailId,
  detail,
  onOpenDetail,
  onCloseDetail,
  creating = false,
  onCreate,
  onDelete,
  addingMembers = false,
  removingMember = false,
  batchPending = false,
  onAddMembers,
  onRemoveMember,
  onBatchAction,
  renderTopology,
  renderInstancePicker,
}: NetworksPageViewProps) {
  const { t } = useTranslation()
  const [createOpen, setCreateOpen] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<NetworkSummaryView | null>(null)

  return (
    // 阶段 6 页面迁移：外壳与页头改用布局层原语（PageShell / PageHeader），
    // 不再手写 `jm-page-stack space-y-4` 与 `jm-page-header` 骨架类名。
    // `data-page` 由 PageShell 透传（它 spread 剩余 props），e2e 的就绪信号依赖它。
    <PageShell data-page="networks">
      <PageHeader
        title={t('networks.title')}
        description={t('networks.subtitle')}
        actions={
          <>
            <div className="jm-toolbar-surface inline-flex items-center gap-1 p-1">
              <ViewTab active={view === 'list'} onClick={() => onViewChange('list')} icon={<List className="size-3.5" />}>
                {t('networks.viewList')}
              </ViewTab>
              <ViewTab active={view === 'topology'} onClick={() => onViewChange('topology')} icon={<GitBranch className="size-3.5" />}>
                {t('networks.viewTopology')}
              </ViewTab>
            </div>
            <Button size="sm" onClick={() => setCreateOpen(true)}>
              {t('networks.create')}
            </Button>
          </>
        }
      />
      {view === 'topology' ? (
        <Panel
          title={t('networks.topoTitle')}
          icon={<Network className="size-4" />}
          bodyClassName="p-4"
        >
          <p className="mb-3 text-xs text-muted-foreground">{t('networks.topoSubtitle')}</p>
          {renderTopology()}
          <NetworkTopologyLegend />
        </Panel>
      ) : isLoading ? (
        // 裸 <p> 换成统一骨架：与其它页的数据区占位一致（阶段 6 补丁的 PageSkeleton 一族）。
        <ListSkeleton />
      ) : (
        <NetworkListView networks={networks ?? []} onView={onOpenDetail} onDelete={setDeleteTarget} />
      )}

      {createOpen && (
        <CreateNetworkModalView
          creating={creating}
          onClose={() => setCreateOpen(false)}
          onSubmit={onCreate}
        />
      )}
      {detailId !== null && (
        <NetworkDetailPanelView
          detail={detail}
          adding={addingMembers}
          removing={removingMember}
          batchPending={batchPending}
          onClose={onCloseDetail}
          onAddMembers={onAddMembers}
          onRemoveMember={onRemoveMember}
          onBatchAction={onBatchAction}
          renderInstancePicker={renderInstancePicker}
        />
      )}

      <DangerConfirm
        open={deleteTarget !== null}
        title={t('networks.deleteConfirm', { name: deleteTarget?.name ?? '' })}
        confirmLabel={t('common.delete')}
        scope="platform"
        onConfirm={() => {
          const target = deleteTarget
          setDeleteTarget(null)
          if (target) onDelete(target)
        }}
        onCancel={() => setDeleteTarget(null)}
      />
    </PageShell>
  )
}

export default NetworksPageView

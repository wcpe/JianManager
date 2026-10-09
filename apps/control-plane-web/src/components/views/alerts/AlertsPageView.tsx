/**
 * @file AlertsPageView：告警页（规则 / 事件 / 渠道三个 Tab 的宿主）的受控视图，取数、写动作与 toast 由应用容器负责。
 * @input lib/alert-contracts（AlertRuleInfo / AlertEventInfo / AlertChannelInfo）、lib/alert-helpers（级别色、静默窗口、
 *        渠道 id 解析、规则汇总）、views/config-explorer/ConfigRow（卡片行/开关/视图切换/汇总筛选条）、
 *        views/DangerConfirm（删除二次确认）、Panel/Tabs/Table/StatusBadge/EmptyState 等原语、翻译上下文
 * @output AlertsPageView、AlertsPageViewProps、AlertTab、AlertEventFilter、AlertRuleDialogArgs、AlertChannelDialogArgs、
 *         AlertRulesTabView、AlertRulesTabViewProps、AlertEventsTabView、AlertEventsTabViewProps、
 *         AlertChannelsTabView、AlertChannelsTabViewProps
 * @sync apps/control-plane-web/src/pages/AlertsPage.tsx（容器）、apps/control-plane-web/src/pages/AlertsPage.dom.test.tsx、
 *        apps/control-plane-web/src/pages/alerts/RuleDialog.tsx 与 ChannelDialog.tsx（对话框容器经插槽注入）
 * @since FR-502（组件受控化迁包；原页 FR-011 告警规则 + FR-085 通道/事件 + FR-149 事件筛选 + FR-208 mock 域簇）
 */
import { useMemo, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { AlertTriangle, Bell, Inbox, Plus } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { EmptyState } from '@jianmanager/ui/components/empty-state'
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
import { Skeleton } from '@jianmanager/ui/components/skeleton'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@jianmanager/ui/components/table'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@jianmanager/ui/components/tabs'
import DangerConfirm from '@/components/views/DangerConfirm'
import { ConfigRow, ConfigSwitch, ConfigSummaryChips, ConfigViewToggle } from '@/components/views/config-explorer/ConfigRow'
import type { ConfigView } from '@/components/views/config-explorer/ConfigRow'
import type { AlertChannelInfo, AlertEventInfo, AlertRuleInfo } from '@/lib/alerts/alert-contracts'
import { formatSilenceWindow, levelStatusLevel, parseChannelIds, summarizeRules } from '@/lib/alerts/alert-helpers'

/** 页内三个 Tab：规则 / 事件 / 渠道。 */
export type AlertTab = 'rules' | 'events' | 'channels'

const ALERT_TRIGGER_TYPES = ['metric', 'instance_crash', 'node_offline', 'log_keyword', 'player_event', 'backup_failed', 'saturation', 'baseline', 'quota_exceeded'] as const
/** Radix Select 不接受空字符串选项值，用哨兵值表达筛选栏的「全部」。 */
const FILTER_ALL = 'all'

/**
 * 事件筛选（页码从 1 起）。
 *
 * 刻意只声明本视图读写到的字段，而非照搬应用侧 `@/api/alerts` 的 `EventQuery`：
 * 两者字段同形、结构互通，容器直接传 `EventQuery` 也兼容，无需把 API 层的类型定义搬进包
 * （同 `ScheduleLogRow`、`ScheduleFilter` 的取舍）。
 */
export interface AlertEventFilter {
  ruleId?: number
  resolved?: boolean
  acknowledged?: boolean
  level?: string
  triggerType?: string
  /** 模糊匹配 message（FR-149）。 */
  keyword?: string
  /** fired_at 下界（RFC3339，FR-149）。 */
  from?: string
  /** fired_at 上界（RFC3339，FR-149）。 */
  to?: string
  page?: number
  pageSize?: number
}

/** 级别 pill：统一走 StatusBadge（severity → 状态色），不硬编码品牌色。 */
function LevelBadge({ level }: { level: string }) {
  const { t } = useTranslation()
  return <StatusBadge level={levelStatusLevel(level)} label={t(`alerts.level_${level}`, level)} dot={false} />
}

/** 规则条件展示文案：按触发类型取关键字段。 */
function ruleConditionText(r: AlertRuleInfo): string {
  switch (r.triggerType) {
    case 'metric':
      return `${r.metric} ${r.operator} ${r.threshold} (${r.durationSec}s)`
    case 'log_keyword':
      return `"${r.keyword}"`
    case 'player_event':
      return r.eventMatch || 'any'
    default:
      return '—'
  }
}

// ── 规则页 ──

/**
 * 规则对话框插槽参数：本视图只决定「何时打开、编辑哪条、关闭回调」，
 * 取数（节点/实例候选）、创建更新 mutation 与表单渲染由外壳的实现承担。
 */
export interface AlertRuleDialogArgs {
  /** 编辑目标；null 表示创建。 */
  rule: AlertRuleInfo | null
  /** 通知通道候选（规则表单要多选绑定渠道）。 */
  channels: AlertChannelInfo[]
  onClose: () => void
}

/**
 * 规则 Tab 的注入契约：规则列表由容器取数注入，启停/删除以回调上报，对话框实现经插槽注入。
 * 汇总筛选与卡片/列表视图切换只裁剪已到手的行、不触发取数，故留本组件。
 */
export interface AlertRulesTabViewProps {
  /** 规则列表（缺省或空数组渲染空态）。 */
  rules?: AlertRuleInfo[]
  /** 列表加载态（渲染骨架行）。 */
  isLoading?: boolean
  /** 规则更新在途：禁用全部启停开关（防止在途重复提交）。 */
  updating?: boolean
  /** 通知通道列表：仅用于规则的渠道 id → 名称映射。 */
  channels?: AlertChannelInfo[]
  /** 启停切换上报（容器执行 PUT）。 */
  onToggle: (rule: AlertRuleInfo) => void
  /** 删除已确认的规则（二次确认已在本组件内完成）。 */
  onDelete: (rule: AlertRuleInfo) => void
  /** 创建/编辑对话框插槽。 */
  renderRuleDialog: (args: AlertRuleDialogArgs) => ReactNode
}

/** 规则 Tab：汇总筛选条 + 卡片/列表双视图 + 行内启停/编辑/删除（FR-011）。 */
export function AlertRulesTabView({
  rules,
  isLoading = false,
  updating = false,
  channels,
  onToggle,
  onDelete,
  renderRuleDialog,
}: AlertRulesTabViewProps) {
  const { t } = useTranslation()
  const [editing, setEditing] = useState<AlertRuleInfo | null>(null)
  const [showCreate, setShowCreate] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<AlertRuleInfo | null>(null)
  const [view, setView] = useState<ConfigView>('card')
  // 汇总条筛选：'enabled' 仅启用 / 'disabled' 仅停用 / null 全部。
  const [filter, setFilter] = useState<'enabled' | 'disabled' | null>(null)

  const channelName = (id: number) => channels?.find((c) => c.id === id)?.name ?? `#${id}`
  const channelText = (r: AlertRuleInfo) =>
    parseChannelIds(r.channelIds).map(channelName).join(', ') || (r.notifyTarget ? 'webhook' : '—')
  const targetText = (r: AlertRuleInfo) =>
    r.targetId ? `${t(`alerts.${r.targetType}`, r.targetType)} #${r.targetId}` : (r.targetType === 'node' ? t('alerts.allNodes') : t('alerts.allInstances'))

  const summary = useMemo(() => summarizeRules(rules ?? []), [rules])
  const visible = useMemo(() => {
    const list = rules ?? []
    if (filter === 'enabled') return list.filter((r) => r.enabled)
    if (filter === 'disabled') return list.filter((r) => !r.enabled)
    return list
  }, [rules, filter])

  return (
    <div className="space-y-4">
      <div className="jm-toolbar-surface flex flex-wrap items-center gap-3 p-2">
        <ConfigSummaryChips
          chips={[
            { label: t('alerts.summaryAll'), value: summary.total, active: filter === null, onClick: () => setFilter(null) },
            {
              label: t('alerts.summaryEnabled'),
              value: summary.enabled,
              tone: 'success',
              active: filter === 'enabled',
              onClick: () => setFilter(filter === 'enabled' ? null : 'enabled'),
            },
            {
              label: t('alerts.summaryDisabled'),
              value: summary.total - summary.enabled,
              tone: 'neutral',
              active: filter === 'disabled',
              onClick: () => setFilter(filter === 'disabled' ? null : 'disabled'),
            },
          ]}
        />
        <div className="ml-auto flex items-center gap-2">
          <ConfigViewToggle view={view} onChange={setView} cardLabel={t('common.cardView')} listLabel={t('common.listView')} />
          <Button onClick={() => setShowCreate(true)}>
            <Plus className="size-4" /> {t('alerts.createRule')}
          </Button>
        </div>
      </div>

      {isLoading ? (
        <div className="space-y-2.5">
          {[0, 1, 2].map((i) => (
            <Skeleton key={i} className="h-16 w-full rounded-lg" />
          ))}
        </div>
      ) : visible.length === 0 ? (
        <Panel>
          <EmptyState icon={<AlertTriangle />} title={t('alerts.emptyRules')} />
        </Panel>
      ) : view === 'card' ? (
        <div className="flex flex-col gap-2.5">
          {visible.map((r) => (
            <ConfigRow
              key={r.id}
              icon={<AlertTriangle className="size-[18px]" />}
              tone={levelStatusLevel(r.level)}
              title={r.name}
              code={ruleConditionText(r)}
              subtitle={`${t(`alerts.trigger_${r.triggerType}`, r.triggerType)} · ${targetText(r)} · ${channelText(r)}`}
              meta={
                formatSilenceWindow(r.silenceStart, r.silenceEnd)
                  ? `${t('alerts.silence')} ${formatSilenceWindow(r.silenceStart, r.silenceEnd)}`
                  : undefined
              }
              trailing={
                <>
                  <LevelBadge level={r.level} />
                  <ConfigSwitch
                    checked={r.enabled}
                    disabled={updating}
                    onChange={() => onToggle(r)}
                    label={t('alerts.enabled')}
                    onLabel={t('alerts.on')}
                    offLabel={t('alerts.off')}
                  />
                  <Button variant="ghost" size="xs" onClick={() => setEditing(r)}>
                    {t('common.edit')}
                  </Button>
                  <Button
                    variant="ghost"
                    size="xs"
                    className="text-status-danger hover:text-status-danger"
                    onClick={() => setDeleteTarget(r)}
                  >
                    {t('common.delete')}
                  </Button>
                </>
              }
            />
          ))}
        </div>
      ) : (
        <Panel bodyClassName="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t('alerts.ruleName')}</TableHead>
                <TableHead>{t('alerts.triggerType')}</TableHead>
                <TableHead>{t('alerts.level')}</TableHead>
                <TableHead>{t('alerts.condition')}</TableHead>
                <TableHead>{t('alerts.channels')}</TableHead>
                <TableHead>{t('alerts.silence')}</TableHead>
                <TableHead>{t('alerts.enabled')}</TableHead>
                <TableHead className="text-right">{t('common.actions')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {visible.map((r) => (
                <TableRow key={r.id}>
                  <TableCell className="font-medium">{r.name}</TableCell>
                  <TableCell>{t(`alerts.trigger_${r.triggerType}`, r.triggerType)}</TableCell>
                  <TableCell><LevelBadge level={r.level} /></TableCell>
                  <TableCell className="font-mono text-xs text-muted-foreground">{ruleConditionText(r)}</TableCell>
                  <TableCell className="text-muted-foreground">{channelText(r)}</TableCell>
                  <TableCell className="text-muted-foreground">{formatSilenceWindow(r.silenceStart, r.silenceEnd) || '—'}</TableCell>
                  <TableCell>
                    <ConfigSwitch
                      checked={r.enabled}
                      disabled={updating}
                      onChange={() => onToggle(r)}
                      label={t('alerts.enabled')}
                      onLabel={t('alerts.on')}
                      offLabel={t('alerts.off')}
                    />
                  </TableCell>
                  <TableCell className="space-x-3 text-right whitespace-nowrap">
                    <Button variant="link" size="xs" className="h-auto p-0" onClick={() => setEditing(r)}>
                      {t('common.edit')}
                    </Button>
                    <Button variant="link" size="xs" className="h-auto p-0 text-status-danger hover:text-status-danger" onClick={() => setDeleteTarget(r)}>
                      {t('common.delete')}
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Panel>
      )}

      {(showCreate || editing) && renderRuleDialog({
        rule: editing,
        channels: channels ?? [],
        onClose: () => {
          setShowCreate(false)
          setEditing(null)
        },
      })}

      {/* 删除二次确认：scope 与原页一致（规则为组级），不额外注入 allowed（原页即不设门禁）。 */}
      <DangerConfirm
        open={!!deleteTarget}
        title={t('alerts.deleteRuleConfirm')}
        description={deleteTarget?.name}
        scope="group"
        onConfirm={() => {
          const target = deleteTarget
          setDeleteTarget(null)
          if (target) onDelete(target)
        }}
        onCancel={() => setDeleteTarget(null)}
      />
    </div>
  )
}

// ── 事件页 ──

/**
 * 事件 Tab 的注入契约：事件行与总数由容器按 `filter` 取数注入；
 * `filter`（含页码）是 `useAlertEvents` 的查询键，一变即重新取数，故归容器持有，
 * 本组件只负责把用户操作合成为「下一份筛选」（改筛选条件即回第 1 页）。
 */
export interface AlertEventsTabViewProps {
  /** 事件行（缺省或空数组渲染空态）。 */
  items?: AlertEventInfo[]
  /** 事件总数（分页脚用）。 */
  total?: number
  /** 当前筛选（受控）。 */
  filter: AlertEventFilter
  /** 筛选变更上报：本组件已在本地完成「改筛选即回第 1 页」的合成。 */
  onFilterChange: (next: AlertEventFilter) => void
  /** 规则列表：仅用于规则筛选下拉的候选。 */
  rules?: AlertRuleInfo[]
  /** 确认单条事件上报。 */
  onAcknowledge: (eventId: number) => void
  /** 全部标为已读上报。 */
  onMarkAllRead: () => void
}

/** 事件 Tab：多维筛选栏 + 事件表格 + 分页（FR-085 / FR-149）。 */
export function AlertEventsTabView({
  items,
  total = 0,
  filter,
  onFilterChange,
  rules,
  onAcknowledge,
  onMarkAllRead,
}: AlertEventsTabViewProps) {
  const { t } = useTranslation()

  const list = items ?? []
  const page = filter.page ?? 1
  const pageSize = filter.pageSize ?? 50
  const totalPages = Math.max(1, Math.ceil(total / pageSize))
  // 改筛选条件即回第 1 页；翻页用单独上报。datetime-local（本地时区）转 RFC3339 给后端（FR-149）。
  const patchFilter = (patch: Partial<AlertEventFilter>) => onFilterChange({ ...filter, ...patch, page: 1 })
  const toIso = (v: string) => (v ? new Date(v).toISOString() : undefined)

  return (
    <div className="space-y-4">
      <div className="jm-toolbar-surface flex flex-wrap items-center gap-2 p-2">
        <Input
          className="h-9 w-56"
          placeholder={t('alerts.keywordPlaceholder')}
          value={filter.keyword ?? ''}
          onChange={(e) => patchFilter({ keyword: e.target.value || undefined })}
        />
        <Select
          value={filter.level ?? FILTER_ALL}
          onValueChange={(v) => patchFilter({ level: v === FILTER_ALL ? undefined : v })}
        >
          <SelectTrigger size="sm" className="w-32">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={FILTER_ALL}>{t('alerts.allLevels')}</SelectItem>
            <SelectItem value="info">{t('alerts.level_info')}</SelectItem>
            <SelectItem value="warn">{t('alerts.level_warn')}</SelectItem>
            <SelectItem value="critical">{t('alerts.level_critical')}</SelectItem>
          </SelectContent>
        </Select>
        <Select
          value={filter.triggerType ?? FILTER_ALL}
          onValueChange={(v) => patchFilter({ triggerType: v === FILTER_ALL ? undefined : v })}
        >
          <SelectTrigger size="sm" className="w-40">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={FILTER_ALL}>{t('alerts.allTriggerTypes')}</SelectItem>
            {ALERT_TRIGGER_TYPES.map((tt) => (
              <SelectItem key={tt} value={tt}>{t(`alerts.trigger_${tt}`, tt)}</SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select
          value={filter.resolved === undefined ? FILTER_ALL : String(filter.resolved)}
          onValueChange={(v) => patchFilter({ resolved: v === FILTER_ALL ? undefined : v === 'true' })}
        >
          <SelectTrigger size="sm" className="w-32">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={FILTER_ALL}>{t('alerts.allStatus')}</SelectItem>
            <SelectItem value="false">{t('alerts.unresolved')}</SelectItem>
            <SelectItem value="true">{t('alerts.resolved')}</SelectItem>
          </SelectContent>
        </Select>
        <Select
          value={filter.acknowledged === undefined ? FILTER_ALL : String(filter.acknowledged)}
          onValueChange={(v) => patchFilter({ acknowledged: v === FILTER_ALL ? undefined : v === 'true' })}
        >
          <SelectTrigger size="sm" className="w-36">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={FILTER_ALL}>{t('alerts.allAck')}</SelectItem>
            <SelectItem value="false">{t('alerts.unacknowledged')}</SelectItem>
            <SelectItem value="true">{t('alerts.acknowledged')}</SelectItem>
          </SelectContent>
        </Select>
        <Select
          value={filter.ruleId === undefined ? FILTER_ALL : String(filter.ruleId)}
          onValueChange={(v) => patchFilter({ ruleId: v === FILTER_ALL ? undefined : Number(v) })}
        >
          <SelectTrigger size="sm" className="w-44">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={FILTER_ALL}>{t('alerts.allRules')}</SelectItem>
            {(rules ?? []).map((r) => (
              <SelectItem key={r.id} value={String(r.id)}>{r.name}</SelectItem>
            ))}
          </SelectContent>
        </Select>
        <label className="flex items-center gap-1 text-xs text-muted-foreground">
          {t('alerts.timeFrom')}
          <Input
            type="datetime-local"
            className="h-8 w-44"
            onChange={(e) => patchFilter({ from: toIso(e.target.value) })}
          />
        </label>
        <label className="flex items-center gap-1 text-xs text-muted-foreground">
          {t('alerts.timeTo')}
          <Input
            type="datetime-local"
            className="h-8 w-44"
            onChange={(e) => patchFilter({ to: toIso(e.target.value) })}
          />
        </label>
        <div className="flex-1" />
        <Button variant="outline" size="sm" onClick={onMarkAllRead}>
          {t('alerts.markAllRead')}
        </Button>
      </div>

      <Panel bodyClassName="p-0">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('alerts.firedAt')}</TableHead>
              <TableHead>{t('alerts.level')}</TableHead>
              <TableHead>{t('alerts.triggerType')}</TableHead>
              <TableHead>{t('alerts.rule')}</TableHead>
              <TableHead>{t('alerts.message')}</TableHead>
              <TableHead>{t('alerts.count')}</TableHead>
              <TableHead>{t('alerts.status')}</TableHead>
              <TableHead className="text-right">{t('common.actions')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {list.map((e) => (
              <TableRow key={e.id} className={e.read ? '' : 'bg-primary/5'}>
                <TableCell className="whitespace-nowrap">{new Date(e.firedAt).toLocaleString()}</TableCell>
                <TableCell><LevelBadge level={e.level} /></TableCell>
                <TableCell>{t(`alerts.trigger_${e.triggerType}`, e.triggerType)}</TableCell>
                <TableCell>{e.rule?.name ?? `#${e.ruleId}`}</TableCell>
                <TableCell className="max-w-[360px] truncate" title={e.message}>{e.message}</TableCell>
                <TableCell>{e.count > 1 ? `×${e.count}` : ''}</TableCell>
                <TableCell>
                  {e.resolved ? (
                    <StatusBadge level="neutral" label={t('alerts.resolved')} dot={false} />
                  ) : (
                    <StatusBadge level="warning" label={t('alerts.unresolved')} dot={false} />
                  )}
                </TableCell>
                <TableCell className="text-right whitespace-nowrap">
                  {e.acknowledged ? (
                    <span className="text-xs text-muted-foreground">{t('alerts.acknowledged')}</span>
                  ) : (
                    <Button variant="link" size="xs" className="h-auto p-0" onClick={() => onAcknowledge(e.id)}>
                      {t('alerts.acknowledge')}
                    </Button>
                  )}
                </TableCell>
              </TableRow>
            ))}
            {list.length === 0 && (
              <TableRow>
                <TableCell colSpan={8} className="p-0">
                  <EmptyState icon={<Inbox />} title={t('alerts.emptyEvents')} />
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </Panel>

      {total > 0 && (
        <div className="flex items-center justify-end gap-3 text-sm text-muted-foreground">
          <span>{t('alerts.totalEvents', { count: total })}</span>
          <Button
            variant="outline"
            size="xs"
            disabled={page <= 1}
            onClick={() => onFilterChange({ ...filter, page: page - 1 })}
          >
            {t('alerts.prevPage')}
          </Button>
          <span>{t('alerts.pageOf', { page, total: totalPages })}</span>
          <Button
            variant="outline"
            size="xs"
            disabled={page >= totalPages}
            onClick={() => onFilterChange({ ...filter, page: page + 1 })}
          >
            {t('alerts.nextPage')}
          </Button>
        </div>
      )}
    </div>
  )
}

// ── 通道页 ──

/**
 * 通道对话框插槽参数：本视图只决定开合与编辑目标，
 * 创建更新 mutation、QQ 扫码绑定弹窗与提示由外壳的实现承担。
 */
export interface AlertChannelDialogArgs {
  /** 编辑目标；null 表示创建。 */
  channel: AlertChannelInfo | null
  onClose: () => void
}

/**
 * 通道 Tab 的注入契约：通道列表由容器取数注入，测试发送/删除以回调上报，对话框实现经插槽注入。
 */
export interface AlertChannelsTabViewProps {
  /** 通道列表（缺省或空数组渲染空态）。 */
  channels?: AlertChannelInfo[]
  /** 列表加载态（渲染骨架行）。 */
  isLoading?: boolean
  /** 测试发送在途：禁用全部测试按钮。 */
  testing?: boolean
  /** 测试发送上报（容器执行发送并决定提示文案）。 */
  onTest: (channel: AlertChannelInfo) => void
  /** 删除已确认的通道（二次确认已在本组件内完成）。 */
  onDelete: (channel: AlertChannelInfo) => void
  /** 创建/编辑对话框插槽。 */
  renderChannelDialog: (args: AlertChannelDialogArgs) => ReactNode
}

/** 通道 Tab：通道卡片列表 + 测试发送/编辑/删除（FR-085 / FR-494）。 */
export function AlertChannelsTabView({
  channels,
  isLoading = false,
  testing = false,
  onTest,
  onDelete,
  renderChannelDialog,
}: AlertChannelsTabViewProps) {
  const { t } = useTranslation()
  const [editing, setEditing] = useState<AlertChannelInfo | null>(null)
  const [showCreate, setShowCreate] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<AlertChannelInfo | null>(null)

  return (
    <div className="space-y-4">
      <div className="flex justify-end">
        <Button onClick={() => setShowCreate(true)}>
          <Plus className="size-4" /> {t('alerts.createChannel')}
        </Button>
      </div>

      {isLoading ? (
        <div className="space-y-2.5">
          {[0, 1].map((i) => (
            <Skeleton key={i} className="h-16 w-full rounded-lg" />
          ))}
        </div>
      ) : (channels ?? []).length === 0 ? (
        <Panel>
          <EmptyState icon={<Bell />} title={t('alerts.emptyChannels')} />
        </Panel>
      ) : (
        <div className="flex flex-col gap-2.5">
          {(channels ?? []).map((c) => (
            <ConfigRow
              key={c.id}
              icon={<Bell className="size-[18px]" />}
              tone={c.enabled ? 'primary' : 'neutral'}
              title={c.name}
              subtitle={t(`alerts.channel_${c.type}`, c.type)}
              trailing={
                <>
                  <StatusBadge
                    level={c.enabled ? 'success' : 'neutral'}
                    label={c.enabled ? t('alerts.on') : t('alerts.off')}
                  />
                  <Button variant="ghost" size="xs" disabled={testing} onClick={() => onTest(c)}>
                    {t('alerts.testSend')}
                  </Button>
                  <Button variant="ghost" size="xs" onClick={() => setEditing(c)}>
                    {t('common.edit')}
                  </Button>
                  <Button
                    variant="ghost"
                    size="xs"
                    className="text-status-danger hover:text-status-danger"
                    onClick={() => setDeleteTarget(c)}
                  >
                    {t('common.delete')}
                  </Button>
                </>
              }
            />
          ))}
        </div>
      )}

      {(showCreate || editing) && renderChannelDialog({
        channel: editing,
        onClose: () => {
          setShowCreate(false)
          setEditing(null)
        },
      })}

      {/* 删除二次确认：scope 与原页一致（通道为平台级），不额外注入 allowed（原页即不设门禁）。 */}
      <DangerConfirm
        open={!!deleteTarget}
        title={t('alerts.deleteChannelConfirm')}
        description={deleteTarget?.name}
        scope="platform"
        onConfirm={() => {
          const target = deleteTarget
          setDeleteTarget(null)
          if (target) onDelete(target)
        }}
        onCancel={() => setDeleteTarget(null)}
      />
    </div>
  )
}

/**
 * 告警页的注入契约（ADR-097 b 范式）：**不取数、不发请求、不弹 toast**。
 *
 * 受控边界：
 * - 三块数据（规则 / 通道 / 事件）与未读计数经 props 注入，容器调用
 *   `useAlertRules` / `useAlertChannels` / `useAlertEvents` / `useUnreadAlertCount` 取数；
 * - 写动作（启停规则、删除规则/通道、测试通道、确认事件、全部已读）以回调上报，
 *   mutation 与成功/失败文案由容器决定（本页的文案在 API 层，容器只负责触发）；
 * - 事件筛选 `eventFilter` **归容器**：它是 `useAlertEvents` 的查询键，一变即重新取数，
 *   故是本页唯一上提的查询语义；页码包含在其中；
 * - **留包内**的纯 UI 状态：Tab 选中（三个数据集已全部注入，切换不触发取数）、
 *   两个对话框的开合与编辑目标、规则汇总筛选与卡片/列表视图切换、规则/通道的删除目标；
 * - 两个对话框走 `renderRuleDialog` / `renderChannelDialog` 插槽：其实现依赖应用侧的
 *   节点/实例候选与 mutation（`pages/alerts/RuleDialog.tsx`、`pages/alerts/ChannelDialog.tsx`），
 *   但「何时打开、编辑哪条」是页面语义，留在本组件。
 */
export interface AlertsPageViewProps {
  /**
   * 当前 Tab（受控）。
   *
   * 归容器而非留包内：事件列表只在事件 Tab 激活时才取数（原先挂在 Tab 内容里，Radix 未激活即不挂载），
   * 容器要用它门控查询，故它是「会触发取数的状态」。
   */
  tab: AlertTab
  /** Tab 切换上报（容器同时据此门控事件查询）。 */
  onTabChange: (tab: AlertTab) => void
  /** 事件 Tab 的未读角标数（0 或缺省不渲染角标）。 */
  unreadCount?: number
  /** 规则列表（规则 Tab 与事件 Tab 的规则筛选项共用）。 */
  rules?: AlertRuleInfo[]
  /** 规则列表加载态（规则 Tab 渲染骨架行）。 */
  rulesLoading?: boolean
  /** 规则更新在途（禁用规则启停开关）。 */
  ruleUpdating?: boolean
  /** 通知通道列表（规则 Tab 用于渠道 id → 名映射，通道 Tab 用于列表本身）。 */
  channels?: AlertChannelInfo[]
  /** 通道列表加载态（通道 Tab 渲染骨架行）。 */
  channelsLoading?: boolean
  /** 通道测试发送在途（禁用测试按钮）。 */
  channelTesting?: boolean
  /** 事件行（容器按 `eventFilter` 取数后注入）。 */
  events?: AlertEventInfo[]
  /** 事件总数（分页脚用）。 */
  eventsTotal?: number
  /** 事件筛选（含页码）；容器是唯一持有者。 */
  eventFilter: AlertEventFilter
  /** 事件筛选变更上报（已含「改筛选即回第 1 页」的合成）。 */
  onEventFilterChange: (next: AlertEventFilter) => void
  /** 规则启停上报。 */
  onToggleRule: (rule: AlertRuleInfo) => void
  /** 删除已确认的规则上报。 */
  onDeleteRule: (rule: AlertRuleInfo) => void
  /** 测试通道上报。 */
  onTestChannel: (channel: AlertChannelInfo) => void
  /** 删除已确认的通道上报。 */
  onDeleteChannel: (channel: AlertChannelInfo) => void
  /** 确认单条事件上报。 */
  onAcknowledgeEvent: (eventId: number) => void
  /** 全部标为已读上报。 */
  onMarkAllRead: () => void
  /** 规则创建/编辑对话框插槽。 */
  renderRuleDialog: (args: AlertRuleDialogArgs) => ReactNode
  /** 通道创建/编辑对话框插槽。 */
  renderChannelDialog: (args: AlertChannelDialogArgs) => ReactNode
}

/**
 * 告警页（FR-011 规则 / FR-085 通道与事件 / FR-149 事件筛选）。
 * 三个 Tab 的宿主：规则（卡片/列表 + 启停）、事件（多维筛选 + 确认）、通道（测试发送 + 扫码绑定）。
 */
export function AlertsPageView({
  tab,
  onTabChange,
  unreadCount,
  rules,
  rulesLoading = false,
  ruleUpdating = false,
  channels,
  channelsLoading = false,
  channelTesting = false,
  events,
  eventsTotal,
  eventFilter,
  onEventFilterChange,
  onToggleRule,
  onDeleteRule,
  onTestChannel,
  onDeleteChannel,
  onAcknowledgeEvent,
  onMarkAllRead,
  renderRuleDialog,
  renderChannelDialog,
}: AlertsPageViewProps) {
  const { t } = useTranslation()
  return (
    // 阶段 6 页面迁移：外壳与页头改用布局层原语。
    // 标题字号由 text-2xl 统一到布局规范的 text-xl（PageHeader 的固定字号）。
    <PageShell data-page="alerts">
      <PageHeader title={t('alerts.title')} />

      <Tabs value={tab} onValueChange={(v) => onTabChange(v as AlertTab)} className="w-full">
        <TabsList className="mb-4">
          <TabsTrigger value="rules">{t('alerts.tabRules')}</TabsTrigger>
          <TabsTrigger value="events">
            {t('alerts.tabEvents')}
            {!!unreadCount && unreadCount > 0 && (
              <span className="ml-1 inline-flex items-center justify-center rounded-full bg-destructive px-1.5 text-xs text-destructive-foreground">
                {unreadCount}
              </span>
            )}
          </TabsTrigger>
          <TabsTrigger value="channels">{t('alerts.tabChannels')}</TabsTrigger>
        </TabsList>

        <TabsContent value="rules">
          <AlertRulesTabView
            rules={rules}
            isLoading={rulesLoading}
            updating={ruleUpdating}
            channels={channels}
            onToggle={onToggleRule}
            onDelete={onDeleteRule}
            renderRuleDialog={renderRuleDialog}
          />
        </TabsContent>
        <TabsContent value="events">
          <AlertEventsTabView
            items={events}
            total={eventsTotal}
            filter={eventFilter}
            onFilterChange={onEventFilterChange}
            rules={rules}
            onAcknowledge={onAcknowledgeEvent}
            onMarkAllRead={onMarkAllRead}
          />
        </TabsContent>
        <TabsContent value="channels">
          <AlertChannelsTabView
            channels={channels}
            isLoading={channelsLoading}
            testing={channelTesting}
            onTest={onTestChannel}
            onDelete={onDeleteChannel}
            renderChannelDialog={renderChannelDialog}
          />
        </TabsContent>
      </Tabs>
    </PageShell>
  )
}

export default AlertsPageView

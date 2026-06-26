import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { AlertTriangle, Bell } from 'lucide-react'
import {
  useAlertRules,
  useAlertEvents,
  useDeleteAlertRule,
  useUpdateAlertRule,
  useAlertChannels,
  useDeleteAlertChannel,
  useTestAlertChannel,
  useAcknowledgeEvent,
  useMarkAllRead,
  useUnreadAlertCount,
  type AlertRuleInfo,
  type AlertChannelInfo,
  type EventQuery,
} from '@/api/alerts'
import DangerConfirm from '@/components/DangerConfirm'
import { Panel } from '@/components/ui/panel'
import { StatusBadge } from '@/components/ui/status-badge'
import { Button } from '@/components/ui/button'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { RuleDialog } from './alerts/RuleDialog'
import { ChannelDialog } from './alerts/ChannelDialog'
import {
  levelStatusLevel,
  formatSilenceWindow,
  parseChannelIds,
  summarizeRules,
} from './alerts/alert-helpers'
import {
  ConfigRow,
  ConfigSwitch,
  ConfigViewToggle,
  ConfigSummaryChips,
  type ConfigView,
} from './config-row'

type Tab = 'rules' | 'events' | 'channels'

export default function AlertsPage() {
  const { t } = useTranslation()
  const [tab, setTab] = useState<Tab>('rules')
  const { data: unread } = useUnreadAlertCount()

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold">{t('alerts.title')}</h1>
      </div>

      <div className="flex gap-1 border-b">
        <TabButton active={tab === 'rules'} onClick={() => setTab('rules')}>
          {t('alerts.tabRules')}
        </TabButton>
        <TabButton active={tab === 'events'} onClick={() => setTab('events')}>
          {t('alerts.tabEvents')}
          {!!unread && unread > 0 && (
            <span className="ml-1 inline-flex items-center justify-center rounded-full bg-destructive px-1.5 text-xs text-destructive-foreground">
              {unread}
            </span>
          )}
        </TabButton>
        <TabButton active={tab === 'channels'} onClick={() => setTab('channels')}>
          {t('alerts.tabChannels')}
        </TabButton>
      </div>

      {tab === 'rules' && <RulesTab />}
      {tab === 'events' && <EventsTab />}
      {tab === 'channels' && <ChannelsTab />}
    </div>
  )
}

/** 选项卡按钮。 */
function TabButton({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button
      className={`px-4 py-2 text-sm font-medium border-b-2 -mb-px transition-colors ${
        active ? 'border-primary text-primary' : 'border-transparent text-muted-foreground hover:text-foreground'
      }`}
      onClick={onClick}
    >
      {children}
    </button>
  )
}

/** 级别 pill：统一走 StatusBadge（severity → 状态色），不硬编码品牌色。 */
function LevelBadge({ level }: { level: string }) {
  const { t } = useTranslation()
  return <StatusBadge level={levelStatusLevel(level)} label={t(`alerts.level_${level}`, level)} dot={false} />
}

// ── 规则页 ──

function RulesTab() {
  const { t } = useTranslation()
  const { data: rules } = useAlertRules()
  const { data: channels } = useAlertChannels()
  const deleteRule = useDeleteAlertRule()
  const updateRule = useUpdateAlertRule()
  const [editing, setEditing] = useState<AlertRuleInfo | null>(null)
  const [showCreate, setShowCreate] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<AlertRuleInfo | null>(null)
  const [view, setView] = useState<ConfigView>('card')
  // 汇总条筛选：'enabled' 仅启用 / 'disabled' 仅停用 / null 全部。
  const [filter, setFilter] = useState<'enabled' | 'disabled' | null>(null)

  const channelName = (id: number) => channels?.find((c) => c.id === id)?.name ?? `#${id}`
  const channelText = (r: AlertRuleInfo) =>
    parseChannelIds(r.channelIds).map(channelName).join(', ') || (r.notifyTarget ? 'webhook' : '—')

  const summary = useMemo(() => summarizeRules(rules ?? []), [rules])
  const visible = useMemo(() => {
    const list = rules ?? []
    if (filter === 'enabled') return list.filter((r) => r.enabled)
    if (filter === 'disabled') return list.filter((r) => !r.enabled)
    return list
  }, [rules, filter])

  const toggleEnabled = (r: AlertRuleInfo) => updateRule.mutate({ id: r.id, enabled: !r.enabled })

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-3">
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
          <Button onClick={() => setShowCreate(true)}>+ {t('alerts.createRule')}</Button>
        </div>
      </div>

      {visible.length === 0 ? (
        <Panel>
          <p className="py-6 text-center text-sm text-muted-foreground">{t('alerts.emptyRules')}</p>
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
              subtitle={`${t(`alerts.trigger_${r.triggerType}`, r.triggerType)} · ${channelText(r)}`}
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
                    disabled={updateRule.isPending}
                    onChange={() => toggleEnabled(r)}
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
                      disabled={updateRule.isPending}
                      onChange={() => toggleEnabled(r)}
                      label={t('alerts.enabled')}
                      onLabel={t('alerts.on')}
                      offLabel={t('alerts.off')}
                    />
                  </TableCell>
                  <TableCell className="space-x-3 text-right whitespace-nowrap">
                    <button className="text-xs text-primary hover:underline" onClick={() => setEditing(r)}>
                      {t('common.edit')}
                    </button>
                    <button className="text-xs text-status-danger hover:underline" onClick={() => setDeleteTarget(r)}>
                      {t('common.delete')}
                    </button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Panel>
      )}

      {(showCreate || editing) && (
        <RuleDialog
          rule={editing}
          channels={channels ?? []}
          onClose={() => {
            setShowCreate(false)
            setEditing(null)
          }}
        />
      )}

      <DangerConfirm
        open={!!deleteTarget}
        title={t('alerts.deleteRuleConfirm')}
        description={deleteTarget?.name}
        scope="group"
        onConfirm={() => {
          if (deleteTarget) deleteRule.mutate(deleteTarget.id)
          setDeleteTarget(null)
        }}
        onCancel={() => setDeleteTarget(null)}
      />
    </div>
  )
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

// ── 事件页 ──

function EventsTab() {
  const { t } = useTranslation()
  const [filter, setFilter] = useState<EventQuery>({})
  const { data: eventPage } = useAlertEvents(filter)
  const { data: rules } = useAlertRules()
  const ack = useAcknowledgeEvent()
  const markAll = useMarkAllRead()

  const items = eventPage?.items ?? []
  const total = eventPage?.total ?? 0
  const page = filter.page ?? 1
  const pageSize = filter.pageSize ?? 50
  const totalPages = Math.max(1, Math.ceil(total / pageSize))
  // 改筛选条件即回第 1 页；翻页用单独 setFilter。datetime-local（本地时区）转 RFC3339 给后端（FR-149）。
  const patchFilter = (patch: Partial<EventQuery>) => setFilter((f) => ({ ...f, ...patch, page: 1 }))
  const toIso = (v: string) => (v ? new Date(v).toISOString() : undefined)

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <input
          className="p-2 border rounded text-sm"
          placeholder={t('alerts.keywordPlaceholder')}
          value={filter.keyword ?? ''}
          onChange={(e) => patchFilter({ keyword: e.target.value || undefined })}
        />
        <select
          className="p-2 border rounded text-sm"
          value={filter.level ?? ''}
          onChange={(e) => patchFilter({ level: e.target.value || undefined })}
        >
          <option value="">{t('alerts.allLevels')}</option>
          <option value="info">{t('alerts.level_info')}</option>
          <option value="warn">{t('alerts.level_warn')}</option>
          <option value="critical">{t('alerts.level_critical')}</option>
        </select>
        <select
          className="p-2 border rounded text-sm"
          value={filter.resolved === undefined ? '' : String(filter.resolved)}
          onChange={(e) => patchFilter({ resolved: e.target.value === '' ? undefined : e.target.value === 'true' })}
        >
          <option value="">{t('alerts.allStatus')}</option>
          <option value="false">{t('alerts.unresolved')}</option>
          <option value="true">{t('alerts.resolved')}</option>
        </select>
        <select
          className="p-2 border rounded text-sm"
          value={filter.acknowledged === undefined ? '' : String(filter.acknowledged)}
          onChange={(e) => patchFilter({ acknowledged: e.target.value === '' ? undefined : e.target.value === 'true' })}
        >
          <option value="">{t('alerts.allAck')}</option>
          <option value="false">{t('alerts.unacknowledged')}</option>
          <option value="true">{t('alerts.acknowledged')}</option>
        </select>
        <select
          className="p-2 border rounded text-sm"
          value={filter.ruleId ?? ''}
          onChange={(e) => patchFilter({ ruleId: e.target.value ? Number(e.target.value) : undefined })}
        >
          <option value="">{t('alerts.allRules')}</option>
          {(rules ?? []).map((r) => (
            <option key={r.id} value={r.id}>{r.name}</option>
          ))}
        </select>
        <label className="flex items-center gap-1 text-xs text-muted-foreground">
          {t('alerts.timeFrom')}
          <input
            type="datetime-local"
            className="p-1.5 border rounded text-sm"
            onChange={(e) => patchFilter({ from: toIso(e.target.value) })}
          />
        </label>
        <label className="flex items-center gap-1 text-xs text-muted-foreground">
          {t('alerts.timeTo')}
          <input
            type="datetime-local"
            className="p-1.5 border rounded text-sm"
            onChange={(e) => patchFilter({ to: toIso(e.target.value) })}
          />
        </label>
        <div className="flex-1" />
        <Button variant="outline" size="sm" onClick={() => markAll.mutate()}>
          {t('alerts.markAllRead')}
        </Button>
      </div>

      <Panel bodyClassName="p-0">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('alerts.firedAt')}</TableHead>
              <TableHead>{t('alerts.level')}</TableHead>
              <TableHead>{t('alerts.rule')}</TableHead>
              <TableHead>{t('alerts.message')}</TableHead>
              <TableHead>{t('alerts.count')}</TableHead>
              <TableHead>{t('alerts.status')}</TableHead>
              <TableHead className="text-right">{t('common.actions')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {items.map((e) => (
              <TableRow key={e.id} className={e.read ? '' : 'bg-primary/5'}>
                <TableCell className="whitespace-nowrap">{new Date(e.firedAt).toLocaleString()}</TableCell>
                <TableCell><LevelBadge level={e.level} /></TableCell>
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
                    <button className="text-xs text-primary hover:underline" onClick={() => ack.mutate(e.id)}>
                      {t('alerts.acknowledge')}
                    </button>
                  )}
                </TableCell>
              </TableRow>
            ))}
            {items.length === 0 && (
              <TableRow>
                <TableCell colSpan={7} className="text-center text-muted-foreground">
                  {t('alerts.emptyEvents')}
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
            onClick={() => setFilter((f) => ({ ...f, page: page - 1 }))}
          >
            {t('alerts.prevPage')}
          </Button>
          <span>{t('alerts.pageOf', { page, total: totalPages })}</span>
          <Button
            variant="outline"
            size="xs"
            disabled={page >= totalPages}
            onClick={() => setFilter((f) => ({ ...f, page: page + 1 }))}
          >
            {t('alerts.nextPage')}
          </Button>
        </div>
      )}
    </div>
  )
}

// ── 通道页 ──

function ChannelsTab() {
  const { t } = useTranslation()
  const { data: channels } = useAlertChannels()
  const deleteChannel = useDeleteAlertChannel()
  const testChannel = useTestAlertChannel()
  const [editing, setEditing] = useState<AlertChannelInfo | null>(null)
  const [showCreate, setShowCreate] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<AlertChannelInfo | null>(null)

  return (
    <div className="space-y-4">
      <div className="flex justify-end">
        <Button onClick={() => setShowCreate(true)}>+ {t('alerts.createChannel')}</Button>
      </div>

      {(channels ?? []).length === 0 ? (
        <Panel>
          <p className="py-6 text-center text-sm text-muted-foreground">{t('alerts.emptyChannels')}</p>
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
                  <Button variant="ghost" size="xs" disabled={testChannel.isPending} onClick={() => testChannel.mutate(c.id)}>
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

      {(showCreate || editing) && (
        <ChannelDialog
          channel={editing}
          onClose={() => {
            setShowCreate(false)
            setEditing(null)
          }}
        />
      )}

      <DangerConfirm
        open={!!deleteTarget}
        title={t('alerts.deleteChannelConfirm')}
        description={deleteTarget?.name}
        scope="platform"
        onConfirm={() => {
          if (deleteTarget) deleteChannel.mutate(deleteTarget.id)
          setDeleteTarget(null)
        }}
        onCancel={() => setDeleteTarget(null)}
      />
    </div>
  )
}

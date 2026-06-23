import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  useAlertRules,
  useAlertEvents,
  useDeleteAlertRule,
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
import { RuleDialog } from './alerts/RuleDialog'
import { ChannelDialog } from './alerts/ChannelDialog'
import {
  levelBadgeClass,
  formatSilenceWindow,
  parseChannelIds,
} from './alerts/alert-helpers'

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

/** 级别徽章。 */
function LevelBadge({ level }: { level: string }) {
  const { t } = useTranslation()
  return (
    <span className={`inline-block rounded px-2 py-0.5 text-xs font-medium ${levelBadgeClass(level)}`}>
      {t(`alerts.level_${level}`, level)}
    </span>
  )
}

// ── 规则页 ──

function RulesTab() {
  const { t } = useTranslation()
  const { data: rules } = useAlertRules()
  const { data: channels } = useAlertChannels()
  const deleteRule = useDeleteAlertRule()
  const [editing, setEditing] = useState<AlertRuleInfo | null>(null)
  const [showCreate, setShowCreate] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<AlertRuleInfo | null>(null)

  const channelName = (id: number) => channels?.find((c) => c.id === id)?.name ?? `#${id}`

  return (
    <div className="space-y-4">
      <div className="flex justify-end">
        <button
          className="px-4 py-2 bg-primary text-primary-foreground rounded-md hover:bg-primary/90"
          onClick={() => setShowCreate(true)}
        >
          {t('alerts.createRule')}
        </button>
      </div>

      <div className="border rounded-lg overflow-hidden">
        <table className="w-full text-sm">
          <thead className="bg-muted">
            <tr>
              <th className="p-3 text-left">{t('alerts.ruleName')}</th>
              <th className="p-3 text-left">{t('alerts.triggerType')}</th>
              <th className="p-3 text-left">{t('alerts.level')}</th>
              <th className="p-3 text-left">{t('alerts.condition')}</th>
              <th className="p-3 text-left">{t('alerts.channels')}</th>
              <th className="p-3 text-left">{t('alerts.silence')}</th>
              <th className="p-3 text-left">{t('alerts.enabled')}</th>
              <th className="p-3 text-left">{t('common.actions')}</th>
            </tr>
          </thead>
          <tbody>
            {(rules ?? []).map((r) => (
              <tr key={r.id} className="border-t">
                <td className="p-3 font-medium">{r.name}</td>
                <td className="p-3">{t(`alerts.trigger_${r.triggerType}`, r.triggerType)}</td>
                <td className="p-3"><LevelBadge level={r.level} /></td>
                <td className="p-3 text-muted-foreground">{ruleConditionText(r)}</td>
                <td className="p-3 text-muted-foreground">
                  {parseChannelIds(r.channelIds).map(channelName).join(', ') || (r.notifyTarget ? 'webhook' : '—')}
                </td>
                <td className="p-3 text-muted-foreground">{formatSilenceWindow(r.silenceStart, r.silenceEnd) || '—'}</td>
                <td className="p-3">{r.enabled ? t('alerts.on') : t('alerts.off')}</td>
                <td className="p-3 space-x-3 whitespace-nowrap">
                  <button className="text-primary hover:underline" onClick={() => setEditing(r)}>
                    {t('common.edit')}
                  </button>
                  <button className="text-destructive hover:underline" onClick={() => setDeleteTarget(r)}>
                    {t('common.delete')}
                  </button>
                </td>
              </tr>
            ))}
            {(!rules || rules.length === 0) && (
              <tr>
                <td colSpan={8} className="p-6 text-center text-muted-foreground">
                  {t('alerts.emptyRules')}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

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
  const { data: events } = useAlertEvents(filter)
  const ack = useAcknowledgeEvent()
  const markAll = useMarkAllRead()

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <select
          className="p-2 border rounded text-sm"
          value={filter.level ?? ''}
          onChange={(e) => setFilter({ ...filter, level: e.target.value || undefined })}
        >
          <option value="">{t('alerts.allLevels')}</option>
          <option value="info">{t('alerts.level_info')}</option>
          <option value="warn">{t('alerts.level_warn')}</option>
          <option value="critical">{t('alerts.level_critical')}</option>
        </select>
        <select
          className="p-2 border rounded text-sm"
          value={filter.resolved === undefined ? '' : String(filter.resolved)}
          onChange={(e) => setFilter({ ...filter, resolved: e.target.value === '' ? undefined : e.target.value === 'true' })}
        >
          <option value="">{t('alerts.allStatus')}</option>
          <option value="false">{t('alerts.unresolved')}</option>
          <option value="true">{t('alerts.resolved')}</option>
        </select>
        <select
          className="p-2 border rounded text-sm"
          value={filter.acknowledged === undefined ? '' : String(filter.acknowledged)}
          onChange={(e) => setFilter({ ...filter, acknowledged: e.target.value === '' ? undefined : e.target.value === 'true' })}
        >
          <option value="">{t('alerts.allAck')}</option>
          <option value="false">{t('alerts.unacknowledged')}</option>
          <option value="true">{t('alerts.acknowledged')}</option>
        </select>
        <div className="flex-1" />
        <button className="px-3 py-2 border rounded-md text-sm hover:bg-muted" onClick={() => markAll.mutate()}>
          {t('alerts.markAllRead')}
        </button>
      </div>

      <div className="border rounded-lg overflow-hidden">
        <table className="w-full text-sm">
          <thead className="bg-muted">
            <tr>
              <th className="p-3 text-left">{t('alerts.firedAt')}</th>
              <th className="p-3 text-left">{t('alerts.level')}</th>
              <th className="p-3 text-left">{t('alerts.rule')}</th>
              <th className="p-3 text-left">{t('alerts.message')}</th>
              <th className="p-3 text-left">{t('alerts.count')}</th>
              <th className="p-3 text-left">{t('alerts.status')}</th>
              <th className="p-3 text-left">{t('common.actions')}</th>
            </tr>
          </thead>
          <tbody>
            {(events ?? []).map((e) => (
              <tr key={e.id} className={`border-t ${e.read ? '' : 'bg-primary/5'}`}>
                <td className="p-3 whitespace-nowrap">{new Date(e.firedAt).toLocaleString()}</td>
                <td className="p-3"><LevelBadge level={e.level} /></td>
                <td className="p-3">{e.rule?.name ?? `#${e.ruleId}`}</td>
                <td className="p-3 max-w-[360px] truncate" title={e.message}>{e.message}</td>
                <td className="p-3">{e.count > 1 ? `×${e.count}` : ''}</td>
                <td className="p-3">
                  {e.resolved ? (
                    <span className="text-muted-foreground">{t('alerts.resolved')}</span>
                  ) : (
                    <span className="text-amber-600 dark:text-amber-400">{t('alerts.unresolved')}</span>
                  )}
                </td>
                <td className="p-3 whitespace-nowrap">
                  {e.acknowledged ? (
                    <span className="text-xs text-muted-foreground">{t('alerts.acknowledged')}</span>
                  ) : (
                    <button className="text-primary hover:underline" onClick={() => ack.mutate(e.id)}>
                      {t('alerts.acknowledge')}
                    </button>
                  )}
                </td>
              </tr>
            ))}
            {(!events || events.length === 0) && (
              <tr>
                <td colSpan={7} className="p-6 text-center text-muted-foreground">
                  {t('alerts.emptyEvents')}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
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
        <button
          className="px-4 py-2 bg-primary text-primary-foreground rounded-md hover:bg-primary/90"
          onClick={() => setShowCreate(true)}
        >
          {t('alerts.createChannel')}
        </button>
      </div>

      <div className="border rounded-lg overflow-hidden">
        <table className="w-full text-sm">
          <thead className="bg-muted">
            <tr>
              <th className="p-3 text-left">{t('alerts.channelName')}</th>
              <th className="p-3 text-left">{t('alerts.channelType')}</th>
              <th className="p-3 text-left">{t('alerts.enabled')}</th>
              <th className="p-3 text-left">{t('common.actions')}</th>
            </tr>
          </thead>
          <tbody>
            {(channels ?? []).map((c) => (
              <tr key={c.id} className="border-t">
                <td className="p-3 font-medium">{c.name}</td>
                <td className="p-3">{t(`alerts.channel_${c.type}`, c.type)}</td>
                <td className="p-3">{c.enabled ? t('alerts.on') : t('alerts.off')}</td>
                <td className="p-3 space-x-3 whitespace-nowrap">
                  <button
                    className="text-primary hover:underline disabled:opacity-50"
                    disabled={testChannel.isPending}
                    onClick={() => testChannel.mutate(c.id)}
                  >
                    {t('alerts.testSend')}
                  </button>
                  <button className="text-primary hover:underline" onClick={() => setEditing(c)}>
                    {t('common.edit')}
                  </button>
                  <button className="text-destructive hover:underline" onClick={() => setDeleteTarget(c)}>
                    {t('common.delete')}
                  </button>
                </td>
              </tr>
            ))}
            {(!channels || channels.length === 0) && (
              <tr>
                <td colSpan={4} className="p-6 text-center text-muted-foreground">
                  {t('alerts.emptyChannels')}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

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

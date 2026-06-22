import { useMemo, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import DangerConfirm from '@/components/DangerConfirm'
import { useInstances } from '@/api/instances'
import {
  useOnlinePlayers,
  useKickPlayer,
  useBanPlayer,
  useUnbanPlayer,
  useBans,
  useWhitelist,
  useWhitelistAction,
  usePlayerEvents,
  type OnlinePlayer,
  type PlayerActionResult,
  type PlayerEvent,
} from '@/api/players'

type Tab = 'online' | 'live' | 'bans' | 'whitelist'

/** 玩家管理页（FR-054 + FR-066 实时事件）：经各后端探针聚合在线玩家、踢/封/解封、白名单与封禁记录（FR-067 退役 RCON）。 */
export default function PlayersPage() {
  const { t } = useTranslation()
  const [tab, setTab] = useState<Tab>('online')

  return (
    <div>
      <h1 className="text-2xl font-bold mb-1">{t('players.title')}</h1>
      <p className="text-xs text-muted-foreground mb-4">{t('players.subtitle')}</p>

      <div className="flex gap-1 mb-4 border-b">
        {(['online', 'live', 'bans', 'whitelist'] as Tab[]).map((key) => (
          <button
            key={key}
            onClick={() => setTab(key)}
            className={`px-3 py-2 text-sm -mb-px border-b-2 ${
              tab === key ? 'border-primary text-foreground font-medium' : 'border-transparent text-muted-foreground'
            }`}
          >
            {t(`players.tab_${key}`)}
          </button>
        ))}
      </div>

      {tab === 'online' && <OnlineTab />}
      {tab === 'live' && <LiveTab />}
      {tab === 'bans' && <BansTab />}
      {tab === 'whitelist' && <WhitelistTab />}
    </div>
  )
}

function OnlineTab() {
  const { t } = useTranslation()
  const { data, isLoading } = useOnlinePlayers()
  const kick = useKickPlayer()
  const ban = useBanPlayer()
  const [confirm, setConfirm] = useState<{ kind: 'kick' | 'ban'; player: OnlinePlayer } | null>(null)
  const [reason, setReason] = useState('')

  const unavailable = (data?.backends || []).filter((b) => !b.available)

  const runAction = () => {
    if (!confirm) return
    const args = { name: confirm.player.name, scope: { reason: reason || undefined } }
    const onSuccess = (res: PlayerActionResult) => {
      toast.success(t('players.actionResult', { succeeded: res.succeeded, failed: res.failed }))
      setConfirm(null)
      setReason('')
    }
    const onError = () => toast.error(t('common.error'))
    if (confirm.kind === 'kick') kick.mutate(args, { onSuccess, onError })
    else ban.mutate(args, { onSuccess, onError })
  }

  return (
    <div>
      {unavailable.length > 0 && (
        <div className="mb-3 text-xs text-amber-600 bg-amber-50 dark:bg-amber-950/30 border border-amber-300/50 rounded-md px-3 py-2">
          {t('players.degraded', { names: unavailable.map((b) => b.instanceName).join(', ') })}
        </div>
      )}

      {isLoading ? (
        <p className="text-muted-foreground">{t('common.loading')}</p>
      ) : (
        <div className="border rounded-lg">
          <Table>
            <TableHeader className="bg-muted/50">
              <TableRow>
                <TableHead>{t('players.playerName')}</TableHead>
                <TableHead>{t('players.subserver')}</TableHead>
                <TableHead className="text-right">{t('common.actions')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {data?.players.map((p) => (
                <TableRow key={`${p.instanceId}-${p.name}`}>
                  <TableCell className="font-medium">{p.name}</TableCell>
                  <TableCell className="text-muted-foreground">{p.instanceName}</TableCell>
                  <TableCell className="text-right space-x-3">
                    <button className="text-xs text-amber-600 hover:underline" onClick={() => setConfirm({ kind: 'kick', player: p })}>
                      {t('players.kick')}
                    </button>
                    <button className="text-xs text-red-600 hover:underline" onClick={() => setConfirm({ kind: 'ban', player: p })}>
                      {t('players.ban')}
                    </button>
                  </TableCell>
                </TableRow>
              ))}
              {(!data || data.players.length === 0) && (
                <TableRow>
                  <TableCell colSpan={3} className="text-center text-muted-foreground">
                    {t('players.noOnline')}
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
        </div>
      )}

      {confirm && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
          <div className="bg-background border rounded-lg p-6 w-full max-w-md shadow-lg">
            <h2 className="text-lg font-bold mb-1">
              {confirm.kind === 'kick' ? t('players.kickTitle') : t('players.banTitle')}
            </h2>
            <p className="text-sm text-muted-foreground mb-3">
              {t('players.confirmTarget', { player: confirm.player.name, server: confirm.player.instanceName })}
            </p>
            <label className="text-sm font-medium">{t('players.reason')}</label>
            <input
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              className="w-full mt-1 mb-4 px-3 py-2 border rounded-md bg-background text-sm"
              placeholder={t('players.reasonPlaceholder')}
            />
            <div className="flex justify-end gap-2">
              <button
                onClick={() => { setConfirm(null); setReason('') }}
                className="px-4 py-2 text-sm border rounded-md hover:bg-accent"
              >
                {t('common.cancel')}
              </button>
              <button
                onClick={runAction}
                disabled={kick.isPending || ban.isPending}
                className="px-4 py-2 text-sm bg-destructive text-white rounded-md disabled:opacity-50"
              >
                {confirm.kind === 'kick' ? t('players.kick') : t('players.ban')}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

/**
 * 实时事件标签页（FR-066）：选一个实例，经 SSE 实时展示该实例（探针）的在线名册与玩家事件流。
 * 探针未连入时降级提示。子服与代理实例都可选（Bukkit 探针报本服 join/quit/chat，BC 探针报跨服路由）。
 */
function LiveTab() {
  const { t } = useTranslation()
  const { data: instances } = useInstances()
  const list = useMemo(() => instances || [], [instances])
  const [instanceId, setInstanceId] = useState<number | null>(null)
  const effectiveId = instanceId ?? list[0]?.id ?? null
  const { connected, roster, events } = usePlayerEvents(effectiveId)

  return (
    <div>
      <div className="flex items-center gap-2 mb-4">
        <label className="text-sm font-medium">{t('players.liveSelectInstance')}</label>
        <select
          value={effectiveId ?? ''}
          onChange={(e) => setInstanceId(Number(e.target.value))}
          className="px-3 py-2 border rounded-md bg-background text-sm"
        >
          {list.length === 0 && <option value="">{t('players.noBackends')}</option>}
          {list.map((i) => (
            <option key={i.id} value={i.id}>
              {i.name}
            </option>
          ))}
        </select>
      </div>

      {effectiveId === null ? (
        <p className="text-muted-foreground text-sm">{t('players.noBackends')}</p>
      ) : (
        <>
          <div
            className={`mb-4 flex items-center gap-2 text-xs rounded-md px-3 py-2 border ${
              connected
                ? 'text-emerald-600 bg-emerald-50 dark:bg-emerald-950/30 border-emerald-300/50'
                : 'text-amber-600 bg-amber-50 dark:bg-amber-950/30 border-amber-300/50'
            }`}
          >
            <span className={`h-2 w-2 rounded-full ${connected ? 'bg-emerald-500' : 'bg-amber-500'}`} />
            {connected ? t('players.liveProbeConnected') : t('players.liveProbeDisconnected')}
          </div>

          <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
            {/* 实时在线名册 */}
            <div className="border rounded-lg">
              <div className="px-3 py-2 border-b bg-muted/50 text-sm font-medium">
                {t('players.liveOnlineCount', { count: roster.length })}
              </div>
              {roster.length === 0 ? (
                <p className="text-center text-muted-foreground text-sm py-6">{t('players.liveNoOnline')}</p>
              ) : (
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>{t('players.playerName')}</TableHead>
                      <TableHead>{t('players.subserver')}</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {roster.map((p) => (
                      <TableRow key={p.name}>
                        <TableCell className="font-medium">{p.name}</TableCell>
                        <TableCell className="text-muted-foreground">{p.server || '--'}</TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              )}
            </div>

            {/* 事件流 */}
            <div className="border rounded-lg">
              <div className="px-3 py-2 border-b bg-muted/50 text-sm font-medium">{t('players.liveEventsTitle')}</div>
              {events.length === 0 ? (
                <p className="text-center text-muted-foreground text-sm py-6">{t('players.liveNoEvents')}</p>
              ) : (
                <ul className="divide-y max-h-[420px] overflow-auto text-sm">
                  {events.map((e, idx) => (
                    <li key={`${e.timestamp}-${idx}`} className="px-3 py-2 flex items-start gap-2">
                      <EventBadge type={e.type} />
                      <span className="flex-1">
                        {e.playerName && <span className="font-medium">{e.playerName}</span>}
                        {e.type === 'chat' && e.message && (
                          <span className="text-muted-foreground">: {e.message}</span>
                        )}
                        {e.type === 'cross_server' && (
                          <span className="text-muted-foreground">
                            {' '}
                            {t('players.liveCrossServerDesc', { from: e.fromServer || '?', to: e.toServer || '?' })}
                          </span>
                        )}
                        {(e.type === 'player_join' || e.type === 'player_quit') && e.server && (
                          <span className="text-muted-foreground"> @ {e.server}</span>
                        )}
                      </span>
                      <span className="text-xs text-muted-foreground shrink-0">
                        {new Date(e.timestamp * 1000).toLocaleTimeString()}
                      </span>
                    </li>
                  ))}
                </ul>
              )}
            </div>
          </div>
        </>
      )}
    </div>
  )
}

/** 事件类型徽标：按事件类型着色 + 中/英文标签。 */
function EventBadge({ type }: { type: PlayerEvent['type'] }) {
  const { t } = useTranslation()
  const color: Record<string, string> = {
    player_join: 'text-emerald-600 bg-emerald-50 dark:bg-emerald-950/30',
    player_quit: 'text-muted-foreground bg-muted',
    chat: 'text-blue-600 bg-blue-50 dark:bg-blue-950/30',
    cross_server: 'text-violet-600 bg-violet-50 dark:bg-violet-950/30',
    connected: 'text-emerald-600 bg-emerald-50 dark:bg-emerald-950/30',
    disconnected: 'text-amber-600 bg-amber-50 dark:bg-amber-950/30',
  }
  return (
    <span className={`shrink-0 text-xs px-1.5 py-0.5 rounded ${color[type] || 'text-muted-foreground bg-muted'}`}>
      {t(`players.evt_${type}`, { defaultValue: type })}
    </span>
  )
}

function BansTab() {
  const { t } = useTranslation()
  const [activeOnly, setActiveOnly] = useState(false)
  const { data: bans, isLoading } = useBans({ active: activeOnly })
  const unban = useUnbanPlayer()
  const [pending, setPending] = useState<string | null>(null)

  const doUnban = () => {
    if (!pending) return
    unban.mutate(
      { name: pending },
      {
        onSuccess: () => {
          toast.success(t('players.unbanned', { player: pending }))
          setPending(null)
        },
        onError: () => toast.error(t('common.error')),
      },
    )
  }

  const scopeLabel = (scope: string) => t(`players.scope_${scope}`, { defaultValue: scope })

  return (
    <div>
      <label className="flex items-center gap-2 text-sm mb-3">
        <input type="checkbox" checked={activeOnly} onChange={(e) => setActiveOnly(e.target.checked)} />
        {t('players.activeOnly')}
      </label>

      {isLoading ? (
        <p className="text-muted-foreground">{t('common.loading')}</p>
      ) : (
        <div className="border rounded-lg">
          <Table>
            <TableHeader className="bg-muted/50">
              <TableRow>
                <TableHead>{t('players.playerName')}</TableHead>
                <TableHead>{t('players.reason')}</TableHead>
                <TableHead>{t('players.scope')}</TableHead>
                <TableHead>{t('players.operator')}</TableHead>
                <TableHead>{t('players.banTime')}</TableHead>
                <TableHead>{t('common.status')}</TableHead>
                <TableHead className="text-right">{t('common.actions')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {bans?.map((b) => (
                <TableRow key={b.id}>
                  <TableCell className="font-medium">{b.playerName}</TableCell>
                  <TableCell className="text-muted-foreground">{b.reason || '--'}</TableCell>
                  <TableCell>{scopeLabel(b.scope)}</TableCell>
                  <TableCell className="text-muted-foreground">{b.operator?.username || '--'}</TableCell>
                  <TableCell className="text-muted-foreground">{new Date(b.createdAt).toLocaleString()}</TableCell>
                  <TableCell>
                    <span className={`inline-flex items-center gap-1.5 text-xs ${b.active ? 'text-red-600' : 'text-muted-foreground'}`}>
                      <span className={`h-2 w-2 rounded-full ${b.active ? 'bg-red-500' : 'bg-muted-foreground'}`} />
                      {b.active ? t('players.banActive') : t('players.banLifted')}
                    </span>
                  </TableCell>
                  <TableCell className="text-right">
                    {b.active && (
                      <button className="text-xs text-blue-600 hover:underline" onClick={() => setPending(b.playerName)}>
                        {t('players.unban')}
                      </button>
                    )}
                  </TableCell>
                </TableRow>
              ))}
              {(!bans || bans.length === 0) && (
                <TableRow>
                  <TableCell colSpan={7} className="text-center text-muted-foreground">
                    {t('players.noBans')}
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
        </div>
      )}

      <DangerConfirm
        open={pending !== null}
        title={t('players.unbanTitle')}
        description={t('players.unbanConfirm', { player: pending || '' })}
        confirmLabel={t('players.unban')}
        scope="group"
        onConfirm={doUnban}
        onCancel={() => setPending(null)}
      />
    </div>
  )
}

function WhitelistTab() {
  const { t } = useTranslation()
  const { data: instances } = useInstances({ role: 'backend' })
  const backends = useMemo(() => instances || [], [instances])
  const [instanceId, setInstanceId] = useState<number | null>(null)
  const effectiveId = instanceId ?? backends[0]?.id ?? null
  const { data: wl, isLoading } = useWhitelist(effectiveId)
  const wlAction = useWhitelistAction(effectiveId)
  const [name, setName] = useState('')

  const add = (e: FormEvent) => {
    e.preventDefault()
    if (!name.trim()) return
    wlAction.mutate(
      { action: 'add', player: name.trim() },
      {
        onSuccess: () => {
          toast.success(t('players.whitelistAdded', { player: name.trim() }))
          setName('')
        },
        onError: () => toast.error(t('common.error')),
      },
    )
  }

  const remove = (player: string) => {
    wlAction.mutate(
      { action: 'remove', player },
      {
        onSuccess: () => toast.success(t('players.whitelistRemoved', { player })),
        onError: () => toast.error(t('common.error')),
      },
    )
  }

  return (
    <div>
      <div className="flex items-center gap-2 mb-4">
        <label className="text-sm font-medium">{t('players.selectBackend')}</label>
        <select
          value={effectiveId ?? ''}
          onChange={(e) => setInstanceId(Number(e.target.value))}
          className="px-3 py-2 border rounded-md bg-background text-sm"
        >
          {backends.length === 0 && <option value="">{t('players.noBackends')}</option>}
          {backends.map((b) => (
            <option key={b.id} value={b.id}>
              {b.name}
            </option>
          ))}
        </select>
      </div>

      {effectiveId === null ? (
        <p className="text-muted-foreground text-sm">{t('players.noBackends')}</p>
      ) : (
        <>
          <form onSubmit={add} className="flex gap-2 mb-4 max-w-md">
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              className="flex-1 px-3 py-2 border rounded-md bg-background text-sm"
              placeholder={t('players.whitelistAddPlaceholder')}
            />
            <button
              type="submit"
              disabled={wlAction.isPending || !name.trim()}
              className="px-3 py-2 text-sm bg-primary text-primary-foreground rounded-md disabled:opacity-50"
            >
              {t('players.whitelistAdd')}
            </button>
          </form>

          {isLoading ? (
            <p className="text-muted-foreground">{t('common.loading')}</p>
          ) : wl && !wl.available ? (
            <p className="text-sm text-amber-600">{t('players.whitelistUnavailable')}</p>
          ) : (
            <div className="border rounded-lg max-w-md">
              <Table>
                <TableHeader className="bg-muted/50">
                  <TableRow>
                    <TableHead>{t('players.playerName')}</TableHead>
                    <TableHead className="text-right">{t('common.actions')}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {wl?.players.map((p) => (
                    <TableRow key={p}>
                      <TableCell className="font-medium">{p}</TableCell>
                      <TableCell className="text-right">
                        <button className="text-xs text-red-600 hover:underline" onClick={() => remove(p)}>
                          {t('common.delete')}
                        </button>
                      </TableCell>
                    </TableRow>
                  ))}
                  {(!wl || wl.players.length === 0) && (
                    <TableRow>
                      <TableCell colSpan={2} className="text-center text-muted-foreground">
                        {t('players.whitelistEmpty')}
                      </TableCell>
                    </TableRow>
                  )}
                </TableBody>
              </Table>
            </div>
          )}
        </>
      )}
    </div>
  )
}

import { Fragment, useId, useMemo, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Button } from '@jianmanager/ui/components/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { Input } from '@jianmanager/ui/components/input'
import { Label } from '@jianmanager/ui/components/label'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@jianmanager/ui/components/table'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@jianmanager/ui/components/select'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
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
type EventFilter = 'all' | PlayerEvent['type']
type PlayerActionKind = 'kick' | 'ban'
type ConfirmState =
  | { kind: PlayerActionKind; mode: 'single'; player: OnlinePlayer }
  | { kind: PlayerActionKind; mode: 'batch' }

const PLAYER_EVENT_FILTERS: EventFilter[] = ['all', 'player_join', 'player_quit', 'chat', 'cross_server', 'connected', 'disconnected']
const ALL_SERVERS = 'all'

function playerKey(player: OnlinePlayer) {
  return `${player.instanceId}:${player.name}`
}

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
  const reasonId = useId()
  const { data, isLoading } = useOnlinePlayers()
  const kick = useKickPlayer()
  const ban = useBanPlayer()
  const [confirm, setConfirm] = useState<ConfirmState | null>(null)
  const [reason, setReason] = useState('')
  const [serverFilter, setServerFilter] = useState(ALL_SERVERS)
  const [selectedKeys, setSelectedKeys] = useState<Set<string>>(() => new Set())

  const players = useMemo(() => data?.players ?? [], [data?.players])
  const backends = useMemo(() => data?.backends ?? [], [data?.backends])
  const unavailable = backends.filter((b) => !b.available)
  const pending = kick.isPending || ban.isPending
  const serverOptions = useMemo(() => {
    const servers = new Map<number, string>()
    for (const backend of backends) servers.set(backend.instanceId, backend.instanceName)
    for (const player of players) servers.set(player.instanceId, player.instanceName)
    return Array.from(servers, ([id, name]) => ({ id, name })).sort((a, b) => a.name.localeCompare(b.name))
  }, [backends, players])
  const visiblePlayers = useMemo(
    () => players.filter((p) => serverFilter === ALL_SERVERS || String(p.instanceId) === serverFilter),
    [players, serverFilter],
  )
  const groupedPlayers = useMemo(
    () =>
      serverOptions
        .filter((server) => serverFilter === ALL_SERVERS || String(server.id) === serverFilter)
        .map((server) => ({
          ...server,
          players: visiblePlayers.filter((p) => p.instanceId === server.id),
        }))
        .filter((server) => server.players.length > 0),
    [serverFilter, serverOptions, visiblePlayers],
  )
  const selectedPlayers = useMemo(() => players.filter((p) => selectedKeys.has(playerKey(p))), [players, selectedKeys])
  const selectedNames = useMemo(() => {
    const names = new Set<string>()
    for (const player of selectedPlayers) names.add(player.name)
    return Array.from(names)
  }, [selectedPlayers])
  const selectedServerCount = new Set(selectedPlayers.map((p) => p.instanceId)).size
  const visibleSelected = visiblePlayers.length > 0 && visiblePlayers.every((p) => selectedKeys.has(playerKey(p)))

  const closeConfirm = () => {
    setConfirm(null)
    setReason('')
  }

  const togglePlayer = (player: OnlinePlayer, checked: boolean) => {
    setSelectedKeys((current) => {
      const next = new Set(current)
      if (checked) next.add(playerKey(player))
      else next.delete(playerKey(player))
      return next
    })
  }

  const toggleVisiblePlayers = (checked: boolean) => {
    setSelectedKeys((current) => {
      const next = new Set(current)
      for (const player of visiblePlayers) {
        if (checked) next.add(playerKey(player))
        else next.delete(playerKey(player))
      }
      return next
    })
  }

  const openBatchConfirm = (kind: PlayerActionKind) => {
    if (selectedPlayers.length === 0) return
    setConfirm({ kind, mode: 'batch' })
  }

  const runAction = async () => {
    if (!confirm) return
    const names = confirm.mode === 'single' ? [confirm.player.name] : selectedNames
    if (names.length === 0) {
      closeConfirm()
      return
    }
    const mutation = confirm.kind === 'kick' ? kick : ban
    let succeeded = 0
    let failed = 0
    for (const name of names) {
      try {
        const res: PlayerActionResult = await mutation.mutateAsync({
          name,
          scope: { reason: reason || undefined },
        })
        succeeded += res.succeeded
        failed += res.failed
      } catch {
        failed += 1
      }
    }
    const message = t('players.actionResult', { succeeded, failed })
    if (failed > 0) toast.error(message)
    else toast.success(message)
    if (confirm.mode === 'batch') setSelectedKeys(new Set())
    closeConfirm()
  }

  const confirmTitle = !confirm
    ? ''
    : confirm.mode === 'batch'
      ? confirm.kind === 'kick' ? t('players.batchKickTitle') : t('players.batchBanTitle')
      : confirm.kind === 'kick' ? t('players.kickTitle') : t('players.banTitle')
  const confirmDescription = !confirm
    ? ''
    : confirm.mode === 'batch'
      ? confirm.kind === 'kick'
        ? t('players.batchKickConfirm', { count: selectedPlayers.length, servers: selectedServerCount })
        : t('players.batchBanConfirm', { count: selectedPlayers.length, servers: selectedServerCount })
      : t('players.confirmTarget', { player: confirm.player.name, server: confirm.player.instanceName })
  const confirmLabel = !confirm
    ? ''
    : confirm.mode === 'batch'
      ? confirm.kind === 'kick' ? t('players.batchKick') : t('players.batchBan')
      : confirm.kind === 'kick' ? t('players.kick') : t('players.ban')

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
        <>
          <div className="mb-3 flex flex-wrap items-center gap-2">
            <label className="text-sm font-medium">{t('players.serverFilter')}</label>
            <Select value={serverFilter} onValueChange={setServerFilter}>
              <SelectTrigger size="sm" className="w-44">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={ALL_SERVERS}>{t('players.allServers')}</SelectItem>
                {serverOptions.map((server) => (
                  <SelectItem key={server.id} value={String(server.id)}>
                    {server.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <div className="ml-auto flex flex-wrap items-center gap-2">
              <span className="text-xs text-muted-foreground">
                {t('players.selectedCount', { count: selectedPlayers.length })}
              </span>
              <Button
                size="sm"
                variant="destructive"
                disabled={selectedPlayers.length === 0 || pending}
                onClick={() => openBatchConfirm('kick')}
              >
                {t('players.batchKick')}
              </Button>
              <Button
                size="sm"
                variant="destructive"
                disabled={selectedPlayers.length === 0 || pending}
                onClick={() => openBatchConfirm('ban')}
              >
                {t('players.batchBan')}
              </Button>
            </div>
          </div>

          <div className="border rounded-lg">
            <Table>
              <TableHeader className="bg-muted/50">
                <TableRow>
                  <TableHead className="w-10">
                    <Checkbox
                      checked={visibleSelected}
                      disabled={visiblePlayers.length === 0}
                      onCheckedChange={(v) => toggleVisiblePlayers(v === true)}
                      aria-label={t('players.selectVisible')}
                    />
                  </TableHead>
                  <TableHead>{t('players.playerName')}</TableHead>
                  <TableHead>{t('players.subserver')}</TableHead>
                  <TableHead className="text-right">{t('common.actions')}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {groupedPlayers.map((group) => (
                  <Fragment key={group.id}>
                    <TableRow className="bg-muted/25 hover:bg-muted/25">
                      <TableCell colSpan={4} className="text-xs font-medium text-muted-foreground">
                        {t('players.serverGroupLabel', { server: group.name, count: group.players.length })}
                      </TableCell>
                    </TableRow>
                    {group.players.map((p) => (
                      <TableRow key={`${p.instanceId}-${p.name}`} data-state={selectedKeys.has(playerKey(p)) ? 'selected' : undefined}>
                        <TableCell>
                          <Checkbox
                            checked={selectedKeys.has(playerKey(p))}
                            onCheckedChange={(v) => togglePlayer(p, v === true)}
                            aria-label={t('players.selectPlayer', { player: p.name, server: p.instanceName })}
                          />
                        </TableCell>
                        <TableCell className="font-medium">{p.name}</TableCell>
                        <TableCell className="text-muted-foreground">{p.instanceName}</TableCell>
                        <TableCell className="text-right">
                          <div className="flex justify-end gap-2">
                            <Button size="xs" variant="destructive" disabled={pending} onClick={() => setConfirm({ kind: 'kick', mode: 'single', player: p })}>
                              {t('players.kick')}
                            </Button>
                            <Button size="xs" variant="destructive" disabled={pending} onClick={() => setConfirm({ kind: 'ban', mode: 'single', player: p })}>
                              {t('players.ban')}
                            </Button>
                          </div>
                        </TableCell>
                      </TableRow>
                    ))}
                  </Fragment>
                ))}
                {players.length === 0 && (
                  <TableRow>
                    <TableCell colSpan={4} className="text-center text-muted-foreground">
                      {t('players.noOnline')}
                    </TableCell>
                  </TableRow>
                )}
                {players.length > 0 && visiblePlayers.length === 0 && (
                  <TableRow>
                    <TableCell colSpan={4} className="text-center text-muted-foreground">
                      {t('players.noOnline')}
                    </TableCell>
                  </TableRow>
                )}
              </TableBody>
            </Table>
          </div>
        </>
      )}

      <Dialog open={confirm !== null} onOpenChange={(open) => { if (!open) closeConfirm() }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{confirmTitle}</DialogTitle>
            <DialogDescription>{confirmDescription}</DialogDescription>
          </DialogHeader>
          <div className="space-y-2">
            <Label htmlFor={reasonId}>{t('players.reason')}</Label>
            <Input
              id={reasonId}
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              placeholder={t('players.reasonPlaceholder')}
              autoFocus
            />
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={closeConfirm}>
              {t('common.cancel')}
            </Button>
            <Button variant="destructive" onClick={runAction} disabled={pending}>
              {confirmLabel}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
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
  const [eventFilter, setEventFilter] = useState<EventFilter>('all')
  const [clearedBefore, setClearedBefore] = useState(0)
  const [pausedEvents, setPausedEvents] = useState<PlayerEvent[] | null>(null)
  const visibleEvents = (pausedEvents ?? events)
    .filter((e) => e.timestamp >= clearedBefore)
    .filter((e) => eventFilter === 'all' || e.type === eventFilter)
  const paused = pausedEvents !== null

  const togglePause = () => setPausedEvents(paused ? null : events)
  const clearEvents = () => {
    const source = pausedEvents ?? events
    setClearedBefore(Math.max(0, ...source.map((e) => e.timestamp)) + 1)
    if (paused) setPausedEvents([])
  }

  return (
    <div>
      <div className="flex items-center gap-2 mb-4">
        <label className="text-sm font-medium">{t('players.liveSelectInstance')}</label>
        <Select
          value={effectiveId === null ? '' : String(effectiveId)}
          onValueChange={(v) => setInstanceId(Number(v))}
          disabled={list.length === 0}
        >
          <SelectTrigger className="w-full">
            <SelectValue placeholder={t('players.noBackends')} />
          </SelectTrigger>
          <SelectContent>
            {list.map((i) => (
              <SelectItem key={i.id} value={String(i.id)}>
                {i.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
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
              <div className="flex flex-wrap items-center gap-2 border-b bg-muted/50 px-3 py-2">
                <div className="text-sm font-medium">{t('players.liveEventsTitle')}</div>
                <div className="ml-auto flex flex-wrap items-center gap-2">
                  <Select value={eventFilter} onValueChange={(v) => setEventFilter(v as EventFilter)}>
                    <SelectTrigger size="sm" className="w-32">
                      <SelectValue aria-label={t('players.liveFilter')} />
                    </SelectTrigger>
                    <SelectContent>
                      {PLAYER_EVENT_FILTERS.map((type) => (
                        <SelectItem key={type} value={type}>
                          {type === 'all' ? t('players.liveFilterAll') : t(`players.evt_${type}`, { defaultValue: type })}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  <Button variant="link" size="xs" className="h-auto p-0 text-muted-foreground hover:text-foreground" onClick={togglePause}>
                    {paused ? t('players.liveResume') : t('players.livePause')}
                  </Button>
                  <Button variant="link" size="xs" className="h-auto p-0 text-muted-foreground hover:text-foreground" onClick={clearEvents}>
                    {t('players.liveClear')}
                  </Button>
                </div>
              </div>
              {visibleEvents.length === 0 ? (
                <p className="text-center text-muted-foreground text-sm py-6">{t('players.liveNoEvents')}</p>
              ) : (
                <ul className="divide-y max-h-[420px] overflow-auto text-sm">
                  {visibleEvents.map((e, idx) => (
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
        <Checkbox
          checked={activeOnly}
          onCheckedChange={(v) => setActiveOnly(v === true)}
          aria-label={t('players.activeOnly')}
        />
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
                    <span className={`inline-flex items-center gap-1.5 text-xs ${b.active ? 'text-status-danger' : 'text-muted-foreground'}`}>
                      <span className={`h-2 w-2 rounded-full ${b.active ? 'bg-status-danger' : 'bg-muted-foreground'}`} />
                      {b.active ? t('players.banActive') : t('players.banLifted')}
                    </span>
                  </TableCell>
                  <TableCell className="text-right">
                    {b.active && (
                      <Button variant="link" size="xs" className="h-auto p-0" onClick={() => setPending(b.playerName)}>
                        {t('players.unban')}
                      </Button>
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
        <Select
          value={effectiveId === null ? '' : String(effectiveId)}
          onValueChange={(v) => setInstanceId(Number(v))}
          disabled={backends.length === 0}
        >
          <SelectTrigger className="w-full">
            <SelectValue placeholder={t('players.noBackends')} />
          </SelectTrigger>
          <SelectContent>
            {backends.map((b) => (
              <SelectItem key={b.id} value={String(b.id)}>
                {b.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
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
            <Button type="submit" disabled={wlAction.isPending || !name.trim()}>
              {t('players.whitelistAdd')}
            </Button>
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
                        <Button variant="link" size="xs" className="h-auto p-0 text-status-danger hover:text-status-danger" onClick={() => remove(p)}>
                          {t('common.delete')}
                        </Button>
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

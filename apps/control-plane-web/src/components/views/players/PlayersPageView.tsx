/**
 * @file PlayersPageView：玩家管理页（在线玩家 / 实时事件 / 封禁记录 / 白名单四个 Tab）的受控视图，
 *       取数、SSE 订阅、写动作与 toast 由应用容器负责。
 * @input lib/player（OnlinePlayersResult / OnlinePlayer / BanRecord / WhitelistResult）、
 *        lib/instance-types（InstanceInfo：实时事件与白名单两处实例选择器的候选）、
 *        views/DangerConfirm（解封二次确认）、Button/Dialog/Input/Label/Select/Checkbox/Table/
 *        PageShell/PageHeader 等原语、翻译上下文
 * @output PlayersPageView、PlayersPageViewProps、PlayerTab、PlayerActionKind、PlayerActionRequest、
 *         PlayerEventType、PlayerEventRow、RosterEntry、PlayersOnlineTabView、PlayersOnlineTabViewProps、
 *         PlayersLiveTabView、PlayersLiveTabViewProps、PlayersBansTabView、PlayersBansTabViewProps、
 *         PlayersWhitelistTabView、PlayersWhitelistTabViewProps
 * @sync apps/control-plane-web/src/pages/PlayersPage.tsx（容器）、apps/control-plane-web/src/pages/PlayersPage.dom.test.tsx
 * @since FR-502（组件受控化迁包；原页 FR-054 玩家治理 + FR-066 实时玩家事件 + FR-067 探针降级）
 */
import { Fragment, useId, useMemo, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
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
import { Label } from '@jianmanager/ui/components/label'
import { PageHeader, PageShell } from '@jianmanager/ui/components/layout'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@jianmanager/ui/components/select'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@jianmanager/ui/components/table'
import DangerConfirm from '@jianmanager/ui/components/views/DangerConfirm'
import type { InstanceInfo } from '@jianmanager/ui/lib/instance-types'
import type {
  BanRecord,
  OnlinePlayer,
  OnlinePlayersResult,
  WhitelistResult,
} from '@jianmanager/ui/lib/player'

/** 页内四个 Tab：在线玩家 / 实时事件 / 封禁记录 / 白名单。 */
export type PlayerTab = 'online' | 'live' | 'bans' | 'whitelist'

/** 页内 Tab 顺序（手写下划线式导航，与既有可达名一致）。 */
const PLAYER_TABS: PlayerTab[] = ['online', 'live', 'bans', 'whitelist']

/** 玩家治理动作种类：踢出 / 封禁。 */
export type PlayerActionKind = 'kick' | 'ban'

/** 事件流筛选取值：'all' 或某种事件类型。 */
type PlayerEventFilter = 'all' | PlayerEventType

/**
 * 探针经反向 WS 实时上报的玩家事件类型（FR-066）。
 *
 * 与应用侧 `@/api/players` 的 `PlayerEventType` 同形；此处按本视图渲染所需的最小字段集声明，
 * 不把 API 层类型搬进包（同 `ScheduleLogRow` / `AlertEventFilter` 的取舍）：容器直接传
 * `PlayerEvent` 也结构兼容。
 */
export type PlayerEventType =
  | 'connected'
  | 'disconnected'
  | 'heartbeat'
  | 'player_join'
  | 'player_quit'
  | 'chat'
  | 'cross_server'

/** 事件流筛选下拉的候选顺序（含「全部」）。 */
const PLAYER_EVENT_FILTERS: PlayerEventFilter[] = ['all', 'player_join', 'player_quit', 'chat', 'cross_server', 'connected', 'disconnected']

/** Radix Select 不接受空字符串选项值，用哨兵值表达「全部子服」。 */
const ALL_SERVERS = 'all'

/**
 * 一条实时玩家事件（本视图渲染所需的最小字段集）。
 *
 * 刻意只声明用到的字段，而非照搬应用侧 `@/api/players` 的 `PlayerEvent`：
 * 两者字段同形、结构互通，容器直接传 `PlayerEvent[]` 也兼容。
 */
export interface PlayerEventRow {
  /** 事件类型（决定徽标文案与着色）。 */
  type: PlayerEventType
  /** Unix 秒级时间戳。 */
  timestamp: number
  /** 相关玩家名（连接/断开事件可能缺省）。 */
  playerName?: string
  /** 聊天正文（仅 chat）。 */
  message?: string
  /** 所在子服（join/quit/cross_server）。 */
  server?: string
  /** 跨服来源子服（仅 cross_server）。 */
  fromServer?: string
  /** 跨服目标子服（仅 cross_server）。 */
  toServer?: string
}

/** 实时在线名册中的一名玩家及其所在子服（跨服后为迁往的子服）。 */
export interface RosterEntry {
  name: string
  /** 所在子服名；探针未给出时为空串。 */
  server: string
}

/**
 * 在线玩家名册的唯一键：同一玩家可能同时出现在多个子服，故按「实例 + 名字」区分。
 */
function playerKey(player: OnlinePlayer) {
  return `${player.instanceId}:${player.name}`
}

// ── 在线玩家 ──

/**
 * 踢出/封禁的上报载荷（单个与批量共用一条上报路径）。
 *
 * `names` 由本组件组装：单个取确认目标，批量取勾选集合按玩家名去重
 * （封禁按玩家名全局执行，同名多服只提交一次——与原页一致）；原因留空时归一为 undefined。
 */
export interface PlayerActionRequest {
  /** 动作种类。 */
  kind: PlayerActionKind
  /** 目标玩家名（已按名字去重）。 */
  names: string[]
  /** 可选原因，随动作一并提交。 */
  reason?: string
}

/**
 * 在线玩家 Tab 的注入契约：聚合名册（含各后端探针可用性）由容器按 Tab 门控取数后注入；
 * 子服筛选、勾选集合、确认弹窗与原因草稿都只影响本 Tab 的展示，留本组件。
 */
export interface PlayersOnlineTabViewProps {
  /** 在线玩家聚合结果（players 为跨后端名册，backends 为探针可用性）。 */
  online?: OnlinePlayersResult
  /** 名册加载态。 */
  isLoading?: boolean
  /** 踢/封在途：禁用行内与批量的危险按钮（防止在途重复提交）。 */
  actionPending?: boolean
  /**
   * 踢出/封禁上报：循环提交（逐玩家一次请求）、成功/失败计数与结果 toast 都在容器完成，
   * 本组件只负责收集目标集合与原因，随后收起弹窗并清空批量勾选。
   */
  onPlayerAction: (request: PlayerActionRequest) => Promise<void>
}

/**
 * 在线玩家 Tab（FR-054 / FR-067）：按子服分组的名册（BC 跨服感知）、批量勾选、踢出/封禁（原因二次确认）、
 * 探针不可达时的降级提示。
 */
export function PlayersOnlineTabView({
  online,
  isLoading = false,
  actionPending = false,
  onPlayerAction,
}: PlayersOnlineTabViewProps) {
  const { t } = useTranslation()
  const reasonId = useId()
  // 确认弹窗的目标：单个玩家或批量（勾选集合）。
  const [confirm, setConfirm] = useState<
    | { kind: PlayerActionKind; mode: 'single'; player: OnlinePlayer }
    | { kind: PlayerActionKind; mode: 'batch' }
    | null
  >(null)
  const [reason, setReason] = useState('')
  const [serverFilter, setServerFilter] = useState(ALL_SERVERS)
  const [selectedKeys, setSelectedKeys] = useState<Set<string>>(() => new Set())

  const players = useMemo(() => online?.players ?? [], [online?.players])
  const backends = useMemo(() => online?.backends ?? [], [online?.backends])
  const unavailable = backends.filter((b) => !b.available)
  const pending = actionPending
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
    const kind = confirm.kind
    const batch = confirm.mode === 'batch'
    // 上报后无论成败都收起弹窗（结果 toast 由容器按 succeeded/failed 决定）；批量则顺带清空勾选。
    await onPlayerAction({ kind, names, reason: reason || undefined })
    if (batch) setSelectedKeys(new Set())
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
            <Button variant="destructive" onClick={() => void runAction()} disabled={pending}>
              {confirmLabel}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

// ── 实时事件 ──

/**
 * 实时事件 Tab 的注入契约：SSE 订阅（探针连接状态、实时名册、事件流）**留在容器**——
 * 它是长驻订阅副作用，不属展示层；本组件只呈现订阅结果。
 *
 * 实例选择（`instanceId`）是订阅目标，一变即换订阅，故由容器持有（见 `PlayersPageViewProps.liveInstanceId` 的说明）；
 * 事件筛选、暂停快照与「清空」水位只影响本面板的展示，留本组件。
 */
export interface PlayersLiveTabViewProps {
  /** 可选实例候选（子服与代理都可选：Bukkit 探针报本服 join/quit/chat，BC 探针报跨服路由）。 */
  instances: InstanceInfo[]
  /** 当前订阅目标实例 id（容器已解析「未选即首个」）；null 表示无候选。 */
  instanceId: number | null
  /** 切换订阅目标上报。 */
  onInstanceChange: (id: number) => void
  /** 探针是否在位连接（false 时降级提示）。 */
  connected: boolean
  /** 实时在线名册（探针在位时由事件流维护）。 */
  roster: RosterEntry[]
  /** 最近事件流（倒序）。 */
  events: PlayerEventRow[]
}

/**
 * 实时事件 Tab（FR-066）：选一个实例，经 SSE 展示该实例（探针）的在线名册与玩家事件流。
 * 探针未连入时降级提示。子服与代理实例都可选（Bukkit 探针报本服 join/quit/chat，BC 探针报跨服路由）。
 */
export function PlayersLiveTabView({
  instances,
  instanceId,
  onInstanceChange,
  connected,
  roster,
  events,
}: PlayersLiveTabViewProps) {
  const { t } = useTranslation()
  const [eventFilter, setEventFilter] = useState<PlayerEventFilter>('all')
  // 「清空」用时间戳水位实现：只隐藏水位之前的事件，不打断事件流本身。
  const [clearedBefore, setClearedBefore] = useState(0)
  // 暂停时冻结一份快照（null 表示跟随实时流）。
  const [pausedEvents, setPausedEvents] = useState<PlayerEventRow[] | null>(null)
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
          value={instanceId === null ? '' : String(instanceId)}
          onValueChange={(v) => onInstanceChange(Number(v))}
          disabled={instances.length === 0}
        >
          <SelectTrigger className="w-full">
            <SelectValue placeholder={t('players.noBackends')} />
          </SelectTrigger>
          <SelectContent>
            {instances.map((i) => (
              <SelectItem key={i.id} value={String(i.id)}>
                {i.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      {instanceId === null ? (
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
                  {/* aria-label 落在可聚焦的触发按钮上（原页加在只读的 SelectValue 上，读屏取不到名字）。 */}
                  <Select value={eventFilter} onValueChange={(v) => setEventFilter(v as PlayerEventFilter)}>
                    <SelectTrigger size="sm" className="w-32" aria-label={t('players.liveFilter')}>
                      <SelectValue />
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
function EventBadge({ type }: { type: PlayerEventType }) {
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

// ── 封禁记录 ──

/**
 * 封禁记录 Tab 的注入契约：封禁列表由容器按 `activeOnly`（查询参数）取数后注入；
 * `activeOnly` 是查询键的一部分，一变即重新取数，故归容器持有；
 * 待解封目标（`pending`）只作用于一处的弹窗开合，留本组件。
 */
export interface PlayersBansTabViewProps {
  /** 封禁记录（缺省或空数组渲染空态）。 */
  bans?: BanRecord[]
  /** 列表加载态。 */
  isLoading?: boolean
  /** 仅看生效中的封禁（受控：容器持有并作为查询参数）。 */
  activeOnly: boolean
  /** 筛选变更上报（容器写回并触发重新取数）。 */
  onActiveOnlyChange: (activeOnly: boolean) => void
  /** 危险操作（解封）是否放行：应用侧读角色等级后注入（组件库不持鉴权状态）。 */
  unbanAllowed?: boolean
  /** 解封上报；返回是否成功——成功才收起确认弹窗（失败保留弹窗便于重试，与原页一致）。 */
  onUnban: (name: string) => Promise<boolean>
}

/** 封禁记录 Tab（FR-054）：记录表 + 作用域列 + 行内解封（二次确认，scope=group 角色门禁）。 */
export function PlayersBansTabView({
  bans,
  isLoading = false,
  activeOnly,
  onActiveOnlyChange,
  unbanAllowed,
  onUnban,
}: PlayersBansTabViewProps) {
  const { t } = useTranslation()
  const [pending, setPending] = useState<string | null>(null)

  const doUnban = async () => {
    if (!pending) return
    const target = pending
    // 原页仅在 mutation 成功回调里 setPending(null)：失败保留弹窗，便于用户重试或取消。
    if (await onUnban(target)) setPending(null)
  }

  const scopeLabel = (scope: string) => t(`players.scope_${scope}`, { defaultValue: scope })

  return (
    <div>
      <label className="flex items-center gap-2 text-sm mb-3">
        <Checkbox
          checked={activeOnly}
          onCheckedChange={(v) => onActiveOnlyChange(v === true)}
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

      {/* 解封二次确认：scope 与原页一致（组级），allowed 由容器注入——不放宽、也不新增门禁。 */}
      <DangerConfirm
        open={pending !== null}
        title={t('players.unbanTitle')}
        description={t('players.unbanConfirm', { player: pending || '' })}
        confirmLabel={t('players.unban')}
        scope="group"
        allowed={unbanAllowed}
        onConfirm={() => void doUnban()}
        onCancel={() => setPending(null)}
      />
    </div>
  )
}

// ── 白名单 ──

/**
 * 白名单 Tab 的注入契约：白名单查询结果（含探针可用性）由容器按 `instanceId` 取数后注入；
 * `instanceId` 是查询键（也是增删 mutation 的作用域），故归容器持有；
 * 添加输入草稿随本 Tab 卸载而清空，留本组件。
 */
export interface PlayersWhitelistTabViewProps {
  /** 后端子服候选（白名单是实例原生能力，代理实例不支持）。 */
  backends: InstanceInfo[]
  /** 当前查询实例 id（容器已解析「未选即首个」）；null 表示无候选。 */
  instanceId: number | null
  /** 切换查询目标上报（容器写回并触发重新取数）。 */
  onInstanceChange: (id: number) => void
  /** 白名单查询结果。 */
  whitelist?: WhitelistResult
  /** 查询加载态。 */
  isLoading?: boolean
  /** 查询失败（显示错误 + 重试，不落空表）。 */
  isError?: boolean
  /** 重试查询上报。 */
  onRefresh: () => void
  /** 增删在途：禁用添加按钮。 */
  pending?: boolean
  /** 添加上报；返回是否成功——成功才清空输入（失败保留便于重试，与原页一致）。 */
  onAdd: (player: string) => Promise<boolean>
  /** 移除上报（结果提示由容器决定）。 */
  onRemove: (player: string) => Promise<void>
}

/** 白名单 Tab（FR-054 / FR-067）：单后端白名单的查看、添加与移除，含探针不可达与查询失败的降级。 */
export function PlayersWhitelistTabView({
  backends,
  instanceId,
  onInstanceChange,
  whitelist,
  isLoading = false,
  isError = false,
  onRefresh,
  pending = false,
  onAdd,
  onRemove,
}: PlayersWhitelistTabViewProps) {
  const { t } = useTranslation()
  const [name, setName] = useState('')

  const add = async (e: FormEvent) => {
    e.preventDefault()
    const player = name.trim()
    if (!player) return
    // 原页仅在 mutation 成功回调里 setName('')：失败保留输入，便于原样重试。
    if (await onAdd(player)) setName('')
  }

  return (
    <div>
      <div className="flex items-center gap-2 mb-4">
        <label className="text-sm font-medium">{t('players.selectBackend')}</label>
        <Select
          value={instanceId === null ? '' : String(instanceId)}
          onValueChange={(v) => onInstanceChange(Number(v))}
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

      {instanceId === null ? (
        <p className="text-muted-foreground text-sm">{t('players.noBackends')}</p>
      ) : (
        <>
          <form onSubmit={(e) => void add(e)} className="flex gap-2 mb-4 max-w-md">
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              className="flex-1 px-3 py-2 border rounded-md bg-background text-sm"
              placeholder={t('players.whitelistAddPlaceholder')}
            />
            <Button type="submit" disabled={pending || !name.trim()}>
              {t('players.whitelistAdd')}
            </Button>
          </form>

          {isLoading ? (
            <p className="text-muted-foreground">{t('common.loading')}</p>
          ) : isError ? (
            /* 查询失败不落空表：显式错误提示 + 重试（复用既有 common.error / common.refresh 键）。 */
            <div className="flex items-center gap-3">
              <p className="text-sm text-destructive">{t('common.error')}</p>
              <Button type="button" variant="outline" size="sm" onClick={onRefresh}>
                {t('common.refresh')}
              </Button>
            </div>
          ) : whitelist && !whitelist.available ? (
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
                  {whitelist?.players.map((p) => (
                    <TableRow key={p}>
                      <TableCell className="font-medium">{p}</TableCell>
                      <TableCell className="text-right">
                        <Button variant="link" size="xs" className="h-auto p-0 text-status-danger hover:text-status-danger" onClick={() => void onRemove(p)}>
                          {t('common.delete')}
                        </Button>
                      </TableCell>
                    </TableRow>
                  ))}
                  {(!whitelist || whitelist.players.length === 0) && (
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

/**
 * 玩家管理页的注入契约（ADR-097 b 范式）：**不取数、不发请求、不弹 toast、不建订阅**。
 *
 * 受控边界：
 * - 四块数据（在线名册 / 实时事件三件套 / 封禁记录 / 白名单）经 props 注入，容器调用
 *   `useOnlinePlayers` / `usePlayerEvents` / `useBans` / `useWhitelist` / `useInstances` 取数；
 * - 写动作（踢/封、解封、白名单增删）以回调上报，其中解封与白名单添加回传 `Promise<boolean>`，
 *   由本组件据返回值决定是否收起弹窗/清空输入；mutation、结果 toast 由容器决定；
 * - **归容器**的受控状态（都是「会触发取数/换订阅」的查询语义）：
 *   ① `tab` —— 原先四个 Tab 的内容组件各自挂载，未激活即不取数（在线名册还带 10s 轮询、
 *      两个实例选择器会打 `/instances`）；视图受控化后 hook 归容器，改用 `enabled` 开关
 *      保住同一时机，tab 因此成为「会触发取数的状态」；
 *   ② `bansActiveOnly` —— `useBans` 的查询参数；
 *   ③ `whitelistInstanceId` —— `useWhitelist` / `useWhitelistAction` 的查询键与作用域；
 *   ④ `liveInstanceId` —— 实时事件 SSE 的订阅目标（订阅本身留在容器），非实时事件 Tab
 *      不保持订阅。以上三者原先随 Tab 卸载重置，上提后跨 Tab 切换保留（本批统一取舍）。
 * - **留本组件**的纯 UI 状态：子服筛选与勾选集合、踢/封确认弹窗与原因草稿、事件面板的
 *  筛选/暂停快照/清空水位、解封待确认目标、白名单输入草稿——它们只影响本 Tab 的展示，
 *  且随 Tab 切换（区块按 Tab 条件挂载）重置，与原页一致；
 * - 解封的门禁语义不变：`DangerConfirm` 仍是 `scope="group"`，`unbanAllowed` 由容器
 *   读角色等级注入（包内不持鉴权状态，也不默认放行）。
 */
export interface PlayersPageViewProps {
  /** 当前 Tab（受控：容器据此门控各 Tab 的查询与订阅）。 */
  tab: PlayerTab
  /** Tab 切换上报。 */
  onTabChange: (tab: PlayerTab) => void
  /** 在线玩家聚合结果（容器按 `tab === 'online'` 门控取数）。 */
  online?: OnlinePlayersResult
  /** 在线名册加载态。 */
  onlineLoading?: boolean
  /** 踢/封在途（禁用行内与批量按钮）。 */
  playerActionPending?: boolean
  /** 踢出/封禁上报（容器循环提交并汇总 succeeded/failed 文案）。 */
  onPlayerAction: (request: PlayerActionRequest) => Promise<void>
  /** 实时事件 Tab 的实例候选（容器按 `tab === 'live'` 门控取数）。 */
  liveInstances: InstanceInfo[]
  /** 实时事件的订阅目标实例 id（容器解析「未选即首个」）；null 表示无候选。 */
  liveInstanceId: number | null
  /** 订阅目标切换上报（容器换订阅）。 */
  onLiveInstanceChange: (id: number) => void
  /** 探针是否在位连接（降级提示用）。 */
  probeConnected: boolean
  /** 实时在线名册（订阅结果，由容器注入）。 */
  roster: RosterEntry[]
  /** 最近事件流（订阅结果，由容器注入）。 */
  events: PlayerEventRow[]
  /** 封禁记录（容器按 `tab === 'bans'` 与 `bansActiveOnly` 门控取数）。 */
  bans?: BanRecord[]
  /** 封禁列表加载态。 */
  bansLoading?: boolean
  /** 是否仅看生效中的封禁（受控查询参数）。 */
  bansActiveOnly: boolean
  /** 筛选变更上报。 */
  onBansActiveOnlyChange: (activeOnly: boolean) => void
  /** 解封是否放行（容器读角色等级后注入）。 */
  unbanAllowed?: boolean
  /** 解封上报；成功才收起确认弹窗。 */
  onUnban: (name: string) => Promise<boolean>
  /** 白名单的后端子服候选（容器按 `tab === 'whitelist'` 门控取数）。 */
  whitelistBackends: InstanceInfo[]
  /** 白名单查询目标实例 id（容器解析「未选即首个」）；null 表示无候选。 */
  whitelistInstanceId: number | null
  /** 查询目标切换上报。 */
  onWhitelistInstanceChange: (id: number) => void
  /** 白名单查询结果。 */
  whitelist?: WhitelistResult
  /** 查询加载态。 */
  whitelistLoading?: boolean
  /** 查询失败态（显示错误 + 重试）。 */
  whitelistError?: boolean
  /** 重试查询上报。 */
  onRetryWhitelist: () => void
  /** 白名单增删在途。 */
  whitelistPending?: boolean
  /** 添加白名单上报；成功才清空输入。 */
  onWhitelistAdd: (player: string) => Promise<boolean>
  /** 移除白名单上报。 */
  onWhitelistRemove: (player: string) => Promise<void>
}

/**
 * 玩家管理页（FR-054 玩家治理 + FR-066 实时事件）：经各后端探针聚合在线玩家、踢/封/解封、
 * 白名单与封禁记录（FR-067 退役 RCON）。四个 Tab 的宿主。
 */
export function PlayersPageView({
  tab,
  onTabChange,
  online,
  onlineLoading = false,
  playerActionPending = false,
  onPlayerAction,
  liveInstances,
  liveInstanceId,
  onLiveInstanceChange,
  probeConnected,
  roster,
  events,
  bans,
  bansLoading = false,
  bansActiveOnly,
  onBansActiveOnlyChange,
  unbanAllowed,
  onUnban,
  whitelistBackends,
  whitelistInstanceId,
  onWhitelistInstanceChange,
  whitelist,
  whitelistLoading = false,
  whitelistError = false,
  onRetryWhitelist,
  whitelistPending = false,
  onWhitelistAdd,
  onWhitelistRemove,
}: PlayersPageViewProps) {
  const { t } = useTranslation()

  return (
    // 全量对齐：外壳与页头改用布局层原语。原为裸 <div> + 手写页头，且无 data-page。
    // 下方的手写 tab（下划线式）本次保留——把它换成布局层的 Tabs 属「页内导航统一」，
    // 与骨架对齐是两件事，不混在一次改动里。
    <PageShell data-page="players">
      <PageHeader title={t('players.title')} description={t('players.subtitle')} />

      <div className="flex gap-1 mb-4 border-b">
        {PLAYER_TABS.map((key) => (
          <button
            key={key}
            onClick={() => onTabChange(key)}
            className={`px-3 py-2 text-sm -mb-px border-b-2 ${
              tab === key ? 'border-primary text-foreground font-medium' : 'border-transparent text-muted-foreground hover:text-foreground'
            }`}
          >
            {t(`players.tab_${key}`)}
          </button>
        ))}
      </div>

      {/* 四个区块按 Tab 条件挂载（与原页一致）：区块内的本地 UI 状态随切换重置；
          数据侧由容器的 enabled 开关保住「未激活不取数/不订阅」的时机。 */}
      {tab === 'online' && (
        <PlayersOnlineTabView
          online={online}
          isLoading={onlineLoading}
          actionPending={playerActionPending}
          onPlayerAction={onPlayerAction}
        />
      )}
      {tab === 'live' && (
        <PlayersLiveTabView
          instances={liveInstances}
          instanceId={liveInstanceId}
          onInstanceChange={onLiveInstanceChange}
          connected={probeConnected}
          roster={roster}
          events={events}
        />
      )}
      {tab === 'bans' && (
        <PlayersBansTabView
          bans={bans}
          isLoading={bansLoading}
          activeOnly={bansActiveOnly}
          onActiveOnlyChange={onBansActiveOnlyChange}
          unbanAllowed={unbanAllowed}
          onUnban={onUnban}
        />
      )}
      {tab === 'whitelist' && (
        <PlayersWhitelistTabView
          backends={whitelistBackends}
          instanceId={whitelistInstanceId}
          onInstanceChange={onWhitelistInstanceChange}
          whitelist={whitelist}
          isLoading={whitelistLoading}
          isError={whitelistError}
          onRefresh={onRetryWhitelist}
          pending={whitelistPending}
          onAdd={onWhitelistAdd}
          onRemove={onWhitelistRemove}
        />
      )}
    </PageShell>
  )
}

export default PlayersPageView

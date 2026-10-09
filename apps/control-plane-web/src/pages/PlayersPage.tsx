// 视图已迁至 @jianmanager/ui（ADR-097）；本层只做四块数据取数、SSE 订阅、写动作、角色门禁注入与 toast。
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
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
  type PlayerActionResult,
} from '@/api/players'
import { useDangerPermission } from '@/lib/danger'
import {
  PlayersPageView,
  type PlayerActionRequest,
  type PlayerTab,
} from '@/components/views/players/PlayersPageView'

/**
 * 玩家管理页容器（ADR-097 b 范式）：四块数据取数、实时事件 SSE 订阅、全部写动作与解封的角色门禁
 * 在这里决定；四个 Tab 的展示、Tab 选中与各区块内的本地 UI 状态交共享视图。
 *
 * 上提的受控状态（都是「会触发取数/换订阅」的查询语义；原先随 Tab 卸载重置，上提后跨 Tab 切换保留）：
 * - `tab`：原先四个 Tab 的内容组件各自挂载，未激活即不取数——在线名册还带 10s 轮询、两个实例选择器
 *   会打 `/instances`，故 hook 归本层后用 `enabled` 门控同一时机，tab 因此也是受控状态；
 * - `bansActiveOnly`：`useBans` 的查询参数，一变即重新取数；
 * - 两个实例选择（实时事件 / 白名单）：分别是 SSE 订阅目标与 `useWhitelist` 的查询键（也是增删作用域），
 *   非激活 Tab 传 null，不订阅也不查询。
 * 保留原路径与原默认导出，路由表（route-chunks 的 `/players`）与调用点无需改动。
 */
export default function PlayersPage() {
  const { t } = useTranslation()
  // 当前 Tab：本层据它门控四个区块的查询与订阅，保住原先「未激活的 Tab 不挂载、不取数」的时机。
  const [tab, setTab] = useState<PlayerTab>('online')

  // ── 在线玩家（FR-054 / FR-067） ──
  const { data: online, isLoading: onlineLoading } = useOnlinePlayers({ enabled: tab === 'online' })
  const kick = useKickPlayer()
  const ban = useBanPlayer()

  /**
   * 踢出/封禁：逐玩家一次请求，累计 succeeded/failed 后统一提示（与原页一致——单个失败不中断后续）。
   * 目标名字集合由视图组装（含同名去重：封禁按玩家名全局执行），本层只负责执行与结果文案。
   */
  const runPlayerAction = async ({ kind, names, reason }: PlayerActionRequest) => {
    const mutation = kind === 'kick' ? kick : ban
    let succeeded = 0
    let failed = 0
    for (const name of names) {
      try {
        const res: PlayerActionResult = await mutation.mutateAsync({ name, scope: { reason: reason || undefined } })
        succeeded += res.succeeded
        failed += res.failed
      } catch {
        failed += 1
      }
    }
    const message = t('players.actionResult', { succeeded, failed })
    if (failed > 0) toast.error(message)
    else toast.success(message)
  }

  // ── 实时事件（FR-066）：SSE 订阅留在本层；非实时事件 Tab 传 null，断开且不重连 ──
  const { data: instances } = useInstances(undefined, tab === 'live')
  const liveInstances = useMemo(() => instances ?? [], [instances])
  const [liveInstanceId, setLiveInstanceId] = useState<number | null>(null)
  // 未选即首个实例（列表异步到达后自动落位，与原先一致）。
  const liveEffectiveId = liveInstanceId ?? liveInstances[0]?.id ?? null
  const { connected, roster, events } = usePlayerEvents(tab === 'live' ? liveEffectiveId : null)

  // ── 封禁记录 ──
  const [bansActiveOnly, setBansActiveOnly] = useState(false)
  const { data: bans, isLoading: bansLoading } = useBans({ active: bansActiveOnly }, { enabled: tab === 'bans' })
  const unban = useUnbanPlayer()
  // 解封走 DangerConfirm scope=group 的前端角色门禁：包内视图不持鉴权状态，判定结果由本层注入。
  const { allowed: unbanAllowed } = useDangerPermission('group')

  /** 解封：成功返回 true（视图据此收起确认弹窗），失败保留弹窗便于重试——与原页 onSuccess 语义一致。 */
  const handleUnban = async (name: string) => {
    try {
      await unban.mutateAsync({ name })
      toast.success(t('players.unbanned', { player: name }))
      return true
    } catch {
      toast.error(t('common.error'))
      return false
    }
  }

  // ── 白名单（FR-054 / FR-067）：后端实例候选与查询/增删都按 Tab 门控 ──
  const { data: backendInstances } = useInstances({ role: 'backend' }, tab === 'whitelist')
  const whitelistBackends = useMemo(() => backendInstances ?? [], [backendInstances])
  const [whitelistInstanceId, setWhitelistInstanceId] = useState<number | null>(null)
  const whitelistEffectiveId = whitelistInstanceId ?? whitelistBackends[0]?.id ?? null
  // 非白名单 Tab 传 null：查询的 enabled 与 mutation 的作用域同时失效，不发请求。
  const whitelistTargetId = tab === 'whitelist' ? whitelistEffectiveId : null
  const {
    data: whitelist,
    isLoading: whitelistLoading,
    isError: whitelistError,
    refetch: refetchWhitelist,
  } = useWhitelist(whitelistTargetId)
  const whitelistAction = useWhitelistAction(whitelistTargetId)

  /** 添加白名单：成功返回 true（视图据此清空输入），失败保留输入便于原样重试。 */
  const handleWhitelistAdd = async (player: string) => {
    try {
      await whitelistAction.mutateAsync({ action: 'add', player })
      toast.success(t('players.whitelistAdded', { player }))
      return true
    } catch {
      toast.error(t('common.error'))
      return false
    }
  }

  const handleWhitelistRemove = async (player: string) => {
    try {
      await whitelistAction.mutateAsync({ action: 'remove', player })
      toast.success(t('players.whitelistRemoved', { player }))
    } catch {
      toast.error(t('common.error'))
    }
  }

  return (
    <PlayersPageView
      tab={tab}
      onTabChange={setTab}
      online={online}
      onlineLoading={onlineLoading}
      playerActionPending={kick.isPending || ban.isPending}
      onPlayerAction={runPlayerAction}
      liveInstances={liveInstances}
      liveInstanceId={liveEffectiveId}
      onLiveInstanceChange={setLiveInstanceId}
      probeConnected={connected}
      roster={roster}
      events={events}
      bans={bans}
      bansLoading={bansLoading}
      bansActiveOnly={bansActiveOnly}
      onBansActiveOnlyChange={setBansActiveOnly}
      unbanAllowed={unbanAllowed}
      onUnban={handleUnban}
      whitelistBackends={whitelistBackends}
      whitelistInstanceId={whitelistEffectiveId}
      onWhitelistInstanceChange={setWhitelistInstanceId}
      whitelist={whitelist}
      whitelistLoading={whitelistLoading}
      whitelistError={whitelistError}
      onRetryWhitelist={() => void refetchWhitelist()}
      whitelistPending={whitelistAction.isPending}
      onWhitelistAdd={handleWhitelistAdd}
      onWhitelistRemove={handleWhitelistRemove}
    />
  )
}

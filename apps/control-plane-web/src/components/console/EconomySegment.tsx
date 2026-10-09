import { useCallback, useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { dispatchBusiness, fetchBusinessManifest, type BusinessResult } from '@/api/business'
import { fetchEconomyEvents, fetchEconomyLeaderboard, fetchEconomyMirror, type EconomyLeaderboardRow, type EconomyMirrorRow } from '@/api/economy'
import { toLedgerRows } from '@/lib/economy-view'
import EconomySegmentView from '@/components/views/instances/EconomySegment'

/**
 * 经济定制页的应用接线层（ADR-097 a 范式）。
 *
 * 视图本体是受控视图（见 components/views）；本层做四件事：按 manifest 发现经济能力、三块查询
 * （余额镜像 / 排行 / 事件流）、经济写动作下发。保留同路径的默认导出与同一套 props，
 * 调用点无需改动。
 */
export default function EconomySegment({ instanceId }: { instanceId: number }) {
  // 经济能力发现：复用 business manifest，检查是否存在 economy 域（探针未连 / 无经济插件则降级）。
  const manifestQuery = useQuery({
    queryKey: ['business-manifest', instanceId],
    queryFn: () => fetchBusinessManifest(instanceId),
    enabled: !!instanceId,
  })
  const economyAvailable = useMemo(() => {
    const m = manifestQuery.data
    return !!m?.available && !!m.output?.domains?.economy
  }, [manifestQuery.data])

  // 三块查询各自「点了查询才落参」（null = 未查询），与视图内的表单态解耦。
  const [balanceParams, setBalanceParams] = useState<{ player: string; currency: string } | null>(null)
  const balanceQuery = useQuery({
    queryKey: ['economy-mirror', balanceParams],
    queryFn: () =>
      fetchEconomyMirror({
        player: balanceParams?.player || undefined,
        currency: balanceParams?.currency || undefined,
      }),
    enabled: balanceParams !== null,
  })

  const [leaderboardParams, setLeaderboardParams] = useState<{
    currency: string
    zone: string
    node: string
  } | null>(null)
  const leaderboardQuery = useQuery({
    queryKey: ['economy-leaderboard', leaderboardParams],
    queryFn: () =>
      fetchEconomyLeaderboard({
        currency: leaderboardParams!.currency,
        zone: leaderboardParams?.zone || undefined,
        node: leaderboardParams?.node || undefined,
        limit: 50,
      }),
    // 排行须有货币（后端亦强制），无货币不发请求。
    enabled: leaderboardParams !== null && leaderboardParams.currency !== '',
  })

  const [ledgerParams, setLedgerParams] = useState<{ player: string; currency: string } | null>(null)
  const eventsQuery = useQuery({
    queryKey: ['economy-events', ledgerParams],
    queryFn: () => fetchEconomyEvents({ limit: 200 }),
    enabled: ledgerParams !== null,
  })
  // 经济事件流（domain=economy）；前端解析 envelope payload → 流水行。
  const ledgerRows = useMemo(() => toLedgerRows(eventsQuery.data ?? []), [eventsQuery.data])

  const dispatch = useCallback(
    async (payload: {
      action: 'transfer' | 'deposit' | 'withdraw'
      payload: string
      reason?: string
      operationId: string
    }): Promise<BusinessResult> => {
      return dispatchBusiness(instanceId, 'economy', payload.action, payload.payload, {
        write: true,
        operationId: payload.operationId,
        reason: payload.reason,
      })
    },
    [instanceId],
  )

  return (
    <EconomySegmentView
      manifestLoading={manifestQuery.isLoading}
      manifestRefreshing={manifestQuery.isFetching}
      economyAvailable={economyAvailable}
      manifestError={manifestQuery.data?.error}
      onRefreshManifest={() => void manifestQuery.refetch()}
      balanceRows={(balanceQuery.data ?? []) as EconomyMirrorRow[]}
      balanceLoading={balanceQuery.isFetching}
      balanceError={balanceQuery.isError}
      onQueryBalance={setBalanceParams}
      leaderboardRows={(leaderboardQuery.data ?? []) as EconomyLeaderboardRow[]}
      leaderboardLoading={leaderboardQuery.isFetching}
      leaderboardError={leaderboardQuery.isError}
      onQueryLeaderboard={setLeaderboardParams}
      ledgerRows={ledgerRows}
      ledgerLoading={eventsQuery.isFetching}
      ledgerError={eventsQuery.isError}
      onQueryLedger={setLedgerParams}
      onDispatch={dispatch}
    />
  )
}

import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { Users } from 'lucide-react'

import { cn } from '@jianmanager/ui'
import { Panel } from '@jianmanager/ui/components/panel'
import { useOnlinePlayers } from '@/api/players'
import { useTopology } from '@/api/topology'

/**
 * 跨服玩家分布 + 本代理已注册子服的在线总数（FR-449 §2.2 块 2）。
 *
 * 由 `players` 能力在 **proxy 语义**下呈现（FR-445 §2.3：backend 的 `players` = 单服实名名单，
 * proxy 的 `players` = 谁在哪个子服）。以该 proxy 的 `registrations` 为唯一真源聚合各后端在线玩家；
 * 探针不可达的子服显「未知」而非 0，且不计入总数。
 *
 * 单一归属：本面板只挂在 `players` 页签（proxy 形态），`bcTopology`（子服拓扑）页签不再重复渲染，
 * 避免同一面板被两个页签各挂一份。
 */
export default function BcPlayersPanel({ instanceId }: { instanceId: number }) {
  const { t } = useTranslation()
  const { data: topology } = useTopology()
  const { data: online } = useOnlinePlayers()

  const proxy = topology?.proxies.find((p) => p.id === instanceId)
  const registrations = useMemo(() => proxy?.registrations ?? [], [proxy])
  const backendIds = useMemo(() => new Set(registrations.map((r) => r.backendId)), [registrations])

  // 跨服玩家：仅统计注册到本 proxy 的后端（按 instanceId 归属聚拢）。
  const playersByBackend = useMemo(() => {
    const map = new Map<number, string[]>()
    for (const p of online?.players ?? []) {
      if (!backendIds.has(p.instanceId)) continue
      const list = map.get(p.instanceId) ?? []
      list.push(p.name)
      map.set(p.instanceId, list)
    }
    return map
  }, [online?.players, backendIds])
  const totalPlayers = useMemo(
    () => [...playersByBackend.values()].reduce((sum, list) => sum + list.length, 0),
    [playersByBackend],
  )
  // 探针不可达的后端（玩家数未知，不计入总数）。
  const unavailableBackends = useMemo(
    () => new Set((online?.backends ?? []).filter((b) => !b.available).map((b) => b.instanceId)),
    [online?.backends],
  )
  // 仅统计「已注册到本 proxy」的不可达子服：全网不可达数与本 proxy 无关，不能混入口径。
  const unavailableRegistered = useMemo(
    () => registrations.filter((r) => unavailableBackends.has(r.backendId)).length,
    [registrations, unavailableBackends],
  )

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-auto p-4" data-testid="bc-players-panel">
      {/* 跨服玩家分布 + 本代理已注册子服的在线总数；标题与说明显式标注口径
          （口径仅含本 proxy 已注册子服，不含探针不可达子服）。 */}
      <Panel title={t('bc.playersTitle', { count: totalPlayers })} data-testid="bc-players">
        {registrations.length === 0 ? (
          <p className="p-3 text-xs text-muted-foreground">{t('bc.noSubServers')}</p>
        ) : (
          <div className="space-y-2 p-3 text-xs">
            {unavailableRegistered > 0 && (
              <p className="text-muted-foreground" data-testid="bc-players-caveat">
                {t('bc.playersTotalCaveat', { count: unavailableRegistered })}
              </p>
            )}
            {registrations.map((r) => {
              const names = playersByBackend.get(r.backendId) ?? []
              const unknown = unavailableBackends.has(r.backendId)
              return (
                <div key={r.id} className="flex flex-wrap items-center gap-2">
                  <span className="inline-flex min-w-32 items-center gap-1.5 text-muted-foreground">
                    <Users className="size-3.5" />
                    {r.backend?.name ?? `#${r.backendId}`}
                  </span>
                  {unknown ? (
                    <span className="text-muted-foreground">{t('bc.playersUnknown')}</span>
                  ) : names.length === 0 ? (
                    <span className="text-muted-foreground">{t('bc.playersNone')}</span>
                  ) : (
                    <span className="flex flex-wrap gap-1">
                      {names.map((n) => (
                        <span key={n} className={cn('rounded bg-muted px-1.5 py-0.5 font-mono text-[11px]')}>
                          {n}
                        </span>
                      ))}
                    </span>
                  )}
                </div>
              )
            })}
          </div>
        )}
      </Panel>
    </div>
  )
}

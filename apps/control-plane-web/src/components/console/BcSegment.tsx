import { useMemo } from 'react'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import { Server, Users } from 'lucide-react'

import { cn, instanceStatusLevel } from '@jianmanager/ui'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import { Panel } from '@jianmanager/ui/components/panel'
import { useOnlinePlayers } from '@/api/players'
import { useTopology } from '@/api/topology'
import GenericConfigSegment from './GenericConfigSegment'
import { ProcessPanel } from './ProcessPanel'

/**
 * BC 代理专属分段（FR-449）——不写死 BC 名，由画像 `bcTopology` 能力驱动。
 *
 * 四块内容：
 * 1. 子服列表 + 各自状态：以 `/topology` 的该 proxy `registrations` 为**唯一真源**
 *    （与拓扑/注册页同源，避免「详情页说的子服」与「拓扑页画的连线」两份数据漂移）。
 * 2. 跨服玩家分布 + 全网总数：聚合各后端在线玩家并按其所在子服分组（proxy 语义的 `players`），
 *    无探针的后端显「未知」而非 0（spec §5）。
 * 3. BC 自身运行指标：BC 也是 JVM 进程，复用 FR-450 的 {@link ProcessPanel}（字段相容）。
 * 4. BC 配置管理：复用 FR-451 的配置源机制（{@link GenericConfigSegment}）。
 */
export default function BcSegment({ instanceId }: { instanceId: number }) {
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

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-auto p-4" data-testid="bc-segment">
      {/* 1. 子服列表 + 各自状态（唯一真源：registrations）。 */}
      <Panel title={t('bc.subServers')} data-testid="bc-subservers">
        {registrations.length === 0 ? (
          <p className="p-3 text-xs text-muted-foreground">{t('bc.noSubServers')}</p>
        ) : (
          <ul className="divide-y">
            {registrations.map((r) => {
              const b = r.backend
              return (
                <li key={r.id} className="flex flex-wrap items-center gap-2 px-3 py-2 text-xs">
                  <Server className="size-3.5 shrink-0 text-muted-foreground" />
                  <Link to={`/instances/${r.backendId}`} className="font-medium hover:text-primary hover:underline">
                    {b?.name ?? `#${r.backendId}`}
                  </Link>
                  {r.alias && <span className="text-muted-foreground">({r.alias})</span>}
                  {b && (
                    <StatusBadge
                      level={instanceStatusLevel(b.status)}
                      label={t(`instances.${b.status.toLowerCase()}`, b.status)}
                      className="ml-auto"
                    />
                  )}
                  {b && b.serverPort > 0 && <span className="font-mono tabular-nums text-muted-foreground">:{b.serverPort}</span>}
                  {!r.enabled && (
                    <span className="rounded border px-1.5 py-0.5 text-[10px] text-muted-foreground">{t('common.disabled')}</span>
                  )}
                </li>
              )
            })}
          </ul>
        )}
      </Panel>

      {/* 2. 跨服玩家分布 + 全网总数。 */}
      <Panel title={t('bc.playersTitle', { count: totalPlayers })} data-testid="bc-players">
        {registrations.length === 0 ? (
          <p className="p-3 text-xs text-muted-foreground">{t('bc.noSubServers')}</p>
        ) : (
          <div className="space-y-2 p-3 text-xs">
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

      {/* 3. BC 自身运行指标（复用 FR-450 进程面板）。 */}
      <ProcessPanel instanceId={instanceId} />

      {/* 4. BC 配置管理（复用 FR-451 配置源）。 */}
      <GenericConfigSegment instanceId={instanceId} />
    </div>
  )
}

import { useMemo } from 'react'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import { Server } from 'lucide-react'

import { instanceStatusLevel } from '@jianmanager/ui'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import { Panel } from '@jianmanager/ui/components/panel'
import { useTopology } from '@/api/topology'

/**
 * BC 代理「子服拓扑」分段（FR-449）——不写死 BC 名，由画像 `bcTopology` 能力驱动。
 *
 * 单一归属（minor 5）：本分段只承载 **子服列表 + 各自状态**。跨服玩家分布归 `players`
 * 页签（{@link BcPlayersPanel}），BC 自身运行指标归 `process` 页签（{@link ProcessPanel}），
 * `config.yml` 归 `config` 页签（{@link GenericConfigSegment}）——每个子面板只在一个页签渲染，
 * 不再在同一实例的两个页签里各挂一份（避免重复拉取与重复 DOM）。
 *
 * 子服列表以 `/topology` 的该 proxy `registrations` 为**唯一真源**（与拓扑/注册页同源，
 * 避免「详情页说的子服」与「拓扑页画的连线」两份数据漂移）。
 */
export default function BcSegment({ instanceId }: { instanceId: number }) {
  const { t } = useTranslation()
  const { data: topology } = useTopology()

  const proxy = topology?.proxies.find((p) => p.id === instanceId)
  const registrations = useMemo(() => proxy?.registrations ?? [], [proxy])

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-auto p-4" data-testid="bc-segment">
      {/* 子服列表 + 各自状态（唯一真源：registrations）。 */}
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
    </div>
  )
}

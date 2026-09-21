import { useTranslation } from 'react-i18next'
import { Activity, Radio } from 'lucide-react'

import { Panel } from '@jianmanager/ui/components/panel'
import { cn } from '@jianmanager/ui'
import { useInstance } from '@/api/instances'
import { useServerState } from '@/api/serverState'

type Reachability = 'reachable' | 'unreachable' | 'unknown'

/**
 * 端口 + 主动健康检查面板（FR-450 §2.3.2）——不写死产品名。
 *
 * 实例声明的监听端口逐项列出；可达性按 FR-447 三态语义呈现（可达 / 不可达 / 未知），
 * **不报错、不装 -1**。主动探活（HTTP GET / TCP connect）执行侧尚未接入（spec §5 待定），
 * 当前可达性由探针/直探连接态派生，并在界面诚实标注口径。
 */
export function HealthPanel({ instanceId }: { instanceId: number }) {
  const { t } = useTranslation()
  const { data: inst } = useInstance(instanceId)
  const running = inst?.status === 'RUNNING'
  const { data: serverState } = useServerState(instanceId, true, 15_000)

  const reachability: Reachability = !inst
    ? 'unknown'
    : !running
      ? 'unreachable'
      : serverState?.connected || serverState?.available
        ? 'reachable'
        : 'unknown'

  const ports: Array<{ label: string; value: number }> = []
  if (inst?.serverPort) ports.push({ label: t('health.portGame'), value: inst.serverPort })
  if (inst?.queryPort) ports.push({ label: t('health.portQuery'), value: inst.queryPort })
  if (inst?.probePort) ports.push({ label: t('health.portProbe'), value: inst.probePort })

  const tone: Record<Reachability, string> = {
    reachable: 'border-status-success/40 bg-status-success/10 text-status-success',
    unreachable: 'border-status-danger/40 bg-status-danger/10 text-status-danger',
    unknown: 'border-muted bg-muted/40 text-muted-foreground',
  }

  return (
    <Panel title={t('health.title')} data-testid="health-panel">
      <div className="space-y-2 p-2 text-xs">
        <div className="flex flex-wrap items-center gap-3">
          <span className={cn('inline-flex items-center gap-1.5 rounded-md border px-2 py-1', tone[reachability])} data-health-state={reachability}>
            <Radio className="size-3.5" />
            {t(`health.state.${reachability}`)}
          </span>
          <span className="text-muted-foreground">{t('health.probeHint')}</span>
        </div>

        <div className="grid grid-cols-1 gap-2 sm:grid-cols-3">
          {ports.length === 0 ? (
            <p className="text-muted-foreground">{t('health.noPort')}</p>
          ) : (
            ports.map((p) => (
              <div key={p.label} className="rounded-md border bg-card p-2">
                <div className="flex items-center gap-1.5 text-muted-foreground">
                  <Activity className="size-3.5 shrink-0" />
                  <span>{p.label}</span>
                </div>
                <p className="mt-1 font-mono text-sm font-semibold tabular-nums">:{p.value}</p>
              </div>
            ))
          )}
        </div>
      </div>
    </Panel>
  )
}

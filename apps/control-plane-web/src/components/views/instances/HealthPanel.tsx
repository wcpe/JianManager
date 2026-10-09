import { useTranslation } from 'react-i18next'
import { Activity, Radio } from 'lucide-react'

import { Panel } from '@jianmanager/ui/components/panel'
import { cn } from '@jianmanager/ui'

type Reachability = 'reachable' | 'unreachable' | 'timeout' | 'unknown'

/**
 * 端口 + 主动健康检查面板（FR-450 §2.3.2）——不写死产品名。
 *
 * 实例声明的监听端口逐项列出；可达性按 FR-447 三态语义呈现（可达 / 不可达 / 超时，
 * 另加「未知」= 无探针），**不报错、不装 -1**。
 *
 * 主动探活（HTTP GET / TCP connect）+ 周期/超时参数执行侧尚未接入（spec §5 待定），
 * 当前可达性由探针/直探连接态派生（connected=探针在位；available=本次取回成功；
 * 在位但取不回 = 超时），并在界面诚实标注口径（见 `health.probeHint` 文案）。
 *
 * 受控视图（ADR-097 a 范式）：不取数——实例状态、端口与连接态经 props 注入。
 * 「实例尚未加载」与「实例存在但字段为空」是两件事，故用 `status` 是否给出区分：
 * 前者归「未知」，后者照常按未运行/无端口呈现。
 */
export interface HealthPanelProps {
  /** 实例运行状态；undefined = 实例尚未加载（判为未知态）。 */
  status?: string
  /** 声明的监听端口；未声明的不渲染。 */
  serverPort?: number
  queryPort?: number
  probePort?: number
  /** 探针/直探连接态：available=本次取回成功、connected=探针在位。 */
  serverState?: { available?: boolean; connected?: boolean }
}

export function HealthPanel({ status, serverPort, queryPort, probePort, serverState }: HealthPanelProps) {
  const { t } = useTranslation()
  const running = status === 'RUNNING'

  // 四态：未加载 → 未知；未运行 → 不可达；探针在位且本次取回成功 → 可达；探针在位但取不回 → 超时；无探针 → 未知。
  const reachability: Reachability = status === undefined
    ? 'unknown'
    : !running
      ? 'unreachable'
      : serverState?.available
        ? 'reachable'
        : serverState?.connected
          ? 'timeout'
          : 'unknown'

  const ports: Array<{ label: string; value: number }> = []
  if (serverPort) ports.push({ label: t('health.portGame'), value: serverPort })
  if (queryPort) ports.push({ label: t('health.portQuery'), value: queryPort })
  if (probePort) ports.push({ label: t('health.portProbe'), value: probePort })

  const tone: Record<Reachability, string> = {
    reachable: 'border-status-success/40 bg-status-success/10 text-status-success',
    unreachable: 'border-status-danger/40 bg-status-danger/10 text-status-danger',
    timeout: 'border-status-warning/40 bg-status-warning/10 text-status-warning',
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

import { useTranslation } from 'react-i18next'
import { Gauge } from 'lucide-react'

import { Button } from '@jianmanager/ui/components/button'
import { Panel } from '@jianmanager/ui/components/panel'
import { useInstanceQuota } from '@/api/quota'

interface QuotaPanelProps {
  /** 实例 DB ID。 */
  instanceId: number
}

/**
 * 实例「配额」区（FR-467）：限额（含来源）+ 实时用量 + 强制状态。
 *
 * 为什么需要这一区：限额此前只在「创建时/启动时」生效，运行期完全不强制——
 * 运维看不到「这个实例的 CPU/内存上限是多少、现在用了多少、有没有被强制」。
 * 此处三件事一次讲清，并把**来源**标出来（实例级 / 组派生 / 不限），
 * 否则运维改实例级限额却发现没生效（被组值覆盖或相反）会无从解释。
 *
 * 还展示**待收紧限额**与 `enforceStateScope`：`enforced*` 是**本进程内**的观察结果
 * （进程重启后重新累积），不写明会被误读为「从未超限」；throttle 档登记的收紧限额
 * 只在下次启动生效，不展示则运维无法判断「到底有没有待生效的改动」。
 *
 * 诚实边界（与后端一致）：非 docker 模式不支持内核级限流，CPU 超限只能告警；
 * 面板如实标注，不假装有限流能力。
 */
export default function QuotaPanel({ instanceId }: QuotaPanelProps) {
  const { t } = useTranslation()
  // 四态齐全：loading / error / 有数据 /（本面板无空态语义，字段恒有值）。
  // 原来只取 isLoading 且用 `isLoading || !quota` 兜底：请求失败后 isLoading 转 false
  // 但 data 恒为 undefined → 面板**永久停在「加载中…」**，运维永远等不到结果也看不到失败原因。
  const { data: quota, isLoading, isError, refetch } = useInstanceQuota(instanceId)

  const pendingThrottle = !!quota && (quota.throttleCpuLimit > 0 || quota.throttleMemLimitMb > 0)

  return (
    <Panel
      data-testid="quota-panel"
      title={t('serverConsole.quotaTitle')}
      icon={<Gauge className="size-3.5" />}
      bodyClassName="p-2.5"
    >
      <p className="pb-1.5 text-[11px] text-muted-foreground">{t('serverConsole.quotaHint')}</p>
      {isLoading ? (
        <p className="py-1 text-[11px] text-muted-foreground">{t('common.loading')}</p>
      ) : isError ? (
        /* 读失败必须显式说出，绝不回落到「加载中」或「不限」——后者会让运维以为配额没生效。 */
        <div data-testid="quota-error" className="flex items-center gap-2 py-1">
          <p className="text-[11px] text-status-danger">{t('serverConsole.quotaLoadFailed')}</p>
          <Button type="button" variant="outline" size="sm" onClick={() => void refetch()}>
            {t('common.refresh')}
          </Button>
        </div>
      ) : !quota ? (
        <p className="py-1 text-[11px] text-muted-foreground">{t('common.noData')}</p>
      ) : (
        <div className="space-y-2 text-xs">
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
            <span data-testid="quota-mode" className="rounded-full bg-muted px-2 py-0.5 text-[11px] text-muted-foreground">
              {t(`serverConsole.quotaMode.${quota.enforceMode}`, { defaultValue: quota.enforceMode })}
            </span>
            {!quota.supportedThrottle && (
              <span data-testid="quota-throttle-unsupported" className="text-[11px] text-status-warning">
                {t('serverConsole.quotaThrottleUnsupported')}
              </span>
            )}
            {quota.sampleNote && <span className="text-[11px] text-muted-foreground">{quota.sampleNote}</span>}
          </div>
          <ul className="space-y-1">
            <QuotaRow
              label={t('serverConsole.quotaCpu')}
              usage={`${quota.cpuPercent.toFixed(1)}%`}
              limit={quota.cpuCores > 0 ? `${quota.cpuCores} ${t('serverConsole.quotaCores')}` : t('serverConsole.quotaUnlimited')}
              source={quota.cpuSource}
              enforced={quota.enforcedCpu}
            />
            <QuotaRow
              label={t('serverConsole.quotaMemory')}
              usage={formatMiB(quota.rssBytes)}
              limit={quota.memLimitMb > 0 ? `${quota.memLimitMb} MiB` : t('serverConsole.quotaUnlimited')}
              source={quota.memSource}
              enforced={quota.enforcedMem}
            />
            <QuotaRow
              label={t('serverConsole.quotaDisk')}
              usage={formatMiB(quota.diskBytes)}
              limit={quota.diskLimitMb > 0 ? `${quota.diskLimitMb} MiB` : t('serverConsole.quotaUnlimited')}
              source={quota.diskSource}
              enforced={quota.enforcedDisk}
            />
          </ul>
          {/* 待收紧限额：throttle 档触发时登记、下次启动生效（R7：恢复即清零）。
              不展示则「已登记限流」在 UI 上不可见，运维无法判断重启是否会有变化。 */}
          {pendingThrottle && (
            <p data-testid="quota-throttle-pending" className="text-[11px] text-status-warning">
              {t('serverConsole.quotaThrottlePending', {
                cpu: quota.throttleCpuLimit > 0 ? `${quota.throttleCpuLimit} ${t('serverConsole.quotaCores')}` : t('serverConsole.quotaNone'),
                mem: quota.throttleMemLimitMb > 0 ? `${quota.throttleMemLimitMb} MiB` : t('serverConsole.quotaNone'),
              })}
            </p>
          )}
          {/* 强制状态作用域：本进程内存计数，重启后重新累积——不写明会把 false 读成「从未超限」。 */}
          {quota.enforceStateScope && (
            <p data-testid="quota-enforce-scope" className="text-[11px] text-muted-foreground">
              {t('serverConsole.quotaEnforceStateScope', { scope: quota.enforceStateScope })}
            </p>
          )}
          <p className="text-[11px] text-muted-foreground">{t('serverConsole.quotaWriteHint')}</p>
        </div>
      )}
    </Panel>
  )
}

function QuotaRow({
  label,
  usage,
  limit,
  source,
  enforced,
}: {
  label: string
  usage: string
  limit: string
  source: string
  enforced: boolean
}) {
  const { t } = useTranslation()
  return (
    <li className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
      <span className="w-12 shrink-0 text-muted-foreground">{label}</span>
      <span className="font-mono text-foreground">{usage}</span>
      <span className="text-muted-foreground">/ {limit}</span>
      <span className="rounded-full bg-muted px-1.5 py-0.5 text-[11px] text-muted-foreground">
        {t(`serverConsole.quotaSource.${source}`, { defaultValue: source })}
      </span>
      {enforced && (
        <span data-testid="quota-enforced" className="text-[11px] font-medium text-status-warning">
          {t('serverConsole.quotaEnforced')}
        </span>
      )}
    </li>
  )
}

/** 字节 → MiB/GiB 文本（用量列）。 */
function formatMiB(bytes: number): string {
  if (bytes <= 0) return '—'
  const mib = bytes / (1024 * 1024)
  if (mib < 1024) return `${mib.toFixed(1)} MiB`
  return `${(mib / 1024).toFixed(2)} GiB`
}

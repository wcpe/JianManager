import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Save, Terminal } from 'lucide-react'

import { Button } from '@jianmanager/ui/components/button'
import { Panel } from '@jianmanager/ui/components/panel'
import { useInstance, useUpdateInstance } from '@/api/instances'
import { ProcessPanel } from './ProcessPanel'

/**
 * 进程能力分段（FR-450）——不写死产品名，由画像 `process` 能力驱动
 * （新增原生二进制产品只需在注册表加一条描述符）。
 *
 * 承载：进程运行指标（{@link ProcessPanel}）+ 启动参数编辑（{@link LaunchParamsPanel}）。
 * 端口 + 健康检查归 `health` 页签（{@link HealthPanel}）独有，本分段**不再**内联，
 * 避免同一实例的 `process` 与 `health` 两个页签重复渲染 HealthPanel（minor 5 单一归属）。
 * 文件目录管理（files 能力）与基础 CPU 监控由 `files` 页签保留，不在此重复。
 */
export default function BinarySegment({ instanceId }: { instanceId: number }) {
  return (
    <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-auto p-4">
      <ProcessPanel instanceId={instanceId} />
      <LaunchParamsPanel instanceId={instanceId} />
    </div>
  )
}

/**
 * 启动参数编辑（FR-450 §2.3.4）：`startCommand` 明面可编辑（依赖 FR-451 配置源 API；
 * 此前仅在列表页的 EditInstanceConfigDialog 里存在，这里在详情页直接呈现）。
 * 变更对下次启动生效，故给显式提示，不做乐观生效假设。
 */
export function LaunchParamsPanel({ instanceId }: { instanceId: number }) {
  const { t } = useTranslation()
  const { data: inst } = useInstance(instanceId)
  const update = useUpdateInstance()
  const [command, setCommand] = useState('')
  const persistedCommand = inst?.startCommand ?? ''

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- 实例加载/刷新后同步草稿
    setCommand(persistedCommand)
  }, [instanceId, persistedCommand])

  const dirty = !!inst && command !== persistedCommand

  return (
    <Panel title={t('binary.launchTitle')} data-testid="launch-params-panel">
      <div className="space-y-2 p-2 text-xs">
        <label htmlFor={`launch-cmd-${instanceId}`} className="flex items-center gap-1.5 text-muted-foreground">
          <Terminal className="size-3.5" />
          {t('binary.launchCommand')}
        </label>
        <div className="flex items-center gap-2">
          <input
            id={`launch-cmd-${instanceId}`}
            className="min-w-0 flex-1 rounded-md border bg-background px-2 py-1.5 font-mono text-xs"
            value={command}
            onChange={(e) => setCommand(e.target.value)}
            placeholder={t('binary.launchPlaceholder')}
          />
          <Button
            size="sm"
            className="gap-1"
            disabled={!dirty || update.isPending}
            onClick={() => update.mutate({ id: instanceId, body: { startCommand: command } })}
          >
            <Save className="size-3.5" />
            {update.isPending ? t('common.saving') : t('common.save')}
          </Button>
        </div>
        <p className="text-muted-foreground">{t('binary.launchHint')}</p>
      </div>
    </Panel>
  )
}

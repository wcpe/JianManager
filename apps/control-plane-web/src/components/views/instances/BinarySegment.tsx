import { useEffect, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Save, Terminal } from 'lucide-react'

import { Button } from '@jianmanager/ui/components/button'
import { Panel } from '@jianmanager/ui/components/panel'

/**
 * 进程能力分段（FR-450）——不写死产品名，由画像 `process` 能力驱动
 * （新增原生二进制产品只需在注册表加一条描述符）。
 *
 * 承载：进程运行指标（由外壳经 `processSlot` 注入）+ 启动参数编辑（{@link LaunchParamsPanel}）。
 * 端口 + 健康检查归 `health` 页签（HealthPanel）独有，本分段**不再**内联，
 * 避免同一实例的 `process` 与 `health` 两个页签重复渲染（minor 5 单一归属）。
 * 文件目录管理（files 能力）与基础 CPU 监控由 `files` 页签保留，不在此重复。
 *
 * 受控视图（ADR-097）：进程指标块自带取数、由外壳注入；启动参数编辑所需的数据与
 * 保存动作经 props 透传。
 */
export interface BinarySegmentProps {
  /** 进程运行指标区（外壳注入，自带取数）。 */
  processSlot?: ReactNode
  /** 启动参数区的数据与回调。 */
  launch: LaunchParamsPanelProps
}

export default function BinarySegment({ processSlot, launch }: BinarySegmentProps) {
  return (
    <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-auto p-4">
      {processSlot}
      <LaunchParamsPanel {...launch} />
    </div>
  )
}

/**
 * 启动参数编辑（FR-450 §2.3.4）：`startCommand` 明面可编辑（依赖 FR-451 配置源 API；
 * 此前仅在列表页的 EditInstanceConfigDialog 里存在，这里在详情页直接呈现）。
 * 变更对下次启动生效，故给显式提示，不做乐观生效假设。
 *
 * 受控视图：已持久化的命令经 props 注入，保存经 `onSave` 上报；
 * 本地草稿在「注入值变化」时同步（首载与保存后刷新都走这条路）。
 */
export interface LaunchParamsPanelProps {
  /** 当前已持久化的启动命令（外壳取数注入）。 */
  startCommand: string
  /** 实例是否已加载；未加载时保存按钮不可用（避免把空草稿写回去）。 */
  loaded?: boolean
  /** 保存在途。 */
  saving?: boolean
  /** 保存启动命令。返回是否成功。 */
  onSave: (command: string) => Promise<boolean>
}

export function LaunchParamsPanel({ startCommand, loaded = true, saving = false, onSave }: LaunchParamsPanelProps) {
  const { t } = useTranslation()
  const [command, setCommand] = useState('')

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- 实例加载/刷新后同步草稿
    setCommand(startCommand)
  }, [startCommand])

  const dirty = loaded && command !== startCommand

  return (
    <Panel title={t('binary.launchTitle')} data-testid="launch-params-panel">
      <div className="space-y-2 p-2 text-xs">
        <label htmlFor="launch-cmd" className="flex items-center gap-1.5 text-muted-foreground">
          <Terminal className="size-3.5" />
          {t('binary.launchCommand')}
        </label>
        <div className="flex items-center gap-2">
          <input
            id="launch-cmd"
            className="min-w-0 flex-1 rounded-md border bg-background px-2 py-1.5 font-mono text-xs"
            value={command}
            onChange={(e) => setCommand(e.target.value)}
            placeholder={t('binary.launchPlaceholder')}
          />
          <Button
            size="sm"
            className="gap-1"
            disabled={!dirty || saving}
            onClick={() => void onSave(command)}
          >
            <Save className="size-3.5" />
            {saving ? t('common.saving') : t('common.save')}
          </Button>
        </div>
        <p className="text-muted-foreground">{t('binary.launchHint')}</p>
      </div>
    </Panel>
  )
}

import { useTranslation } from 'react-i18next'
import { useInstance } from '@/api/instances'
import { useTerminalToken } from '@/api/terminal'
import TerminalComponent from '@/components/Terminal'
import { Button } from '@/components/ui/button'

/**
 * 工作区终端面板：为单个实例打开终端（ADR-009 / FR-037）。
 * 复用一次性 token + xterm，逻辑与实例详情页「终端」Tab 一致：
 * 运行态用 write token，否则 read token 只读。
 */
interface TerminalPaneProps {
  /** 当前打开终端的实例 id */
  instanceId: number
  /**
   * 隐藏自带的面包屑/占位按钮工具栏。
   * 可组合工作区（FR-166）中由卡壳 {@link WorkspaceCard} 统一承载卡头，
   * 此时本组件只渲染终端区，避免双重头部。
   */
  hideHeader?: boolean
}

export default function TerminalPane({ instanceId, hideHeader = false }: TerminalPaneProps) {
  const { t } = useTranslation()
  const { data: instance } = useInstance(instanceId)
  const status = instance?.status ?? ''
  const isRunning = status === 'RUNNING'
  // 完全停机（STOPPED）的实例无进程可 attach：CP→Worker 拨号失败时终端会陷入
  // 「连上即断、重连重置计数」的死循环刷断连（FIX-B），故 STOPPED 一律展示静态占位、不连 WS。
  // STARTING/STOPPING/CRASHED 仍连终端（只读）以看启动/关服/崩溃输出，行为不变。
  const isStopped = status === 'STOPPED'
  // 仅在状态已知且非完全停机时请求 token / 挂载终端：STOPPED 与状态未知都不发起 WS
  // （enabled=false 连 token 都不取），避免对停机/加载中实例无谓拨号或闪现终端。
  const canAttach = !!status && !isStopped
  const { data: tokenData, isLoading, error } = useTerminalToken(instanceId, 'write', canAttach)

  return (
    <div className="flex h-full flex-col">
      {/* 工具栏：面包屑 + 禁用占位按钮（分段模式下由父组件承载，隐藏） */}
      {!hideHeader && (
        <div className="flex items-center justify-between border-b px-4 py-2">
          <div className="flex items-center gap-1.5 text-sm">
            <span className="text-muted-foreground">{t('console.title')}</span>
            <span className="text-muted-foreground">/</span>
            <span className="font-medium">{instance?.name ?? `#${instanceId}`}</span>
          </div>
          <div className="flex gap-2">
            <Button variant="outline" size="sm" disabled title={t('console.splitSoon')}>
              {t('console.split')}
            </Button>
            <Button variant="outline" size="sm" disabled title={t('console.directorSoon')}>
              {t('console.director')}
            </Button>
          </div>
        </div>
      )}

      {/* 非运行（STARTING/STOPPING/CRASHED）仍连只读终端，给出状态提示 */}
      {status && !isRunning && !isStopped && (
        <div className="mx-4 mt-2 rounded-lg border border-amber-300 bg-amber-50 p-2 text-xs text-amber-700 dark:border-amber-800 dark:bg-amber-950 dark:text-amber-300">
          {t('instanceDetail.terminalReadOnly', { status })}
        </div>
      )}

      {/* 终端区 */}
      <div className="min-h-0 flex-1 p-4">
        {!status ? (
          // 实例状态未知（加载中）：先不挂载终端，避免拿不到状态就拨号/闪现。
          <div className="flex min-h-[400px] items-center justify-center rounded-lg bg-[#1a1b26] p-4">
            <p className="text-sm text-gray-500">{t('instanceDetail.connecting')}</p>
          </div>
        ) : isStopped ? (
          // 完全停机：不挂载 xterm、不连 WS，展示「实例未运行」静态占位，避免死循环刷断连（FIX-B）。
          <div className="flex min-h-[400px] flex-col items-center justify-center gap-1.5 rounded-lg bg-[#1a1b26] p-4 text-center">
            <p className="text-sm font-medium text-gray-300">
              {t('instanceDetail.terminalNotRunning', { status })}
            </p>
            <p className="text-xs text-gray-500">{t('instanceDetail.terminalNotRunningHint')}</p>
          </div>
        ) : error ? (
          <div className="flex min-h-[400px] items-center justify-center rounded-lg bg-[#1a1b26] p-4">
            <p className="text-sm text-muted-foreground">
              {t('instanceDetail.terminalConnectFailed')}: {(error as Error).message || t('common.error')}
            </p>
          </div>
        ) : (
          <TerminalComponent
            key={instanceId}
            instanceId={String(instanceId)}
            wsUrl={tokenData?.wsUrl}
            token={tokenData?.token}
            readOnly={!isRunning}
            isLoading={isLoading}
          />
        )}
      </div>
    </div>
  )
}

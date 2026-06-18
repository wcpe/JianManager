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
   * 工作区分段（FR-039）中由 WorkspacePane 统一承载面包屑与「终端 | Bot」切换，
   * 此时本组件只渲染终端区，避免双重头部。
   */
  hideHeader?: boolean
}

export default function TerminalPane({ instanceId, hideHeader = false }: TerminalPaneProps) {
  const { t } = useTranslation()
  const { data: instance } = useInstance(instanceId)
  const status = instance?.status ?? ''
  const isRunning = status === 'RUNNING'
  const { data: tokenData, isLoading, error } = useTerminalToken(
    instanceId,
    isRunning ? 'write' : 'read',
  )

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

      {/* 终端区 */}
      <div className="min-h-0 flex-1 p-4">
        {status && !isRunning && (
          <div className="mb-2 rounded-lg border border-amber-300 bg-amber-50 p-2 text-xs text-amber-700 dark:border-amber-800 dark:bg-amber-950 dark:text-amber-300">
            {t('instanceDetail.terminalReadOnly', { status })}
          </div>
        )}
        {error ? (
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

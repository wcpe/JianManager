import { useCallback, useState, type KeyboardEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { useInstance } from '@/api/instances'
import { useTerminalToken } from '@/api/terminal'
import TerminalComponent from '@/components/Terminal'
import StoppedLogsView from './StoppedLogsView'
import { terminalSessionManager } from '@/lib/terminal-session-manager'
import { Button } from '@jianmanager/ui/components/button'
import { cn } from '@jianmanager/ui'
import { Eye, Maximize2, Minimize2, Pencil, RotateCcw, Search, ZoomIn, ZoomOut } from 'lucide-react'

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
  /**
   * 终端会话保活（FR-295，ADR-067）：控制台 keep-alive 宿主下传 true，
   * 卸载/隐藏不释放连接；独立表面（画布卡片等）保持默认卸载即释放。
   */
  persistSession?: boolean
}

export default function TerminalPane({ instanceId, hideHeader = false, persistSession = false }: TerminalPaneProps) {
  const { t } = useTranslation()
  const [fullscreen, setFullscreen] = useState(false)
  const [fontSize, setFontSize] = useState(14)
  const [terminalSearchOpen, setTerminalSearchOpen] = useState(false)
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
  const { isLoading, error, refetch } = useTerminalToken(instanceId, 'write', canAttach)
  // 一次性 token 首连即被 CP 消费失效（onetimetoken.Store），重连（自动重试 / 手动重连）必须
  // 现取一条新 token——复用已消费 token 会被 /ws/terminal 以 401「token already used」拒绝，
  // 导致重连永不恢复（FR-140）。故不把 token 作为静态 prop 下传，而暴露拉取回调让终端每次连接前现取。
  const fetchToken = useCallback(async () => {
    const result = await refetch()
    const data = result.data
    if (!data?.wsUrl || !data.token) {
      throw new Error(result.error instanceof Error ? result.error.message : '获取终端令牌失败')
    }
    return { wsUrl: data.wsUrl, token: data.token }
  }, [refetch])
  const adjustFont = (delta: number) => setFontSize((value) => Math.min(20, Math.max(11, value + delta)))
  const handlePaneKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'f') {
      event.preventDefault()
      setTerminalSearchOpen(true)
    } else if (event.key === 'Escape' && terminalSearchOpen) {
      event.preventDefault()
      setTerminalSearchOpen(false)
    }
  }

  return (
    <div className={cn('flex h-full flex-col bg-background', fullscreen && 'fixed inset-4 z-50 rounded-xl border shadow-2xl')} onKeyDown={handlePaneKeyDown}>
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

      {/* 顶栏（REF 方案 A）：三层 chrome 收敛为单条扁平工具栏——去浮卡/去阴影/去外边距，
          状态提示并入行首（不再独占整行），控件右对齐、扁平 ghost，把纵向空间还给日志区。 */}
      {canAttach && (
        <div className="flex flex-wrap items-center gap-1 border-b bg-card/40 px-3 py-1.5 text-xs">
          <span
            className={cn(
              'inline-flex items-center gap-1 rounded-full px-2 py-0.5 font-medium',
              isRunning ? 'bg-status-success/10 text-status-success' : 'bg-muted text-muted-foreground',
            )}
          >
            {isRunning ? <Pencil className="size-3" /> : <Eye className="size-3" />}
            {isRunning ? t('instanceDetail.terminalWritable') : t('instanceDetail.terminalReadOnlyBadge')}
          </span>
          {/* 非运行（STARTING/STOPPING/CRASHED）：状态提示并入工具栏行首，替代此前独占一行的琥珀横幅。 */}
          {status && !isRunning && !isStopped && (
            <span className="text-amber-600 dark:text-amber-400">{t('instanceDetail.terminalReadOnly', { status })}</span>
          )}
          {/* 手动重连改经连接管理器（FR-295）：不再 remount 组件，断旧连、现取新 token 重建（FR-140）。 */}
          <Button size="sm" variant="ghost" className="ml-auto h-7 rounded-md px-2 text-xs" onClick={() => terminalSessionManager.reconnect(instanceId)}>
            <RotateCcw className="mr-1 size-3.5" />
            {t('instanceDetail.terminalReconnect')}
          </Button>
          <Button
            size="icon"
            variant="ghost"
            className="size-7 rounded-md"
            onClick={() => setTerminalSearchOpen(true)}
            aria-label={t('instanceDetail.terminalSearchOpen', { defaultValue: '搜索终端' })}
            aria-pressed={terminalSearchOpen}
          >
            <Search className="size-3.5" />
          </Button>
          <Button size="icon" variant="ghost" className="size-7 rounded-md" onClick={() => adjustFont(-1)} aria-label={t('instanceDetail.terminalFontDown')}>
            <ZoomOut className="size-3.5" />
          </Button>
          <span className="min-w-9 text-center font-mono tabular-nums text-muted-foreground">{fontSize}px</span>
          <Button size="icon" variant="ghost" className="size-7 rounded-md" onClick={() => adjustFont(1)} aria-label={t('instanceDetail.terminalFontUp')}>
            <ZoomIn className="size-3.5" />
          </Button>
          <Button
            size="sm"
            variant="ghost"
            className="h-7 rounded-md px-2 text-xs"
            onClick={() => setFullscreen((v) => !v)}
            aria-pressed={fullscreen}
          >
            {fullscreen ? <Minimize2 className="mr-1 size-3.5" /> : <Maximize2 className="mr-1 size-3.5" />}
            {fullscreen ? t('instanceDetail.terminalExitFullscreen') : t('instanceDetail.terminalFullscreen')}
          </Button>
        </div>
      )}

      {/* 终端区 */}
      <div className="min-h-0 flex-1 p-2">
        {!status ? (
          // 实例状态未知（加载中）：先不挂载终端，避免拿不到状态就拨号/闪现。
          <div className="flex min-h-[400px] items-center justify-center rounded-lg bg-[#1a1b26] p-4">
            <p className="text-sm text-gray-500">{t('instanceDetail.connecting')}</p>
          </div>
        ) : isStopped ? (
          // 完全停机：不挂载 xterm、不连 WS（避免死循环刷断连 FIX-B），改从 DB 回放历史日志（FR-345）——
          // 令关服过程/崩溃现场在停机态仍可见，替代此前空白「实例未运行」占位。
          <StoppedLogsView instanceId={instanceId} status={status} />
        ) : error ? (
          <div className="flex min-h-[400px] items-center justify-center rounded-lg bg-[#1a1b26] p-4">
            <p className="text-sm text-muted-foreground">
              {t('instanceDetail.terminalConnectFailed')}: {(error as Error).message || t('common.error')}
            </p>
          </div>
        ) : (
          <TerminalComponent
            key={String(instanceId)}
            instanceId={String(instanceId)}
            fetchToken={fetchToken}
            readOnly={!isRunning}
            isLoading={isLoading}
            fontSize={fontSize}
            searchOpen={terminalSearchOpen}
            onSearchOpenChange={setTerminalSearchOpen}
            persistSession={persistSession}
          />
        )}
      </div>
    </div>
  )
}

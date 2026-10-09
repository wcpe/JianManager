import { useState, type KeyboardEvent, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Eye, Maximize2, Minimize2, Pencil, Play, RotateCcw, Search, ZoomIn, ZoomOut } from 'lucide-react'

import { Button } from '@jianmanager/ui/components/button'
import { cn } from '@jianmanager/ui'
import { ConsoleCommandBar } from '@/components/views/console/ConsoleCommandBar'
import { InstanceConsoleView } from '@/components/views/console/InstanceConsoleView'
import { ConsoleImmersiveMode } from '@/components/views/console/ConsoleImmersiveMode'
import { terminalSessionManager } from '@/lib/terminal-session-manager'
import type { FetchTerminalCreds } from '@/lib/terminal-session-manager'
import type { ConsoleHistoryState } from '@/lib/console-history'

/**
 * 工作区终端面板：为单个实例打开控制台（ADR-009 / FR-037）。
 * 复用一次性 token，逻辑与实例详情页「终端」Tab 一致：运行态可写，否则只读。
 *
 * FR-415 起承载的是输出/输入分离的新控制台（{@link InstanceConsoleView}，ADR-086），
 * 不再是 xterm 渲染壳。工具栏语义（重连 / 字号 / 搜索 / 全屏）保持不变。
 */
export interface TerminalPaneProps {
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
  /** 沉浸模式内的 pane：去掉自身工具栏与 F11 入口，避免嵌套打开第二层 portal。 */
  embeddedImmersive?: boolean
  /** pane 获得鼠标/键盘焦点时通知沉浸层更新焦点边框。 */
  onPaneFocus?: () => void
  /** 实例状态（应用侧 `useInstance`；缺省即「未知」，不连 WS）。 */
  status?: string
  /** 实例名（面包屑展示；缺省回退 `#id`）。 */
  instanceName?: string
  /**
   * 现取一次性终端凭据（应用侧 `useTerminalToken` + refetch）。
   * 每次连接前现取：一次性 token 首连即被消费失效，复用会 401（FR-140）。
   */
  fetchToken?: FetchTerminalCreds
  /** 启动实例（应用侧 `useStartInstance`）；停机/崩溃态的直达动作。 */
  onStartInstance?: () => void
  /** 启动请求进行中（按钮禁用）。 */
  isStarting?: boolean
  /** 终端凭据获取中（应用侧 `useTerminalToken` 的 loading）。 */
  isTokenLoading?: boolean
  /** 终端凭据获取失败的原因（应用侧 `useTerminalToken` 的 error；有值时展示失败态）。 */
  tokenError?: string
  /** 是否有 `terminal.access`（应用侧权限；无则命令栏禁用，FR-432）。 */
  canAccessTerminal?: boolean
  /** 历史回溯控制器（透传给 {@link InstanceConsoleView}；应用侧 `useConsoleHistory`）。 */
  historyBacktrack: ConsoleHistoryState
  /**
   * 渲染停机日志回放（应用侧接线层组件，自带 `useLogs` 取数与「完整历史」链接）。
   * 缺省时不渲染该段（停机态只剩命令栏禁用提示）。
   */
  renderStoppedLogs?: (args: { instanceId: number; status: string }) => ReactNode
}

export function TerminalPane({
  instanceId,
  hideHeader = false,
  persistSession = false,
  embeddedImmersive = false,
  onPaneFocus,
  status = '',
  instanceName,
  fetchToken,
  onStartInstance,
  isStarting = false,
  isTokenLoading = false,
  tokenError,
  canAccessTerminal = true,
  historyBacktrack,
  renderStoppedLogs,
}: TerminalPaneProps) {
  const { t } = useTranslation()
  const [fullscreen, setFullscreen] = useState(false)
  const [immersive, setImmersive] = useState(false)
  const [fontSize, setFontSize] = useState(14)
  const [terminalSearchOpen, setTerminalSearchOpen] = useState(false)
  // 实例状态由 props 注入（应用侧 useInstance）；缺省即「未知」，不连 WS。
  const isRunning = status === 'RUNNING'
  // 完全停机（STOPPED）的实例无进程可 attach：CP→Worker 拨号失败时终端会陷入
  // 「连上即断、重连重置计数」的死循环刷断连（FIX-B），故 STOPPED 一律展示静态占位、不连 WS。
  // STARTING/STOPPING/CRASHED 仍连终端（只读）以看启动/关服/崩溃输出，行为不变。
  const isStopped = status === 'STOPPED'
  // 仅在状态已知且非完全停机时请求 token / 挂载终端：STOPPED 与状态未知都不发起 WS
  // （enabled=false 连 token 都不取），避免对停机/加载中实例无谓拨号或闪现终端。
  // token 的取数（含每次连接前现取）在应用侧接线层，此处只消费注入的 fetchToken。
  const canAttach = !!status && !isStopped
  const adjustFont = (delta: number) => setFontSize((value) => Math.min(20, Math.max(11, value + delta)))

  // 命令栏禁用时的一行原因 + 直达动作（spec §2.2）。
  // 停机/崩溃给「启动实例」；STARTING/STOPPING 是过渡态，给按钮只会诱导用户重复触发。
  // FR-432：无 terminal.access 时命令栏禁用（平台管理员 hasPerm 恒 true）。
  const canStart = isStopped || status === 'CRASHED'
  const disabledReason = !canAccessTerminal
    ? t('permissions.terminalDenied')
    : status
      ? t('instanceDetail.consoleInputDisabled', { status })
      : undefined
  const startAction = canStart ? (
    <Button
      size="xs"
      variant="outline"
      data-testid="instance-operate-start-from-terminal"
      disabled={isStarting || !canAccessTerminal}
      onClick={() => {
        onStartInstance?.()
      }}
    >
      <Play className="mr-1 size-3" />
      {t('instanceDetail.consoleStartInstance')}
    </Button>
  ) : undefined

  const handlePaneKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (!embeddedImmersive && event.key === 'F11') {
      event.preventDefault()
      setImmersive((value) => !value)
    } else if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'f') {
      event.preventDefault()
      setTerminalSearchOpen(true)
    } else if (event.key === 'Escape' && terminalSearchOpen) {
      event.preventDefault()
      setTerminalSearchOpen(false)
    }
  }

  return (
    <div
      className={cn(
        'flex h-full min-w-0 w-full flex-col bg-background',
        fullscreen && 'fixed inset-4 z-50 rounded-xl border shadow-2xl',
        embeddedImmersive && 'bg-[#161a22]',
      )}
      onKeyDown={handlePaneKeyDown}
      onFocusCapture={onPaneFocus}
      onMouseDownCapture={onPaneFocus}
    >
      {/* 工具栏：面包屑 + 禁用占位按钮（分段模式下由父组件承载，隐藏） */}
      {!hideHeader && (
        <div className="flex items-center justify-between border-b px-4 py-2">
          <div className="flex items-center gap-1.5 text-sm">
            <span className="text-muted-foreground">{t('console.title')}</span>
            <span className="text-muted-foreground">/</span>
            <span className="font-medium">{instanceName ?? `#${instanceId}`}</span>
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
      {canAttach && !embeddedImmersive && (
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
          <Button
            size="sm"
            variant="ghost"
            className="h-7 rounded-md px-2 text-xs"
            onClick={() => setImmersive(true)}
          >
            <Maximize2 className="mr-1 size-3.5" />
            {t('instanceDetail.consoleImmersiveEnter')}
          </Button>
        </div>
      )}

      {/* 控制台区 */}
      <div className={cn('flex min-h-0 flex-1 flex-col', embeddedImmersive ? 'p-0' : 'p-2')}>
        {!status ? (
          // 实例状态未知（加载中）：先不挂载控制台，避免拿不到状态就拨号/闪现。
          <div className="flex min-h-[400px] items-center justify-center rounded-lg bg-[#1a1b26] p-4">
            <p className="text-sm text-gray-500">{t('instanceDetail.connecting')}</p>
          </div>
        ) : isStopped || !canAccessTerminal ? (
          // 完全停机：不连 WS（避免死循环刷断连 FIX-B），改从 DB 回放历史日志（FR-345）——
          // 令关服过程/崩溃现场在停机态仍可见。命令栏仍在位但禁用 + 带「启动实例」直达动作
          // （spec §2.2）：让「为什么输不进去、怎么才能输」在同一处说清，而不是干脆没有输入框。
          // FR-432：无 terminal.access 同样禁用输入，只读输出仍可看。
          <div className="flex min-h-0 flex-1 flex-col">
            <div className="min-h-0 flex-1 overflow-hidden">
              {renderStoppedLogs?.({ instanceId, status })}
            </div>
            <ConsoleCommandBar
              disabled
              disabledReason={disabledReason}
              action={startAction}
              onSubmit={() => {}}
              fontSize={fontSize}
              immersive={embeddedImmersive}
            />
          </div>
        ) : tokenError ? (
          <div className="flex min-h-[400px] items-center justify-center rounded-lg bg-[#1a1b26] p-4">
            <p className="text-sm text-muted-foreground">
              {t('instanceDetail.terminalConnectFailed')}: {tokenError || t('common.error')}
            </p>
          </div>
        ) : (
          <InstanceConsoleView
            key={String(instanceId)}
            instanceId={instanceId}
            fetchToken={fetchToken}
            readOnly={!isRunning || !canAccessTerminal}
            isLoading={isTokenLoading}
            fontSize={fontSize}
            searchOpen={terminalSearchOpen}
            onSearchOpenChange={setTerminalSearchOpen}
            persistSession={persistSession}
            disabledReason={disabledReason}
            disabledAction={startAction}
            immersive={embeddedImmersive}
            historyBacktrack={historyBacktrack}
          />
        )}
      </div>

      {immersive && !embeddedImmersive && (
        <ConsoleImmersiveMode
          initialInstanceId={instanceId}
          onExit={() => setImmersive(false)}
          renderPane={({ instanceId: paneInstanceId, onFocus }) => (
            <TerminalPane
              instanceId={paneInstanceId}
              hideHeader
              embeddedImmersive
              onPaneFocus={onFocus}
              // 沉浸 pane 与底层主视图共享同一会话：pane 卸载（退出沉浸台）绝不能触发
              // release-dispose，否则会连带释放实例页主视图正在用的会话导致控制台空白。
              // 会话生命周期由管理器 LRU（FR-296）兜底，这里只租不还。
              persistSession
              // 沉浸 pane 复用外层已注入的取数面（状态 / 凭据 / 权限 / 回溯），不再各自取数。
              status={status}
              instanceName={instanceName}
              fetchToken={fetchToken}
              onStartInstance={onStartInstance}
              isStarting={isStarting}
              isTokenLoading={isTokenLoading}
              tokenError={tokenError}
              canAccessTerminal={canAccessTerminal}
              historyBacktrack={historyBacktrack}
              renderStoppedLogs={renderStoppedLogs}
            />
          )}
        />
      )}
    </div>
  )
}

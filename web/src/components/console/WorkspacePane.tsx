import { useTranslation } from 'react-i18next'
import {
  useInstance,
  useStartInstance,
  useStopInstance,
  useRestartInstance,
  useKillInstance,
} from '@/api/instances'
import { useConsoleStore, type WorkspaceSegment } from '@/stores/console'
import TerminalPane from './TerminalPane'
import BotSegment from './BotSegment'
import MetricsSegment from './MetricsSegment'
import FileBrowser from '@/components/FileBrowser'
import ConfigEditor from '@/components/ConfigEditor'
import PluginManager from '@/components/plugins/PluginManager'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Button } from '@/components/ui/button'
import { StatusBadge } from '@/components/ui/status-badge'
import { instanceStatusLevel } from '@/lib/threshold'

/**
 * 工作区单实例面板（FR-039）：顶部「终端 | Bot」分段切换，按实例记忆当前分段（store）。
 * 终端段复用既有 {@link TerminalPane}；Bot 段为聚合优先的 {@link BotSegment}。
 */
interface WorkspacePaneProps {
  /** 当前工作区打开的实例 id */
  instanceId: number
}

export default function WorkspacePane({ instanceId }: WorkspacePaneProps) {
  const { t } = useTranslation()
  const { data: instance } = useInstance(instanceId)
  const segment = useConsoleStore((s) => s.workspaceSegmentByInstance[instanceId] ?? 'terminal')
  const setSegment = useConsoleStore((s) => s.setWorkspaceSegment)
  const closeInstance = useConsoleStore((s) => s.closeInstance)

  const status = instance?.status ?? ''
  const isRunning = status === 'RUNNING'
  const start = useStartInstance()
  const stop = useStopInstance()
  const restart = useRestartInstance()
  const kill = useKillInstance()

  return (
    <div className="flex h-full flex-col">
      <div className="flex items-center gap-3 border-b px-4 py-2">
        <div className="flex items-center gap-1.5 text-sm">
          <span className="text-muted-foreground">{t('console.title')}</span>
          <span className="text-muted-foreground">/</span>
          <span className="font-medium">{instance?.name ?? `#${instanceId}`}</span>
          {status && <StatusBadge level={instanceStatusLevel(status)} label={status} className="ml-1" />}
        </div>

        {/* 实例生命周期操作（此前控制台缺失启动/停止等按钮） */}
        <div className="flex items-center gap-1.5">
          {isRunning ? (
            <>
              <Button size="sm" variant="outline" disabled={stop.isPending} onClick={() => stop.mutate(instanceId)}>
                {t('instances.stop')}
              </Button>
              <Button size="sm" variant="outline" disabled={restart.isPending} onClick={() => restart.mutate(instanceId)}>
                {t('instances.restart')}
              </Button>
              <Button size="sm" variant="outline" disabled={kill.isPending} onClick={() => kill.mutate(instanceId)}>
                {t('instances.kill')}
              </Button>
            </>
          ) : (
            <Button size="sm" disabled={!instance || start.isPending} onClick={() => start.mutate(instanceId)}>
              {t('instances.start')}
            </Button>
          )}
        </div>

        <Tabs
          value={segment}
          onValueChange={(v: string) => setSegment(instanceId, v as WorkspaceSegment)}
          className="ml-auto"
        >
          <TabsList>
            <TabsTrigger value="terminal">{t('console.segmentTerminal')}</TabsTrigger>
            <TabsTrigger value="files">{t('instanceDetail.files')}</TabsTrigger>
            <TabsTrigger value="config">{t('instanceDetail.config')}</TabsTrigger>
            <TabsTrigger value="plugins">{t('plugins.tab')}</TabsTrigger>
            <TabsTrigger value="metrics">{t('metrics.tab')}</TabsTrigger>
            <TabsTrigger value="bot">{t('console.segmentBot')}</TabsTrigger>
          </TabsList>
        </Tabs>
        <Button size="sm" variant="ghost" title={t('common.close')} onClick={closeInstance}>
          ✕
        </Button>
      </div>

      <div className="min-h-0 flex-1 overflow-auto">
        {segment === 'bot' ? (
          <BotSegment instanceId={instanceId} />
        ) : segment === 'files' ? (
          <div className="p-4">
            <FileBrowser instanceId={instanceId} />
          </div>
        ) : segment === 'config' ? (
          <div className="p-4">
            <ConfigEditor instanceId={instanceId} />
          </div>
        ) : segment === 'plugins' ? (
          <div className="p-4">
            <PluginManager instanceId={instanceId} />
          </div>
        ) : segment === 'metrics' ? (
          <MetricsSegment instanceUuid={instance?.uuid ?? ''} />
        ) : (
          <TerminalPane instanceId={instanceId} hideHeader />
        )}
      </div>
    </div>
  )
}

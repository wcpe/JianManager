import { useTranslation } from 'react-i18next'
import { useInstance } from '@/api/instances'
import { useConsoleStore, type WorkspaceSegment } from '@/stores/console'
import TerminalPane from './TerminalPane'
import BotSegment from './BotSegment'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'

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

  return (
    <div className="flex h-full flex-col">
      <div className="flex items-center gap-3 border-b px-4 py-2">
        <div className="flex items-center gap-1.5 text-sm">
          <span className="text-muted-foreground">{t('console.title')}</span>
          <span className="text-muted-foreground">/</span>
          <span className="font-medium">{instance?.name ?? `#${instanceId}`}</span>
        </div>
        <Tabs
          value={segment}
          onValueChange={(v) => setSegment(instanceId, v as WorkspaceSegment)}
          className="ml-auto"
        >
          <TabsList>
            <TabsTrigger value="terminal">{t('console.segmentTerminal')}</TabsTrigger>
            <TabsTrigger value="bot">{t('console.segmentBot')}</TabsTrigger>
          </TabsList>
        </Tabs>
      </div>

      <div className="min-h-0 flex-1">
        {segment === 'bot' ? (
          <BotSegment instanceId={instanceId} />
        ) : (
          <TerminalPane instanceId={instanceId} hideHeader />
        )}
      </div>
    </div>
  )
}

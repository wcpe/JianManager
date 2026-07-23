import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { Download } from 'lucide-react'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@jianmanager/ui/components/tabs'
import ConfigExplorer from '@/components/config-explorer/ConfigExplorer'
import UnifiedExplorerShell from '@/components/file-browser/UnifiedExplorerShell'
import {
  instanceBrowseCapability,
  instanceFilesCapability,
} from '@/components/file-browser/capability'
import { instanceFileSource } from '@/components/file-browser/sources/instanceSource'
import type { FileBrowserAction } from '@/components/file-browser/types'

/**
 * 实例「资源卡片」（FR-130 文件+配置合一；FR-213 共享浏览器；FR-378 统一壳）。
 *
 * - 「管理」= ConfigExplorer（全功能配置，能力不减）
 * - 「文件」= UnifiedExplorerShell + instance-files Capability → ExplorerTabHost
 * - 「浏览」= UnifiedExplorerShell + instance-browse → FileBrowser + 下载
 */
interface InstanceResourceCardProps {
  instanceId: number
}

export default function InstanceResourceCard({ instanceId }: InstanceResourceCardProps) {
  const { t } = useTranslation()
  const source = useMemo(() => instanceFileSource(instanceId), [instanceId])

  const downloadAction = useMemo<FileBrowserAction>(
    () => ({
      key: 'download',
      label: t('fileBrowser.download'),
      icon: <Download className="size-4" />,
      visible: (e) => !e.isDir,
      onAction: (e) => {
        void source.download?.(e)
      },
    }),
    [t, source],
  )

  const filesCap = useMemo(() => instanceFilesCapability(), [])
  const browseCap = useMemo(() => instanceBrowseCapability(downloadAction), [downloadAction])

  return (
    <Tabs defaultValue="manage" className="flex h-full min-h-0 flex-col">
      <TabsList className="self-start rounded-full">
        <TabsTrigger value="manage" className="rounded-full text-xs">
          {t('resourceCard.manage')}
        </TabsTrigger>
        <TabsTrigger value="files" className="rounded-full text-xs">
          {t('instanceDetail.files')}
        </TabsTrigger>
        <TabsTrigger value="browse" className="rounded-full text-xs">
          {t('resourceCard.browse')}
        </TabsTrigger>
      </TabsList>
      <TabsContent value="manage" className="mt-3 min-h-0 flex-1">
        <ConfigExplorer instanceId={instanceId} />
      </TabsContent>
      <TabsContent value="files" className="mt-3 min-h-0 flex-1">
        <UnifiedExplorerShell capability={filesCap} instanceId={instanceId} />
      </TabsContent>
      <TabsContent value="browse" className="mt-3 min-h-0 flex-1">
        <UnifiedExplorerShell capability={browseCap} source={source} />
      </TabsContent>
    </Tabs>
  )
}

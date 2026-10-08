// 视图已迁至 @jianmanager/ui（ADR-097）；本层只注入三个视图（受控化）。
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { Download } from 'lucide-react'
import { InstanceResourceCard as InstanceResourceCardView } from '@jianmanager/ui/components/views/console/InstanceResourceCard'
import ConfigExplorer from '@/components/config-explorer/ConfigExplorer'
import UnifiedExplorerShell from '@/components/file-browser/UnifiedExplorerShell'
import { instanceBrowseCapability, instanceFilesCapability } from '@/components/file-browser/capability'
import { instanceFileSource } from '@/components/file-browser/sources/instanceSource'
import type { FileBrowserAction } from '@jianmanager/ui/lib/file-browser-types'

/**
 * 实例「资源卡片」的取数接线层（FR-130 / FR-213 / FR-378 / FR-422）。
 *
 * 分段状态与寄居布局已入包；三个视图（ConfigExplorer 与两种 UnifiedExplorerShell）以及
 * 文件数据源、能力画像、下载动作留应用侧，经三个插槽注入。
 */
export default function InstanceResourceCard({ instanceId }: { instanceId: number }) {
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
    <InstanceResourceCardView
      renderManage={(leading) => <ConfigExplorer instanceId={instanceId} toolbarLeading={leading} />}
      renderFiles={(leading) => (
        <UnifiedExplorerShell capability={filesCap} instanceId={instanceId} leading={leading} />
      )}
      renderBrowse={(header) => (
        <UnifiedExplorerShell capability={browseCap} source={source} header={header} />
      )}
    />
  )
}

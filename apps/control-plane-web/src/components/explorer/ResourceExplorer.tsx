import { useCallback } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'

import ResourceExplorer from '@/components/views/explorer/ResourceExplorer'
import { fetchFileList, readFileContent, writeFileContent, deleteFile, renameFile, uploadFile, downloadFile, downloadArchive, checkFileAccess, chmodFile, searchFiles } from '@/api/files'
import { listArchiveEntries, readArchiveEntry, decompile } from '@/api/archive'
import { reportInstanceDraft } from '@/lib/console/console-draft-registry'
import { useThemeStore } from '@/stores/theme'
import VersionDrawer from './VersionDrawer'

// 配置增强能力的类型在这里转出：它的消费方（ConfigExplorer）按原路径 import。
export type { ConfigCapabilities } from '@/components/views/explorer/ResourceExplorer'

/** 接线层对外暴露的 props：包内视图的 props 去掉全部注入项。 */
type ConnectedProps = Omit<
  Parameters<typeof ResourceExplorer>[0],
  | 'fileApi'
  | 'toast'
  | 'reportDraft'
  | 'invalidateVersions'
  | 'theme'
  | 'searchFiles'
  | 'listArchiveEntries'
  | 'readArchiveEntry'
  | 'decompile'
  | 'renderVersionDrawer'
>

/**
 * 共享资源管理器接线层（ADR-097）：文件操作、提示通道、草稿登记、版本缓存失效、
 * 主题与三个归档/搜索取数都在这里注入；版本抽屉经渲染函数交回，它内部自带取数。
 * 消费方（ConfigExplorer / ExplorerTabHost / InstanceFilesPage 等）零改动。
 */
export default function ResourceExplorerConnected(props: ConnectedProps) {
  const qc = useQueryClient()
  const resolvedTheme = useThemeStore((s) => s.resolvedTheme)

  const invalidateVersions = useCallback(
    (path: string) => {
      qc.invalidateQueries({ queryKey: ['fileVersions', props.instanceId, path] })
    },
    [qc, props.instanceId],
  )

  const renderVersionDrawer = useCallback(
    (args: {
      instanceId: number
      filePath: string | null
      open: boolean
      onOpenChange: (open: boolean) => void
      onRolledBack: () => void
    }) => <VersionDrawer {...args} />,
    [],
  )

  return (
    <ResourceExplorer
      {...props}
      fileApi={{
        fetchFileList,
        readFileContent,
        writeFileContent,
        deleteFile,
        renameFile,
        uploadFile,
        downloadFile,
        downloadArchive,
        checkFileAccess,
        chmodFile,
      }}
      toast={toast}
      reportDraft={reportInstanceDraft}
      invalidateVersions={invalidateVersions}
      theme={resolvedTheme === 'dark' ? 'dark' : 'light'}
      searchFiles={searchFiles}
      listArchiveEntries={listArchiveEntries}
      readArchiveEntry={readArchiveEntry}
      decompile={decompile}
      renderVersionDrawer={renderVersionDrawer}
    />
  )
}

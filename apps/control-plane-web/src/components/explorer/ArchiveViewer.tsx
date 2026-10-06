import ArchiveViewer from '@jianmanager/ui/components/views/explorer/ArchiveViewer'
import { listArchiveEntries, readArchiveEntry, decompile } from '@/api/archive'
import { useThemeStore } from '@/stores/theme'

/**
 * 归档查看器接线层（ADR-097）：注入三处归档取数与解析后的主题，消费方零改动。
 */
export default function ArchiveViewerConnected(
  props: Omit<Parameters<typeof ArchiveViewer>[0], 'listEntries' | 'readEntry' | 'decompile' | 'theme'>,
) {
  const resolvedTheme = useThemeStore((s) => s.resolvedTheme)
  return (
    <ArchiveViewer
      {...props}
      listEntries={listArchiveEntries}
      readEntry={readArchiveEntry}
      decompile={decompile}
      theme={resolvedTheme === 'dark' ? 'dark' : 'light'}
    />
  )
}

import FileTree from '@/components/views/explorer/FileTree'
import { fetchFileList } from '@/api/files'

/**
 * 懒加载目录树的接线层（ADR-097）：把应用侧取数注入包内视图，消费方（ResourceExplorer）零改动。
 */
export default function FileTreeConnected(props: Omit<Parameters<typeof FileTree>[0], 'fetchEntries'>) {
  return <FileTree {...props} fetchEntries={fetchFileList} />
}

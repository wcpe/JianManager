import SearchPanel from '@jianmanager/ui/components/views/explorer/SearchPanel'
import { searchFiles } from '@/api/files'

/**
 * 文件搜索面板接线层（ADR-097）：注入搜索取数，消费方（ResourceExplorer）零改动。
 */
export default function SearchPanelConnected(props: Omit<Parameters<typeof SearchPanel>[0], 'search'>) {
  return <SearchPanel {...props} search={searchFiles} />
}

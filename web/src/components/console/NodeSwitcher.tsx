import { useTranslation } from 'react-i18next'
import { useNodes } from '@/api/nodes'
import { useConsoleStore } from '@/stores/console'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

/** 哨兵值：表示「全部节点」（shadcn Select 仅接受字符串 value）。 */
const ALL = 'all'

/** 实例树上方的节点切换下拉：「全部节点」+ 各节点。 */
export default function NodeSwitcher() {
  const { t } = useTranslation()
  const { data: nodes } = useNodes()
  const selectedNodeId = useConsoleStore((s) => s.selectedNodeId)
  const setSelectedNodeId = useConsoleStore((s) => s.setSelectedNodeId)

  return (
    <Select
      value={selectedNodeId === null ? ALL : String(selectedNodeId)}
      onValueChange={(v: string) => setSelectedNodeId(v === ALL ? null : Number(v))}
    >
      <SelectTrigger size="sm" className="w-full">
        <SelectValue placeholder={t('console.allNodes')} />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value={ALL}>{t('console.allNodes')}</SelectItem>
        {nodes?.map((node) => (
          <SelectItem key={node.id} value={String(node.id)}>
            {node.name}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

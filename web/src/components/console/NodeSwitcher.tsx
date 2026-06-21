import { useTranslation } from 'react-i18next'
import { Server } from 'lucide-react'
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

/**
 * 实例树上方的节点切换下拉（FR-069 瘦身）：「全部节点」+ 各节点。
 * 紧凑控件——矮行高 + 小字号 + 前置节点图标，避免占用过多侧栏垂直空间；
 * 选项变化仅切换 console store 的 selectedNodeId，实例树据此复用 `GET /instances?nodeId=`。
 */
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
      <SelectTrigger
        size="sm"
        className="h-7 w-full gap-1.5 px-2 text-xs [&_svg]:size-3.5"
        aria-label={t('console.nodeSwitcher')}
      >
        <Server className="shrink-0 text-muted-foreground" />
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

import { useCallback, useEffect, useState } from 'react'
import { ChevronRight, ChevronDown, Folder, FolderOpen } from 'lucide-react'
import { fetchFileList } from '@/api/files'
import { joinPath } from './paths'
import { cn } from '@/lib/utils'

/** 树节点（懒加载：children 为 undefined 表示未展开/未加载）。 */
interface TreeNode {
  name: string
  path: string
  children?: TreeNode[]
  loading?: boolean
}

interface FileTreeProps {
  instanceId: number
  /** 当前选中目录（高亮）。 */
  currentDir: string
  /** 点击目录回调。 */
  onSelectDir: (dir: string) => void
  /** 把某路径拖放到某目录回调（树内移动）。 */
  onDropMove: (targetDir: string) => void
  /** 外部刷新信号变化时，重置树（增删改后）。 */
  refreshKey: number
}

/** 懒加载目录树（FR-070 资源管理器左栏）。点目录展开拉取子目录；支持作为拖拽移动的放置目标。 */
export default function FileTree({
  instanceId,
  currentDir,
  onSelectDir,
  onDropMove,
  refreshKey,
}: FileTreeProps) {
  const [root, setRoot] = useState<TreeNode>({ name: '/', path: '', children: undefined })
  const [expanded, setExpanded] = useState<Set<string>>(new Set(['']))
  const [dragOver, setDragOver] = useState<string | null>(null)

  const loadChildren = useCallback(
    async (path: string): Promise<TreeNode[]> => {
      const entries = await fetchFileList(instanceId, path)
      return entries
        .filter((e) => e.isDir)
        .map((e) => ({ name: e.name, path: joinPath(path, e.name), children: undefined }))
    },
    [instanceId],
  )

  // 挂载或刷新：重新加载根目录子目录，保留展开集合（失效的自然不渲染）。
  useEffect(() => {
    let alive = true
    loadChildren('')
      .then((children) => {
        if (alive) setRoot({ name: '/', path: '', children })
      })
      .catch(() => {
        if (alive) setRoot({ name: '/', path: '', children: [] })
      })
    return () => {
      alive = false
    }
  }, [loadChildren, refreshKey])

  const toggle = useCallback(
    async (node: TreeNode) => {
      const isOpen = expanded.has(node.path)
      const next = new Set(expanded)
      if (isOpen) {
        next.delete(node.path)
        setExpanded(next)
        return
      }
      next.add(node.path)
      setExpanded(next)
      // 懒加载子目录（仅首次）。
      if (node.children === undefined) {
        const children = await loadChildren(node.path)
        setRoot((prev) => updateNode(prev, node.path, (n) => ({ ...n, children })))
      }
    },
    [expanded, loadChildren],
  )

  const renderNode = (node: TreeNode, depth: number): React.ReactNode => {
    const isOpen = expanded.has(node.path)
    const isCurrent = node.path === currentDir
    const isDropTarget = dragOver === node.path
    return (
      <div key={node.path || '__root__'}>
        <div
          className={cn(
            'flex items-center gap-1 px-1 py-0.5 text-sm cursor-pointer rounded hover:bg-accent/50',
            isCurrent && 'bg-accent font-medium',
            isDropTarget && 'ring-1 ring-primary bg-primary/10',
          )}
          style={{ paddingLeft: `${depth * 12 + 4}px` }}
          onClick={() => {
            onSelectDir(node.path)
            void toggle(node)
          }}
          onDragOver={(e) => {
            e.preventDefault()
            setDragOver(node.path)
          }}
          onDragLeave={() => setDragOver((d) => (d === node.path ? null : d))}
          onDrop={(e) => {
            e.preventDefault()
            setDragOver(null)
            onDropMove(node.path)
          }}
        >
          <span
            className="shrink-0"
            onClick={(e) => {
              e.stopPropagation()
              void toggle(node)
            }}
          >
            {isOpen ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
          </span>
          {isOpen ? <FolderOpen className="size-4 text-amber-500" /> : <Folder className="size-4 text-amber-500" />}
          <span className="truncate">{node.name === '/' ? '/' : node.name}</span>
        </div>
        {isOpen &&
          node.children?.map((child) => renderNode(child, depth + 1))}
      </div>
    )
  }

  return <div className="text-left">{renderNode(root, 0)}</div>
}

/** 在树中按 path 定位节点并以 updater 替换（不可变更新）。 */
function updateNode(node: TreeNode, path: string, updater: (n: TreeNode) => TreeNode): TreeNode {
  if (node.path === path) return updater(node)
  if (!node.children) return node
  return { ...node, children: node.children.map((c) => updateNode(c, path, updater)) }
}

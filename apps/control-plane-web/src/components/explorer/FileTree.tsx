import { useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronRight, ChevronDown, Folder, FolderOpen } from 'lucide-react'
import { fetchFileList } from '@/api/files'
import { joinPath } from './paths'
import { cn } from '@jianmanager/ui'
import { useVirtualRows } from '@/lib/virtual-list'

/** 树节点（懒加载：children 为 undefined 表示未展开/未加载）。 */
interface TreeNode {
  name: string
  path: string
  children?: TreeNode[]
  loading?: boolean
}

interface VisibleTreeNode extends TreeNode {
  depth: number
  parentPath: string | null
}

interface FileTreeProps {
  instanceId: number
  /** 当前选中目录（高亮）。 */
  currentDir: string
  /** 点击目录回调。 */
  onSelectDir: (dir: string) => void
  /** 把某路径拖放到某目录回调（树内移动）。 */
  /** FR-377：第二参 dataTransfer 供跨窗解析条目。 */
  onDropMove: (targetDir: string, dt?: DataTransfer) => void
  /** 外部刷新信号变化时，重置树（增删改后）。 */
  refreshKey: number
}

const FILE_TREE_ROW_HEIGHT = 28

/** 懒加载目录树（FR-070 资源管理器左栏）。点目录展开拉取子目录；支持作为拖拽移动的放置目标。 */
export default function FileTree({
  instanceId,
  currentDir,
  onSelectDir,
  onDropMove,
  refreshKey,
}: FileTreeProps) {
  const { t } = useTranslation()
  const [root, setRoot] = useState<TreeNode>({ name: '/', path: '', children: undefined })
  const [expanded, setExpanded] = useState<Set<string>>(new Set(['']))
  const [dragOver, setDragOver] = useState<string | null>(null)
  const [focusedPath, setFocusedPath] = useState('')
  const itemRefs = useRef(new Map<string, HTMLDivElement>())

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

  const visibleNodes = useMemo(() => flattenVisibleNodes(root, expanded), [root, expanded])
  const focusablePath = visibleNodes.some((n) => n.path === focusedPath) ? focusedPath : ''
  const { containerRef, onScroll, range } = useVirtualRows({
    total: visibleNodes.length,
    itemSize: FILE_TREE_ROW_HEIGHT,
    overscan: 8,
    fallbackViewportSize: 420,
  })
  const virtualNodes = visibleNodes.slice(range.start, range.end)

  const focusPath = useCallback((path: string) => {
    setFocusedPath(path)
    const index = visibleNodes.findIndex((n) => n.path === path)
    if (index >= 0 && containerRef.current) {
      containerRef.current.scrollTop = Math.max(0, (index - 2) * FILE_TREE_ROW_HEIGHT)
      onScroll()
    }
    queueMicrotask(() => itemRefs.current.get(path)?.focus())
  }, [containerRef, onScroll, visibleNodes])

  const moveFocus = useCallback(
    (from: string, delta: number) => {
      const index = visibleNodes.findIndex((n) => n.path === from)
      if (index < 0) return
      const next = visibleNodes[Math.min(visibleNodes.length - 1, Math.max(0, index + delta))]
      if (next) focusPath(next.path)
    },
    [focusPath, visibleNodes],
  )

  const handleKeyDown = useCallback(
    (event: KeyboardEvent<HTMLDivElement>, node: VisibleTreeNode) => {
      if (event.key === 'ArrowDown') {
        event.preventDefault()
        moveFocus(node.path, 1)
      } else if (event.key === 'ArrowUp') {
        event.preventDefault()
        moveFocus(node.path, -1)
      } else if (event.key === 'ArrowRight') {
        event.preventDefault()
        if (!expanded.has(node.path)) {
          void toggle(node)
        } else if (node.children?.[0]) {
          focusPath(node.children[0].path)
        }
      } else if (event.key === 'ArrowLeft') {
        event.preventDefault()
        if (expanded.has(node.path) && node.path !== '') {
          setExpanded((prev) => {
            const next = new Set(prev)
            next.delete(node.path)
            return next
          })
        } else if (node.parentPath !== null) {
          focusPath(node.parentPath)
        }
      } else if (event.key === 'Enter' || event.key === ' ') {
        event.preventDefault()
        onSelectDir(node.path)
        void toggle(node)
      }
    },
    [expanded, focusPath, moveFocus, onSelectDir, toggle],
  )

  const renderNode = (node: VisibleTreeNode): React.ReactNode => {
    const isOpen = expanded.has(node.path)
    const isCurrent = node.path === currentDir
    const isDropTarget = dragOver === node.path
    return (
      <div key={node.path || '__root__'}>
        <div
          ref={(el) => {
            if (el) itemRefs.current.set(node.path, el)
            else itemRefs.current.delete(node.path)
          }}
          role="treeitem"
          aria-expanded={isOpen}
          aria-selected={isCurrent}
          aria-level={node.depth + 1}
          tabIndex={focusablePath === node.path ? 0 : -1}
          className={cn(
            'flex items-center gap-1 px-1 py-0.5 text-sm cursor-pointer rounded outline-none hover:bg-accent/50 focus-visible:ring-2 focus-visible:ring-ring/40',
            isCurrent && 'bg-accent font-medium',
            isDropTarget && 'ring-1 ring-primary bg-primary/10',
          )}
          style={{ minHeight: FILE_TREE_ROW_HEIGHT, paddingLeft: `${node.depth * 12 + 4}px` }}
          onFocus={() => setFocusedPath(node.path)}
          onKeyDown={(event) => handleKeyDown(event, node)}
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
            onDropMove(node.path, e.dataTransfer)
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
      </div>
    )
  }

  return (
    <div
      ref={containerRef}
      onScroll={onScroll}
      role="tree"
      aria-label={t('files.treeLabel')}
      className="h-full overflow-auto text-left"
    >
      {range.before > 0 && <div aria-hidden="true" style={{ height: range.before }} />}
      {virtualNodes.map(renderNode)}
      {range.after > 0 && <div aria-hidden="true" style={{ height: range.after }} />}
    </div>
  )
}

/** 在树中按 path 定位节点并以 updater 替换（不可变更新）。 */
function updateNode(node: TreeNode, path: string, updater: (n: TreeNode) => TreeNode): TreeNode {
  if (node.path === path) return updater(node)
  if (!node.children) return node
  return { ...node, children: node.children.map((c) => updateNode(c, path, updater)) }
}

function flattenVisibleNodes(root: TreeNode, expanded: Set<string>): VisibleTreeNode[] {
  const out: VisibleTreeNode[] = []
  const walk = (node: TreeNode, depth: number, parentPath: string | null) => {
    out.push({ ...node, depth, parentPath })
    if (!expanded.has(node.path)) return
    for (const child of node.children ?? []) walk(child, depth + 1, node.path)
  }
  walk(root, 0, null)
  return out
}

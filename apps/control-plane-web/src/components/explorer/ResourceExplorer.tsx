import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useQueryClient } from '@tanstack/react-query'
import { Download, FileQuestion, History, Loader2, Save, X } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import DangerConfirm from '@/components/DangerConfirm'
import {
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
  type FileInfo,
} from '@/api/files'
import { reportInstanceDraft } from '@/lib/console-draft-registry'
import CodeEditor from './editor/CodeEditor'
import EditorShortcutsHelp from './editor/EditorShortcutsHelp'
import ArchiveViewer from './ArchiveViewer'
import DecompileViewer from './DecompileViewer'
import FileTree from './FileTree'
import FileList from './FileList'
import Toolbar from './Toolbar'
import SearchPanel from './SearchPanel'
import PromptDialog from './PromptDialog'
import VersionDrawer from './VersionDrawer'
import {
  emptySelection,
  clickSelect,
  selectAll as selectAllKeys,
  pruneSelection,
  type SelectionState,
  type ClickModifiers,
} from './selection'
import {
  cutEntries,
  copyEntries,
  planPaste,
  type Clipboard,
  type ClipboardEntry,
  type PasteOp,
} from './clipboard'
import {
  setBusClipboard,
  getBusClipboard,
  clearBusClipboard,
  toClipboard,
  subscribeBusClipboard,
  writeDragToDataTransfer,
  readDragFromDataTransfer,
  setDragPayload,
} from './explorer-clipboard-bus'
import { joinPath, baseName, isValidName } from './paths'
import { needsDiscardConfirm } from './discard-guard'
import {
  emptyNavHistory,
  navPush,
  navBack,
  navForward,
  canNavBack,
  canNavForward,
} from './nav-history'
import {
  loadSortState,
  saveSortState,
  loadViewMode,
  saveViewMode,
  sortFiles,
  type FileSortState,
  type FileViewMode,
} from './file-sort'


/**
 * 配置增强能力（FR-071）。注入后资源管理器在「配置」语义下复用：
 * 打开文件时改用配置编辑器（schema 双模式 + 跨文件校验 + 配置版本），
 * 左栏顶部插入收藏/已发现配置，历史按钮打开配置版本抽屉。不注入时为纯文件资源管理器（FR-070）。
 */
export interface ConfigCapabilities {
  /**
   * 渲染打开文件的编辑器（取代默认 CodeEditor 面板）。
   * 自行读取/保存内容（走配置端点，生成配置版本），保存后调用 onAfterSave 让资源管理器刷新树与版本缓存。
   */
  renderEditor: (args: {
    instanceId: number
    path: string
    name: string
    onClose: () => void
    onAfterSave: () => void
    onOpenVersions: () => void
    /** 编辑器内部 dirty 变化时上报，供资源管理器切换/关闭守卫判断（BUG-018 #36）。 */
    onDirtyChange: (dirty: boolean) => void
    /** 搜索命中跳转目标行（1 起，FR-074）；未定位时 undefined。 */
    gotoLine?: number
    /** 定位 nonce：变化即重触发定位（同一行再次点击也能重跳，FR-074）。 */
    gotoNonce?: number
  }) => ReactNode
  /** 左栏目录树上方的额外内容（收藏栏 + 已发现配置面板）。 */
  sidebarExtra?: ReactNode
  /**
   * 渲染配置版本抽屉（取代文件版本抽屉）。
   * filePath 为当前查看版本的文件；onRolledBack 在回滚后触发（供编辑器重载）。
   */
  renderVersionDrawer: (args: {
    instanceId: number
    filePath: string | null
    open: boolean
    onOpenChange: (open: boolean) => void
    onRolledBack: () => void
  }) => ReactNode
}

/**
 * 共享资源管理器（FR-070）。
 *
 * 双栏：左懒加载目录树 + 右目录内容/编辑器。统管选中/多选/剪贴板/编辑态/历史抽屉。
 * 是 FR-071/073/074/075/082/083/084 复用的入口——对外仅依赖 `instanceId`，
 * 所有文件操作经 `@/api/files`（既有后端端点 + 批量 zip）。
 *
 * 传入 `config`（FR-071）时叠加配置语义：打开文件改用配置编辑器、左栏插收藏/发现、历史走配置版本抽屉；
 * 不传时行为与 FR-070 完全一致。
 */
export interface ExplorerContextInfo {
  dir: string
  file?: string
  dirty: boolean
}

interface ResourceExplorerProps {
  /** 实例 ID。 */
  instanceId: number
  /** 配置增强（FR-071）。省略即为纯文件资源管理器。 */
  config?: ConfigCapabilities
  /** 允许外部打开指定相对路径文件（收藏/发现面板点选）。 */
  openPathRef?: (open: (path: string) => void) => void
  /** FR-376：挂载时初始目录（相对工作目录，空=根）。 */
  initialDir?: string
  /** FR-376：挂载后打开的文件相对路径。 */
  initialFile?: string
  /** FR-376：目录/打开文件/脏态变化回传（多标签标题与草稿）。 */
  onContextChange?: (ctx: ExplorerContextInfo) => void
  /**
   * FR-376：草稿登记 key 后缀（默认 resource-file）；多标签时用 resource-tab:${id}，
   * 避免同实例多 Explorer 互相覆盖草稿登记。
   */
  draftKey?: string
}

/** 打开的编辑文件状态。 */
interface OpenFile {
  /** 相对工作目录的完整路径。 */
  path: string
  /** 文件名（决定语言高亮）。 */
  name: string
  /** 已保存的内容（用于脏标记比较）。 */
  saved: string
  /** 当前编辑内容。 */
  draft: string
  /**
   * 搜索命中跳转的目标行（1 起，FR-074）。每次跳转用单调递增的 nonce 拼进 key
   * 以重触发定位（即便同一行被再次点击）。0 表示不定位。
   */
  gotoLine?: number
  /** 定位 nonce：用于强制编辑器重定位（搭配 gotoLine）。 */
  gotoNonce?: number
}

type BlockedPreviewReason = 'binary' | 'too-large'

interface BlockedPreview {
  path: string
  name: string
  size: number
  reason: BlockedPreviewReason
}

interface BatchFailure {
  path: string
  message: string
}

interface BatchOperationState {
  label: string
  total: number
  completed: number
  skipped: number
  failed: BatchFailure[]
  done: boolean
}

const TEXT_EDIT_LIMIT_BYTES = 1024 * 1024

const BINARY_PREVIEW_EXTENSIONS = new Set([
  '.bin',
  '.class',
  '.dat',
  '.db',
  '.dll',
  '.dylib',
  '.exe',
  '.gif',
  '.gz',
  '.ico',
  '.jpeg',
  '.jpg',
  '.mp3',
  '.mp4',
  '.ogg',
  '.pdf',
  '.png',
  '.rar',
  '.so',
  '.sqlite',
  '.webp',
  '.xz',
  '.7z',
])

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}

function fileExt(name: string): string {
  const dot = name.lastIndexOf('.')
  return dot >= 0 ? name.slice(dot).toLowerCase() : ''
}

function blockedPreviewFor(file: FileInfo, path: string): BlockedPreview | null {
  if (file.isDir) return null
  if (BINARY_PREVIEW_EXTENSIONS.has(fileExt(file.name))) {
    return { path, name: file.name, size: file.size, reason: 'binary' }
  }
  if (file.size > TEXT_EDIT_LIMIT_BYTES) {
    return { path, name: file.name, size: file.size, reason: 'too-large' }
  }
  return null
}

function errorMessage(err: unknown): string {
  const data = (err as { response?: { data?: { message?: string; error?: string } } })?.response?.data
  return data?.message || data?.error || (err instanceof Error ? err.message : '')
}

function BatchOperationNotice({
  state,
  onDismiss,
}: {
  state: BatchOperationState | null
  onDismiss: () => void
}) {
  const { t } = useTranslation()
  if (!state) return null
  const failed = state.failed.length
  return (
    <div
      role="status"
      className="border-b bg-muted/20 px-3 py-2 text-xs text-muted-foreground"
    >
      <div className="flex items-start justify-between gap-3">
        <div className="space-y-1">
          <div className="font-medium text-foreground">
            {t('files.batchProgress', { label: state.label, completed: state.completed, total: state.total })}
            {failed > 0 && <span className="ml-2 text-destructive">{t('files.batchFailed', { count: failed })}</span>}
            {state.skipped > 0 && <span className="ml-2">{t('files.batchSkipped', { count: state.skipped })}</span>}
          </div>
          {failed > 0 && (
            <ul className="space-y-0.5">
              {state.failed.map((item) => (
                <li key={item.path} className="font-mono text-[11px] text-destructive">
                  {item.path}: {item.message || t('common.error')}
                </li>
              ))}
            </ul>
          )}
        </div>
        {state.done && (
          <Button type="button" variant="link" size="xs" className="h-auto shrink-0 p-0" onClick={onDismiss}>
            {t('common.close')}
          </Button>
        )}
      </div>
    </div>
  )
}

export default function ResourceExplorer({
  instanceId,
  config,
  openPathRef,
  initialDir = '',
  initialFile,
  onContextChange,
  draftKey = 'resource-file',
}: ResourceExplorerProps) {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const configMode = config != null

  // 当前目录（相对工作目录，空串=根）+ FR-375 导航/排序/视图。
  const [nav, setNav] = useState(() => emptyNavHistory(initialDir))
  const currentDir = nav.current
  const [files, setFiles] = useState<FileInfo[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [fileSort, setFileSort] = useState<FileSortState>(() => loadSortState())
  const [viewMode, setViewMode] = useState<FileViewMode>(() => loadViewMode())
  const explorerRootRef = useRef<HTMLDivElement>(null)


  // 树刷新信号（增删改后递增以重置树缓存）。
  const [treeRefresh, setTreeRefresh] = useState(0)

  // 选中态 + 剪贴板（FR-377：真源为实例级总线，本地 state 仅为订阅镜像）。
  const [selection, setSelection] = useState<SelectionState>(emptySelection)
  const explorerSourceId = useRef(
    // eslint-disable-next-line react-hooks/purity -- 实例级总线 sourceId 仅挂载一次生成
    `re-${instanceId}-${Math.random().toString(36).slice(2, 9)}`,
  ).current
  const [clipboard, setClipboardLocal] = useState<Clipboard | null>(() =>
    toClipboard(getBusClipboard(instanceId)),
  )
  useEffect(() => {
    /* eslint-disable react-hooks/set-state-in-effect -- 订阅实例级剪贴板总线镜像，属外部系统同步 */
    setClipboardLocal(toClipboard(getBusClipboard(instanceId)))
    return subscribeBusClipboard(instanceId, (bus) => {
      setClipboardLocal(toClipboard(bus))
    })
    /* eslint-enable react-hooks/set-state-in-effect */
  }, [instanceId])
  const setClipboard = useCallback(
    (next: Clipboard | null) => {
      if (!next || next.entries.length === 0) {
        clearBusClipboard(instanceId, explorerSourceId)
        setClipboardLocal(null)
        return
      }
      const bus = setBusClipboard(instanceId, next.mode, next.entries, explorerSourceId)
      setClipboardLocal(toClipboard(bus))
    },
    [instanceId, explorerSourceId],
  )
  const [batchOperation, setBatchOperation] = useState<BatchOperationState | null>(null)

  // 编辑器打开的文件。
  const [openFile, setOpenFile] = useState<OpenFile | null>(null)
  /** FR-375：当前打开文件相对 Worker 是否可写（null=未知/探测中）。 */
  const [openFileWritable, setOpenFileWritable] = useState<boolean | null>(null)
  const [blockedPreview, setBlockedPreview] = useState<BlockedPreview | null>(null)
  // 始终持最新 openFile，供事件回调读「未保存」态而不必把 openFile 列入各 useCallback 依赖。
  const openFileRef = useRef<OpenFile | null>(null)
  useEffect(() => {
    openFileRef.current = openFile
  }, [openFile])
  // 配置模式下编辑器在子组件内自管 dirty，经此 ref 上报，供切换/关闭守卫一并判断（BUG-018 #36）。
  const configDirtyRef = useRef(false)
  const setConfigDirty = useCallback((d: boolean) => {
    configDirtyRef.current = d
    // FR-296 淘汰偏好：配置编辑器脏态同步登记到实例草稿注册表。
    reportInstanceDraft(instanceId, 'resource-config', d)
  }, [instanceId])
  const [discardAction, setDiscardAction] = useState<{ action: () => void } | null>(null)

  // 切换/关闭文件前若有未保存草稿则二次确认，避免静默丢失编辑（BUG-018）。
  const hasDiscardConflict = useCallback(
    (nextPath?: string): boolean => needsDiscardConfirm(openFileRef.current, configDirtyRef.current, nextPath),
    [],
  )
  const runOrAskDiscard = useCallback(
    (action: () => void, nextPath?: string) => {
      if (hasDiscardConflict(nextPath)) {
        setDiscardAction({ action })
        return
      }
      action()
    },
    [hasDiscardConflict],
  )

  // 搜索面板开关（FR-074）。打开时占据文件列表列。
  const [searchOpen, setSearchOpen] = useState(false)
  // 归档浏览（jar/zip）/ 反编译（class/jar）视图（FR-075）。与文本编辑器互斥占用右栏。
  const [archiveFor, setArchiveFor] = useState<{ path: string; name: string } | null>(null)
  const [decompileFor, setDecompileFor] = useState<{ path: string; name: string } | null>(null)

  // 对话框/抽屉状态。
  const [prompt, setPrompt] = useState<
    | { kind: 'newFile' | 'newFolder' | 'rename'; initial: string; oldName?: string }
    | null
  >(null)
  const [deleteTargets, setDeleteTargets] = useState<string[] | null>(null)
  const [versionFor, setVersionFor] = useState<string | null>(null)

  // 有序文件名（shift 范围选择 / 全选基于此；与列表展示排序一致，FR-375）。
  const orderedNames = useMemo(() => sortFiles(files, fileSort).map((f) => f.name), [files, fileSort])
  const existingNames = useMemo(() => new Set(orderedNames), [orderedNames])

  /** 拉取某目录内容并复位选中/错误。 */
  const loadDir = useCallback(
    async (dir: string) => {
      setLoading(true)
      setError('')
      try {
        const data = await fetchFileList(instanceId, dir)
        setFiles(data)
        setSelection((s) => pruneSelection(s, data.map((f) => f.name)))
      } catch (err: unknown) {
        const axiosMsg = (err as { response?: { data?: { message?: string } } })?.response?.data
          ?.message
        setError(axiosMsg || (err instanceof Error ? err.message : t('files.loadFailed')))
        setFiles([])
      } finally {
        setLoading(false)
      }
    },
    [instanceId, t],
  )

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- 目录/刷新信号变化时手动拉取列表，属合法同步
    void loadDir(currentDir)
  }, [loadDir, currentDir, treeRefresh])

  /** 切换目录：推导航历史并清空选中（FR-375）。 */
  const navigate = useCallback((dir: string) => {
    setNav((s) => navPush(s, dir))
    setSelection(emptySelection())
  }, [])

  const goBack = useCallback(() => {
    setNav((s) => {
      if (!canNavBack(s)) return s
      return navBack(s)
    })
    setSelection(emptySelection())
  }, [])

  const goForward = useCallback(() => {
    setNav((s) => {
      if (!canNavForward(s)) return s
      return navForward(s)
    })
    setSelection(emptySelection())
  }, [])

  // FR-375：鼠标侧键后退/前进（button 3/4）
  useEffect(() => {
    const el = explorerRootRef.current
    if (!el) return
    const onUp = (e: MouseEvent) => {
      if (e.button === 3) {
        e.preventDefault()
        goBack()
      } else if (e.button === 4) {
        e.preventDefault()
        goForward()
      }
    }
    const onDown = (e: MouseEvent) => {
      if (e.button === 3 || e.button === 4) e.preventDefault()
    }
    el.addEventListener('mouseup', onUp)
    el.addEventListener('mousedown', onDown)
    return () => {
      el.removeEventListener('mouseup', onUp)
      el.removeEventListener('mousedown', onDown)
    }
  }, [goBack, goForward])

  // FR-375：有历史时拦截浏览器后退
  useEffect(() => {
    const onPop = () => {
      setNav((s) => {
        if (!canNavBack(s)) return s
        window.history.pushState({ jmExplorer: 1 }, '')
        return navBack(s)
      })
      setSelection(emptySelection())
    }
    window.history.pushState({ jmExplorer: 1 }, '')
    window.addEventListener('popstate', onPop)
    return () => window.removeEventListener('popstate', onPop)
  }, [])

  const changeViewMode = useCallback((mode: FileViewMode) => {
    setViewMode(mode)
    saveViewMode(mode)
  }, [])

  const changeSort = useCallback((s: FileSortState) => {
    setFileSort(s)
    saveSortState(s)
  }, [])


  /** 整目录刷新（增删改后调用）：刷新列表 + 重置树。 */
  const refreshAll = useCallback(() => {
    setTreeRefresh((n) => n + 1)
  }, [])

  // ---- 选择 ----
  const onRowClick = useCallback(
    (name: string, mods: ClickModifiers) => {
      setSelection((s) => clickSelect(s, name, orderedNames, mods))
    },
    [orderedNames],
  )
  const onSelectAll = useCallback(() => setSelection(selectAllKeys(orderedNames)), [orderedNames])
  const onClearSelection = useCallback(() => setSelection(emptySelection()), [])

  const selectedNames = useMemo(() => [...selection.selected], [selection])
  const selectedPaths = useMemo(
    () => selectedNames.map((n) => joinPath(currentDir, n)),
    [selectedNames, currentDir],
  )

  // ---- 打开（双击 / 收藏·发现面板点选）----
  // 配置模式下编辑器自行读取内容（走配置端点），故只记录打开路径，不预读。
  const refreshOpenWritable = useCallback(
    async (path: string) => {
      try {
        const a = await checkFileAccess(instanceId, path)
        setOpenFileWritable(a.writable)
      } catch {
        setOpenFileWritable(null)
      }
    },
    [instanceId],
  )

  const tryFixOpenFilePerm = useCallback(async () => {
    const f = openFileRef.current
    if (!f) return
    try {
      await chmodFile(instanceId, f.path)
      toast.success(t('files.tryFixPerm'))
      await refreshOpenWritable(f.path)
    } catch (err: unknown) {
      toast.error(errorMessage(err) || t('files.saveFailed'))
    }
  }, [instanceId, refreshOpenWritable, t])

  const openByPathNow = useCallback(
    async (path: string, name: string) => {
      // 打开文本编辑器即关闭归档/反编译视图（右栏互斥）。
      setArchiveFor(null)
      setDecompileFor(null)
      setBlockedPreview(null)
      setOpenFileWritable(null)
      if (configMode) {
        setOpenFile({ path, name, saved: '', draft: '' })
        void refreshOpenWritable(path)
        return
      }
      try {
        const content = await readFileContent(instanceId, path)
        setOpenFile({ path, name, saved: content, draft: content })
        void refreshOpenWritable(path)
      } catch {
        toast.error(t('files.loadFailed'))
      }
    },
    [configMode, instanceId, refreshOpenWritable, t],
  )
  const openByPath = useCallback(
    (path: string, name: string) => {
      runOrAskDiscard(() => { void openByPathNow(path, name) }, path)
    },
    [openByPathNow, runOrAskDiscard],
  )

  const openEntry = useCallback(
    async (file: FileInfo) => {
      if (file.isDir) {
        navigate(joinPath(currentDir, file.name))
        return
      }
      const path = joinPath(currentDir, file.name)
      const blocked = configMode ? null : blockedPreviewFor(file, path)
      if (blocked) {
        runOrAskDiscard(() => {
          setOpenFile(null)
          setArchiveFor(null)
          setDecompileFor(null)
          setBlockedPreview(blocked)
        }, path)
        return
      }
      openByPath(path, file.name)
    },
    [configMode, currentDir, navigate, openByPath, runOrAskDiscard],
  )

  // 搜索命中点击：打开文件并定位到行（FR-074）。filename 模式 line=0 即仅打开不定位。
  // 配置模式下编辑器自行读取内容，gotoLine/gotoNonce 经 renderEditor 透传给配置编辑器定位。
  const openSearchHitNow = useCallback(
    async (path: string, line: number) => {
      const name = path.split('/').pop() || path
      setBlockedPreview(null)
      if (configMode) {
        setOpenFile({
          path,
          name,
          saved: '',
          draft: '',
          gotoLine: line > 0 ? line : undefined,
          gotoNonce: Date.now(),
        })
        return
      }
      try {
        const content = await readFileContent(instanceId, path)
        setOpenFile({
          path,
          name,
          saved: content,
          draft: content,
          gotoLine: line > 0 ? line : undefined,
          gotoNonce: Date.now(),
        })
      } catch {
        toast.error(t('files.loadFailed'))
      }
    },
    [configMode, instanceId, t],
  )
  const openSearchHit = useCallback(
    (path: string, line: number) => {
      runOrAskDiscard(() => { void openSearchHitNow(path, line) }, path)
    },
    [openSearchHitNow, runOrAskDiscard],
  )

  // 暴露「按路径打开」给外部（收藏/发现面板）。
  useEffect(() => {
    if (!openPathRef) return
    openPathRef((path: string) => {
      const name = path.split('/').pop() || path
      void openByPath(path, name)
    })
  }, [openPathRef, openByPath])

  // FR-376：深链/新标签 initialFile 仅挂载时打开一次。
  const initialFileOpened = useRef(false)
  useEffect(() => {
    if (initialFileOpened.current || !initialFile) return
    initialFileOpened.current = true
    const name = initialFile.split('/').pop() || initialFile
    void openByPathNow(initialFile, name)
  }, [initialFile, openByPathNow])

  // FR-376：上下文回传（标签标题 / 脏态）；回调用 ref，避免父组件未 memo 时空转。
  const onContextChangeRef = useRef(onContextChange)
  // 渲染期写入 ref.current 是 React 官方「最新回调」模式；不读 current 参与 JSX。
  // eslint-disable-next-line react-hooks/refs -- 仅同步最新回调指针，不读 ref 做渲染决策
  onContextChangeRef.current = onContextChange
  useEffect(() => {
    onContextChangeRef.current?.({
      dir: currentDir,
      file: openFile?.path,
      dirty: openFile !== null && openFile.draft !== openFile.saved,
    })
  }, [currentDir, openFile])

  // ---- 归档浏览 / 反编译（FR-075）----
  // 三类右栏内容（文本编辑器 / 归档浏览 / 反编译）互斥：打开一个即关闭其余。
  const openArchive = useCallback((file: FileInfo) => {
    const path = joinPath(currentDir, file.name)
    runOrAskDiscard(() => {
      setOpenFile(null)
      setBlockedPreview(null)
      setDecompileFor(null)
      setArchiveFor({ path, name: file.name })
    })
  }, [currentDir, runOrAskDiscard])

  const openDecompile = useCallback((file: FileInfo) => {
    const path = joinPath(currentDir, file.name)
    runOrAskDiscard(() => {
      setOpenFile(null)
      setBlockedPreview(null)
      setArchiveFor(null)
      setDecompileFor({ path, name: file.name })
    })
  }, [currentDir, runOrAskDiscard])

  // ---- 保存（Ctrl+S）----
  // saving 守卫防 Ctrl+S 连击重复提交（FR-324）；toast.loading→success/error 给即时反馈。
  const [saving, setSaving] = useState(false)
  const saveOpenFile = useCallback(async () => {
    if (!openFile || saving) return
    setSaving(true)
    const toastId = toast.loading(t('files.saving'))
    try {
      await writeFileContent(instanceId, openFile.path, openFile.draft)
      setOpenFile((f) => (f ? { ...f, saved: f.draft } : f))
      // 保存改前生成快照（FR-051），失效该文件版本缓存。
      qc.invalidateQueries({ queryKey: ['fileVersions', instanceId, openFile.path] })
      toast.success(t('files.saved'), { id: toastId })
    } catch {
      toast.error(t('files.saveFailed'), { id: toastId })
    } finally {
      setSaving(false)
    }
  }, [openFile, saving, instanceId, qc, t])

  // ---- 新建 / 重命名（PromptDialog）----
  const validateName = useCallback(
    (value: string): string => {
      const name = value.trim()
      if (!isValidName(name)) return t('files.nameInvalid')
      // 重命名到自身允许；其余同名冲突拒绝。
      if (prompt?.kind === 'rename' && name === prompt.oldName) return ''
      if (existingNames.has(name)) return t('files.nameExists')
      return ''
    },
    [existingNames, prompt, t],
  )

  const submitPrompt = useCallback(
    async (value: string) => {
      if (!prompt) return
      const name = value.trim()
      const kind = prompt.kind
      setPrompt(null)
      // toast.loading→success/error：新建/重命名点击后即时反馈（FR-324）。
      const toastId = toast.loading(kind === 'rename' ? t('files.renaming') : t('files.creating'))
      try {
        if (kind === 'newFile') {
          // 后端无独立 create 端点：写空内容即创建文件。
          await writeFileContent(instanceId, joinPath(currentDir, name), '')
          toast.success(t('files.createSuccess'), { id: toastId })
        } else if (kind === 'newFolder') {
          // 通过在新目录下写占位文件创建目录（后端按路径自动建父目录）。
          await writeFileContent(instanceId, joinPath(joinPath(currentDir, name), '.gitkeep'), '')
          toast.success(t('files.createSuccess'), { id: toastId })
        } else if (kind === 'rename' && prompt.oldName) {
          if (name !== prompt.oldName) {
            await renameFile(
              instanceId,
              joinPath(currentDir, prompt.oldName),
              joinPath(currentDir, name),
            )
            toast.success(t('files.renamed'), { id: toastId })
          } else {
            toast.dismiss(toastId)
          }
        }
        refreshAll()
      } catch {
        toast.error(kind === 'rename' ? t('files.renameFailed') : t('files.createFailed'), { id: toastId })
      }
    },
    [prompt, instanceId, currentDir, refreshAll, t],
  )

  // ---- 删除（DangerConfirm 二次确认，FR-059）----
  const confirmDelete = useCallback(async () => {
    if (!deleteTargets) return
    const paths = deleteTargets
    setDeleteTargets(null)
    // 批量删除可能几秒（多文件），toast.loading 给即时反馈（FR-324）。
    const toastId = toast.loading(t('files.deleting'))
    try {
      await Promise.all(paths.map((p) => deleteFile(instanceId, p)))
      // 若删除的是当前打开的文件/归档/反编译目标，关闭对应右栏（避免展示已删条目）。
      if (openFile && paths.includes(openFile.path)) setOpenFile(null)
      if (blockedPreview && paths.includes(blockedPreview.path)) setBlockedPreview(null)
      if (archiveFor && paths.includes(archiveFor.path)) setArchiveFor(null)
      if (decompileFor && paths.includes(decompileFor.path)) setDecompileFor(null)
      toast.success(t('files.deleted'), { id: toastId })
      refreshAll()
    } catch {
      toast.error(t('files.deleteFailed'), { id: toastId })
    }
  }, [deleteTargets, instanceId, openFile, blockedPreview, archiveFor, decompileFor, refreshAll, t])

  // ---- 上传（拖拽 / 按钮，批量逐文件）----
  // 逐文件上传，toast 实时显示当前文件名 + 百分比（FR-324）；批量时带 i/N 计数。
  const handleUpload = useCallback(
    async (fileList: FileList) => {
      const arr = [...fileList]
      const toastId = toast.loading(t('files.uploading'))
      try {
        for (let i = 0; i < arr.length; i++) {
          const f = arr[i]!
          const dest = joinPath(currentDir, f.name)
          const prefix = arr.length > 1 ? `${i + 1}/${arr.length} · ` : ''
          await uploadFile(instanceId, dest, f, (percent) => {
            toast.loading(
              percent < 0 ? `${prefix}${f.name}` : `${prefix}${f.name} ${percent}%`,
              { id: toastId },
            )
          })
          // 覆盖已存在文件会改前快照（FR-051）。
          qc.invalidateQueries({ queryKey: ['fileVersions', instanceId, dest] })
        }
        toast.success(t('files.uploaded'), { id: toastId })
        refreshAll()
      } catch {
        toast.error(t('files.uploadFailed'), { id: toastId })
      }
    },
    [currentDir, instanceId, qc, refreshAll, t],
  )

  // ---- 下载（单文件流式 / 多选 zip）----
  // 下载点击后即时反馈（FR-324）：流式取字节到浏览器期间显「下载中…」，落盘成功即消。
  const downloadSingle = useCallback(
    (file: FileInfo) => {
      const toastId = toast.loading(t('files.downloading'))
      void downloadFile(instanceId, joinPath(currentDir, file.name))
        .then(() => toast.success(t('files.downloaded'), { id: toastId }))
        .catch(() => toast.error(t('files.downloadFailed'), { id: toastId }))
    },
    [instanceId, currentDir, t],
  )
  const downloadSelected = useCallback(() => {
    if (selectedPaths.length === 0) return
    // 单个非目录选中走单文件流式；否则批量 zip。
    const single = selectedPaths.length === 1 && files.find((f) => f.name === selectedNames[0])
    if (single && !single.isDir) {
      downloadSingle(single)
      return
    }
    void downloadArchive(instanceId, selectedPaths, 'files.zip').catch(() =>
      toast.error(t('files.downloadFailed')),
    )
  }, [selectedPaths, selectedNames, files, instanceId, downloadSingle, t])

  // ---- 剪切 / 复制 / 粘贴 / 拖拽移动 ----
  const entriesFor = useCallback(
    (names: string[]): ClipboardEntry[] =>
      names.map((n) => ({
        path: joinPath(currentDir, n),
        isDir: files.find((f) => f.name === n)?.isDir ?? false,
      })),
    [currentDir, files],
  )

  const cutSelection = useCallback(
    (names: string[]) => setClipboard(cutEntries(entriesFor(names))),
    [entriesFor],
  )
  const copySelection = useCallback(
    (names: string[]) => setClipboard(copyEntries(entriesFor(names))),
    [entriesFor],
  )

  const runFileOps = useCallback(
    async (
      label: string,
      ops: PasteOp[],
      skipped: number,
      action: (op: PasteOp) => Promise<void>,
    ): Promise<BatchFailure[]> => {
      const failures: BatchFailure[] = []
      setBatchOperation({ label, total: ops.length, completed: 0, skipped, failed: [], done: false })
      for (const op of ops) {
        try {
          await action(op)
        } catch (err) {
          failures.push({ path: op.from, message: errorMessage(err) })
        } finally {
          setBatchOperation((prev) => (
            prev
              ? { ...prev, completed: Math.min(prev.completed + 1, prev.total), failed: [...failures] }
              : prev
          ))
        }
      }
      setBatchOperation((prev) => (prev ? { ...prev, completed: ops.length, failed: [...failures], done: true } : prev))
      return failures
    },
    [],
  )

  /** 在目标目录粘贴剪贴板内容（move=rename；copy=read+write，仅文件）。 */
  const pasteInto = useCallback(
    async (targetDir: string) => {
      if (!clipboard) return
      // 目标目录已有名字集合：目标==当前目录用现成列表，否则现拉一次。
      let names: Set<string>
      if (targetDir === currentDir) {
        names = existingNames
      } else {
        try {
          const entries = await fetchFileList(instanceId, targetDir)
          names = new Set(entries.map((e) => e.name))
        } catch {
          names = new Set()
        }
      }
      const plan = planPaste(clipboard, targetDir, names)
      if (plan.ops.length === 0) {
        toast.error(t('files.pasteNothing'))
        return
      }
      const failures = await runFileOps(t('files.paste'), plan.ops, plan.skipped.length, async (op) => {
        if (op.kind === 'move') {
          await renameFile(instanceId, op.from, op.to)
          return
        }
        // 复制：读源写目标（仅文件，目录已在 planPaste 中剔除）。
        const content = await readFileContent(instanceId, op.from)
        await writeFileContent(instanceId, op.to, content)
      })
      if (clipboard.mode === 'cut') {
        const failedPaths = new Set(failures.map((failure) => failure.path))
        // FR-377：完整成功则清空总线，避免幽灵剪切
        setClipboard(
          failures.length > 0
            ? cutEntries(clipboard.entries.filter((entry) => failedPaths.has(entry.path)))
            : null,
        )
      }
      if (failures.length === 0) toast.success(t('files.pasteSuccess'))
      else if (failures.length < plan.ops.length) toast.warning(t('files.pastePartialFailed'))
      else toast.error(t('files.pasteFailed'))
      refreshAll()
    },
    [clipboard, currentDir, existingNames, instanceId, refreshAll, runFileOps, t],
  )

  // 拖拽源：记录被拖动的文件名集合（拖单个未选中项时仅拖该项）。
  // FR-377：同时写入 dataTransfer MIME + 总线 drag payload，供跨 Explorer 放置。
  const [dragName, setDragName] = useState<string | null>(null)
  const onDragStartItem = useCallback(
    (name: string, dt: DataTransfer) => {
      // 拖动已选中项时移动整个选区；否则仅移动该项。
      setDragName(name)
      let names: string[]
      if (!selection.selected.has(name)) {
        setSelection(clickSelect(emptySelection(), name, orderedNames))
        names = [name]
      } else {
        names = [...selection.selected]
      }
      writeDragToDataTransfer(dt, instanceId, entriesFor(names))
    },
    [selection, orderedNames, instanceId, entriesFor],
  )
  const runMoveEntries = useCallback(
    (entries: ClipboardEntry[], targetDir: string) => {
      const clip = cutEntries(entries)
      void (async () => {
        let existing: Set<string>
        try {
          const list = await fetchFileList(instanceId, targetDir)
          existing = new Set(list.map((e) => e.name))
        } catch {
          existing = new Set()
        }
        const plan = planPaste(clip, targetDir, existing)
        if (plan.ops.length === 0) {
          toast.error(t('files.pasteNothing'))
          return
        }
        const failures = await runFileOps(t('files.move'), plan.ops, plan.skipped.length, (op) =>
          renameFile(instanceId, op.from, op.to),
        )
        if (failures.length === 0) toast.success(t('files.moveSuccess'))
        else if (failures.length < plan.ops.length) toast.warning(t('files.movePartialFailed'))
        else toast.error(t('files.moveFailed'))
        setDragPayload(null)
        refreshAll()
      })()
    },
    [instanceId, refreshAll, runFileOps, t],
  )
  const onDropMove = useCallback(
    (targetDir: string, dt?: DataTransfer) => {
      // FR-377：优先跨窗 MIME / 总线 payload，其次本组件 dragName
      const remote = readDragFromDataTransfer(dt ?? null, instanceId)
      if (remote && remote.length > 0) {
        setDragName(null)
        runMoveEntries(remote, targetDir)
        return
      }
      if (dragName === null) return
      const names = selection.selected.has(dragName) ? [...selection.selected] : [dragName]
      setDragName(null)
      runMoveEntries(entriesFor(names), targetDir)
    },
    [dragName, selection, entriesFor, instanceId, runMoveEntries],
  )
  /** 列表区放置：资源条目 → 移入 currentDir */
  const onDropEntriesToCurrent = useCallback(
    (dt: DataTransfer) => {
      const remote = readDragFromDataTransfer(dt, instanceId)
      if (!remote || remote.length === 0) return
      runMoveEntries(remote, currentDir)
    },
    [instanceId, currentDir, runMoveEntries],
  )

  const dirty = openFile !== null && openFile.draft !== openFile.saved

  // FR-296 淘汰偏好：文本编辑器脏态登记到实例草稿注册表——热缓存宿主据此优先淘汰
  // 无草稿实例、被迫淘汰带草稿者时 toast 警示。故意无 effect 清理：Activity 隐藏会卸
  // effects 但草稿 DOM 状态仍在；真卸载由宿主 clearInstanceDrafts 统一清签。
  // FR-376：多标签用 draftKey 区分，避免互相覆盖。
  useEffect(() => {
    reportInstanceDraft(instanceId, draftKey, dirty)
  }, [dirty, instanceId, draftKey])

  return (
    <div
      ref={explorerRootRef}
      className="flex h-full min-h-[480px] max-h-full overflow-hidden rounded-lg border"
      data-testid="resource-explorer"
    >
      {/* 左：收藏/发现（配置模式）+ 目录树（滚动隔离 FR-375） */}
      <div className="flex w-56 shrink-0 flex-col overflow-hidden border-r bg-muted/20">
        {config?.sidebarExtra}
        <div className="min-h-0 flex-1 overflow-auto overscroll-contain p-1">
          <FileTree
            instanceId={instanceId}
            currentDir={currentDir}
            onSelectDir={navigate}
            onDropMove={onDropMove}
            refreshKey={treeRefresh}
          />
        </div>
      </div>

      {/* 右：工具栏 + 内容/编辑器 */}
      <div className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden">
        <Toolbar
          currentDir={currentDir}
          selectedCount={selection.selected.size}
          canPaste={clipboard !== null && clipboard.entries.length > 0}
          canBack={canNavBack(nav)}
          canForward={canNavForward(nav)}
          onBack={goBack}
          onForward={goForward}
          onNavigate={navigate}
          onNewFile={() => setPrompt({ kind: 'newFile', initial: '' })}
          onNewFolder={() => setPrompt({ kind: 'newFolder', initial: '' })}
          onUpload={handleUpload}
          onDownloadSelected={downloadSelected}
          onDeleteSelected={() => setDeleteTargets(selectedPaths)}
          onPaste={() => void pasteInto(currentDir)}
          onSelectAll={onSelectAll}
          onClearSelection={onClearSelection}
          onToggleSearch={() => setSearchOpen((v) => !v)}
          searchActive={searchOpen}
          viewMode={viewMode}
          onViewModeChange={changeViewMode}
        />

        <BatchOperationNotice state={batchOperation} onDismiss={() => setBatchOperation(null)} />

        <div className="flex min-h-0 flex-1 overflow-hidden">
          {/* 目录内容列表 / 搜索面板。打开归档/反编译查看器时**整列收起**——目录树仍在左栏可导航，
              查看器（ArchiveViewer flex-1 / DecompileViewer flex-1）占满右栏，避免树｜列表｜查看器三栏挤（FR-111）。
              打开文本编辑器时与编辑器并排 w-1/2（编辑场景需对照文件列表，保留）。 */}
          {!archiveFor && !decompileFor && (
            <div className={openFile || blockedPreview ? 'flex w-1/2 min-h-0 flex-col overflow-hidden border-r' : 'flex min-h-0 flex-1 flex-col overflow-hidden'}>
              {searchOpen ? (
                <SearchPanel
                  instanceId={instanceId}
                  onOpenHit={(path, line) => void openSearchHit(path, line)}
                  onClose={() => setSearchOpen(false)}
                />
              ) : (
                <FileList
                  files={files}
                  loading={loading}
                  error={error}
                  selection={selection}
                  onRowClick={onRowClick}
                  onOpen={openEntry}
                  onDragStartItem={onDragStartItem}
                  onDropUpload={handleUpload}
                  onDropEntries={onDropEntriesToCurrent}
                  onRename={(name) => setPrompt({ kind: 'rename', initial: name, oldName: name })}
                  onDelete={(name) => setDeleteTargets([joinPath(currentDir, name)])}
                  onDownload={downloadSingle}
                  onCut={() => cutSelection(selectedNames.length ? selectedNames : [])}
                  onCopy={() => copySelection(selectedNames.length ? selectedNames : [])}
                  onOpenArchive={openArchive}
                  onDecompile={openDecompile}
                  sort={fileSort}
                  onSortChange={changeSort}
                  viewMode={viewMode}
                />
              )}
            </div>
          )}

          {/* 编辑器：配置模式用注入的配置编辑器，否则默认 CodeEditor */}
          {openFile &&
            (config ? (
              <div className="flex w-1/2 min-w-0 flex-col">
                {/* eslint-disable-next-line react-hooks/refs -- runOrAskDiscard/setConfigDirty 仅在回调内访问 ref，renderEditor 渲染期不读 ref 值 */}
                {config.renderEditor({
                  instanceId,
                  path: openFile.path,
                  name: openFile.name,
                  onClose: () => runOrAskDiscard(() => setOpenFile(null)),
                  onAfterSave: refreshAll,
                  onOpenVersions: () => setVersionFor(openFile.path),
                  onDirtyChange: setConfigDirty,
                  gotoLine: openFile.gotoLine,
                  gotoNonce: openFile.gotoNonce,
                })}
              </div>
            ) : (
              <div className="flex w-1/2 min-w-0 min-h-0 flex-col overflow-hidden">
                <div className="flex shrink-0 items-center justify-between border-b bg-muted/30 px-2 py-1 text-sm">
                  <span className="truncate font-medium">
                    {openFile.name}
                    {dirty && <span className="ml-1 text-amber-500">•</span>}
                    {openFileWritable === false && (
                      <span className="ml-2 text-xs text-muted-foreground">({t('files.readOnly')})</span>
                    )}
                    {openFileWritable === true && (
                      <span className="ml-2 text-xs text-muted-foreground">({t('files.writable')})</span>
                    )}
                  </span>
                  <div className="flex items-center gap-1">
                    <EditorShortcutsHelp />
                    {openFileWritable === false && (
                      <Button
                        size="sm"
                        variant="outline"
                        className="h-7 gap-1 px-2 text-xs"
                        onClick={() => void tryFixOpenFilePerm()}
                      >
                        {t('files.tryFixPerm')}
                      </Button>
                    )}
                    <Button
                      size="sm"
                      variant="ghost"
                      className="h-7 gap-1 px-2 text-xs"
                      onClick={() => setVersionFor(openFile.path)}
                    >
                      <History className="size-3.5" /> {t('fileVersions.title')}
                    </Button>
                    <Button
                      size="sm"
                      variant="outline"
                      className="h-7 gap-1 px-2 text-xs"
                      disabled={!dirty || saving || openFileWritable === false}
                      onClick={() => void saveOpenFile()}
                    >
                      {saving ? <Loader2 className="size-3.5 animate-spin" /> : <Save className="size-3.5" />}{' '}
                      {t('files.save')}
                    </Button>
                    <Button
                      size="sm"
                      variant="ghost"
                      className="h-7 px-1.5"
                      title={t('common.close')}
                      onClick={() => runOrAskDiscard(() => setOpenFile(null))}
                    >
                      <X className="size-3.5" />
                    </Button>
                  </div>
                </div>
                <div className="min-h-0 flex-1">
                  <CodeEditor
                    value={openFile.draft}
                    filename={openFile.name}
                    gotoLine={openFile.gotoLine}
                    gotoNonce={openFile.gotoNonce}
                    onChange={(v) => setOpenFile((f) => (f ? { ...f, draft: v } : f))}
                    onSave={() => void saveOpenFile()}
                  />
                </div>
              </div>
            ))}

          {blockedPreview && (
            <div className="flex w-1/2 min-w-0 flex-col">
              <div className="flex items-center justify-between border-b bg-muted/30 px-2 py-1 text-sm">
                <span className="truncate font-medium">{blockedPreview.name}</span>
                <Button
                  size="sm"
                  variant="ghost"
                  className="h-7 px-1.5"
                  title={t('common.close')}
                  onClick={() => setBlockedPreview(null)}
                >
                  <X className="size-3.5" />
                </Button>
              </div>
              <div className="flex min-h-0 flex-1 flex-col items-center justify-center gap-3 p-6 text-center">
                <FileQuestion className="size-8 text-muted-foreground" />
                <p className="max-w-sm text-sm text-muted-foreground">
                  {blockedPreview.reason === 'binary'
                    ? t('fileBrowser.binaryNotice')
                    : t('fileBrowser.tooLargeNotice', { size: formatBytes(blockedPreview.size) })}
                </p>
                <p className="font-mono text-xs text-muted-foreground">{blockedPreview.path}</p>
                <Button
                  size="sm"
                  variant="outline"
                  className="gap-1"
                  onClick={() => {
                    void downloadFile(instanceId, blockedPreview.path).catch(() =>
                      toast.error(t('files.downloadFailed')),
                    )
                  }}
                >
                  <Download className="size-3.5" /> {t('files.download')}
                </Button>
              </div>
            </div>
          )}

          {/* 归档浏览（jar/zip）：内部条目树 + 只读查看/反编译（FR-075）。 */}
          {archiveFor && (
            <ArchiveViewer
              instanceId={instanceId}
              path={archiveFor.path}
              name={archiveFor.name}
              onClose={() => setArchiveFor(null)}
            />
          )}

          {/* 反编译（工作目录内 .class/.jar）：只读 Java 源码（FR-075）。 */}
          {decompileFor && (
            <DecompileViewer
              instanceId={instanceId}
              path={decompileFor.path}
              name={decompileFor.name}
              onClose={() => setDecompileFor(null)}
            />
          )}
        </div>
      </div>

      {/* 新建 / 重命名输入框 */}
      <PromptDialog
        open={prompt !== null}
        title={
          prompt?.kind === 'rename'
            ? t('files.renameTitle')
            : prompt?.kind === 'newFolder'
              ? t('files.newFolder')
              : t('files.newFile')
        }
        initialValue={prompt?.initial ?? ''}
        validate={validateName}
        onSubmit={(v) => void submitPrompt(v)}
        onCancel={() => setPrompt(null)}
      />

      {/* 删除二次确认（FR-059）。多选时提示数量。 */}
      <DangerConfirm
        open={deleteTargets !== null}
        title={t('files.delete')}
        description={
          deleteTargets && deleteTargets.length > 1
            ? t('files.deleteConfirmMany', { count: deleteTargets.length })
            : t('files.deleteConfirm', { name: deleteTargets ? baseName(deleteTargets[0]) : '' })
        }
        confirmLabel={t('files.delete')}
        scope="group"
        onConfirm={() => void confirmDelete()}
        onCancel={() => setDeleteTargets(null)}
      />

      <Dialog open={discardAction !== null} onOpenChange={(open) => { if (!open) setDiscardAction(null) }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('files.unsavedTitle')}</DialogTitle>
            <DialogDescription>{t('files.unsavedConfirm')}</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDiscardAction(null)}>
              {t('common.cancel')}
            </Button>
            <Button
              variant="destructive"
              onClick={() => {
                const action = discardAction?.action
                setDiscardAction(null)
                action?.()
              }}
            >
              {t('common.confirm')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 历史版本抽屉：配置模式用配置版本抽屉（FR-031），否则文件版本抽屉（FR-051）。 */}
      {config ? (
        config.renderVersionDrawer({
          instanceId,
          filePath: versionFor,
          open: versionFor !== null,
          onOpenChange: (o: boolean) => {
            if (!o) setVersionFor(null)
          },
          onRolledBack: () => {
            // 回滚改变文件内容：配置版本抽屉已失效配置读取缓存，配置编辑器经 React Query 自动重载。
          },
        })
      ) : (
        <VersionDrawer
          instanceId={instanceId}
          filePath={versionFor}
          open={versionFor !== null}
          onOpenChange={(o) => {
            if (!o) setVersionFor(null)
          }}
          onRolledBack={() => {
            // 回滚改变文件内容：若正编辑同一文件，重新载入。
            if (openFile && versionFor === openFile.path) {
              void readFileContent(instanceId, openFile.path).then((content) =>
                setOpenFile((f) => (f ? { ...f, saved: content, draft: content } : f)),
              )
            }
          }}
        />
      )}
    </div>
  )
}

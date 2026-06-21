import { useCallback, useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useQueryClient } from '@tanstack/react-query'
import { History, Save, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
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
  type FileInfo,
} from '@/api/files'
import CodeEditor from './editor/CodeEditor'
import FileTree from './FileTree'
import FileList from './FileList'
import Toolbar from './Toolbar'
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
} from './clipboard'
import { joinPath, baseName, isValidName } from './paths'

/**
 * 共享资源管理器（FR-070）。
 *
 * 双栏：左懒加载目录树 + 右目录内容/编辑器。统管选中/多选/剪贴板/编辑态/历史抽屉。
 * 是 FR-071/073/074/075/082/083/084 复用的入口——对外仅依赖 `instanceId`，
 * 所有文件操作经 `@/api/files`（既有后端端点 + 批量 zip）。
 */
interface ResourceExplorerProps {
  /** 实例 ID。 */
  instanceId: number
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
}

export default function ResourceExplorer({ instanceId }: ResourceExplorerProps) {
  const { t } = useTranslation()
  const qc = useQueryClient()

  // 当前目录（相对工作目录，空串=根）。
  const [currentDir, setCurrentDir] = useState('')
  const [files, setFiles] = useState<FileInfo[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')

  // 树刷新信号（增删改后递增以重置树缓存）。
  const [treeRefresh, setTreeRefresh] = useState(0)

  // 选中态 + 剪贴板。
  const [selection, setSelection] = useState<SelectionState>(emptySelection)
  const [clipboard, setClipboard] = useState<Clipboard | null>(null)

  // 编辑器打开的文件。
  const [openFile, setOpenFile] = useState<OpenFile | null>(null)

  // 对话框/抽屉状态。
  const [prompt, setPrompt] = useState<
    | { kind: 'newFile' | 'newFolder' | 'rename'; initial: string; oldName?: string }
    | null
  >(null)
  const [deleteTargets, setDeleteTargets] = useState<string[] | null>(null)
  const [versionFor, setVersionFor] = useState<string | null>(null)

  // 有序文件名（shift 范围选择 / 全选基于此）。
  const orderedNames = useMemo(() => files.map((f) => f.name), [files])
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

  /** 切换目录（清空选中）。 */
  const navigate = useCallback((dir: string) => {
    setCurrentDir(dir)
    setSelection(emptySelection())
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

  // ---- 打开（双击）----
  const openEntry = useCallback(
    async (file: FileInfo) => {
      if (file.isDir) {
        navigate(joinPath(currentDir, file.name))
        return
      }
      const path = joinPath(currentDir, file.name)
      try {
        const content = await readFileContent(instanceId, path)
        setOpenFile({ path, name: file.name, saved: content, draft: content })
      } catch {
        toast.error(t('files.loadFailed'))
      }
    },
    [currentDir, instanceId, navigate, t],
  )

  // ---- 保存（Ctrl+S）----
  const saveOpenFile = useCallback(async () => {
    if (!openFile) return
    try {
      await writeFileContent(instanceId, openFile.path, openFile.draft)
      setOpenFile((f) => (f ? { ...f, saved: f.draft } : f))
      // 保存改前生成快照（FR-051），失效该文件版本缓存。
      qc.invalidateQueries({ queryKey: ['fileVersions', instanceId, openFile.path] })
      toast.success(t('files.saved'))
    } catch {
      toast.error(t('files.saveFailed'))
    }
  }, [openFile, instanceId, qc, t])

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
      try {
        if (kind === 'newFile') {
          // 后端无独立 create 端点：写空内容即创建文件。
          await writeFileContent(instanceId, joinPath(currentDir, name), '')
          toast.success(t('files.createSuccess'))
        } else if (kind === 'newFolder') {
          // 通过在新目录下写占位文件创建目录（后端按路径自动建父目录）。
          await writeFileContent(instanceId, joinPath(joinPath(currentDir, name), '.gitkeep'), '')
          toast.success(t('files.createSuccess'))
        } else if (kind === 'rename' && prompt.oldName) {
          if (name !== prompt.oldName) {
            await renameFile(
              instanceId,
              joinPath(currentDir, prompt.oldName),
              joinPath(currentDir, name),
            )
            toast.success(t('files.renamed'))
          }
        }
        refreshAll()
      } catch {
        toast.error(kind === 'rename' ? t('files.renameFailed') : t('files.createFailed'))
      }
    },
    [prompt, instanceId, currentDir, refreshAll, t],
  )

  // ---- 删除（DangerConfirm 二次确认，FR-059）----
  const confirmDelete = useCallback(async () => {
    if (!deleteTargets) return
    const paths = deleteTargets
    setDeleteTargets(null)
    try {
      await Promise.all(paths.map((p) => deleteFile(instanceId, p)))
      // 若删除的是当前打开的文件，关闭编辑器。
      if (openFile && paths.includes(openFile.path)) setOpenFile(null)
      toast.success(t('files.deleted'))
      refreshAll()
    } catch {
      toast.error(t('files.deleteFailed'))
    }
  }, [deleteTargets, instanceId, openFile, refreshAll, t])

  // ---- 上传（拖拽 / 按钮，批量逐文件）----
  const handleUpload = useCallback(
    async (fileList: FileList) => {
      const arr = [...fileList]
      try {
        for (const f of arr) {
          const dest = joinPath(currentDir, f.name)
          await uploadFile(instanceId, dest, f)
          // 覆盖已存在文件会改前快照（FR-051）。
          qc.invalidateQueries({ queryKey: ['fileVersions', instanceId, dest] })
        }
        toast.success(t('files.uploaded'))
        refreshAll()
      } catch {
        toast.error(t('files.uploadFailed'))
      }
    },
    [currentDir, instanceId, qc, refreshAll, t],
  )

  // ---- 下载（单文件流式 / 多选 zip）----
  const downloadSingle = useCallback(
    (file: FileInfo) => {
      void downloadFile(instanceId, joinPath(currentDir, file.name)).catch(() =>
        toast.error(t('files.downloadFailed')),
      )
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
      try {
        for (const op of plan.ops) {
          if (op.kind === 'move') {
            await renameFile(instanceId, op.from, op.to)
          } else {
            // 复制：读源写目标（仅文件，目录已在 planPaste 中剔除）。
            const content = await readFileContent(instanceId, op.from)
            await writeFileContent(instanceId, op.to, content)
          }
        }
        // 剪切粘贴后清空剪贴板（移动后源已不存在）。
        if (clipboard.mode === 'cut') setClipboard(null)
        toast.success(t('files.pasteSuccess'))
        refreshAll()
      } catch {
        toast.error(t('files.pasteFailed'))
      }
    },
    [clipboard, currentDir, existingNames, instanceId, refreshAll, t],
  )

  // 拖拽源：记录被拖动的文件名集合（拖单个未选中项时仅拖该项）。
  const [dragName, setDragName] = useState<string | null>(null)
  const onDragStartItem = useCallback(
    (name: string) => {
      // 拖动已选中项时移动整个选区；否则仅移动该项。
      setDragName(name)
      if (!selection.selected.has(name)) {
        setSelection(clickSelect(emptySelection(), name, orderedNames))
      }
    },
    [selection, orderedNames],
  )
  const onDropMove = useCallback(
    (targetDir: string) => {
      if (dragName === null) return
      const names = selection.selected.has(dragName) ? [...selection.selected] : [dragName]
      setDragName(null)
      // 树内拖拽 = 剪切到目标目录（move）。直接构造一次性剪贴板执行。
      const clip = cutEntries(entriesFor(names))
      void (async () => {
        let existing: Set<string>
        try {
          const entries = await fetchFileList(instanceId, targetDir)
          existing = new Set(entries.map((e) => e.name))
        } catch {
          existing = new Set()
        }
        const plan = planPaste(clip, targetDir, existing)
        if (plan.ops.length === 0) {
          toast.error(t('files.pasteNothing'))
          return
        }
        try {
          for (const op of plan.ops) await renameFile(instanceId, op.from, op.to)
          toast.success(t('files.moveSuccess'))
          refreshAll()
        } catch {
          toast.error(t('files.moveFailed'))
        }
      })()
    },
    [dragName, selection, entriesFor, instanceId, refreshAll, t],
  )

  const dirty = openFile !== null && openFile.draft !== openFile.saved

  return (
    <div className="flex h-[600px] overflow-hidden rounded-lg border">
      {/* 左：目录树 */}
      <div className="w-56 shrink-0 overflow-auto border-r bg-muted/20 p-1">
        <FileTree
          instanceId={instanceId}
          currentDir={currentDir}
          onSelectDir={navigate}
          onDropMove={onDropMove}
          refreshKey={treeRefresh}
        />
      </div>

      {/* 右：工具栏 + 内容/编辑器 */}
      <div className="flex min-w-0 flex-1 flex-col">
        <Toolbar
          currentDir={currentDir}
          selectedCount={selection.selected.size}
          canPaste={clipboard !== null && clipboard.entries.length > 0}
          onNavigate={navigate}
          onNewFile={() => setPrompt({ kind: 'newFile', initial: '' })}
          onNewFolder={() => setPrompt({ kind: 'newFolder', initial: '' })}
          onUpload={handleUpload}
          onDownloadSelected={downloadSelected}
          onDeleteSelected={() => setDeleteTargets(selectedPaths)}
          onPaste={() => void pasteInto(currentDir)}
          onSelectAll={onSelectAll}
          onClearSelection={onClearSelection}
        />

        <div className="flex min-h-0 flex-1">
          {/* 目录内容列表 */}
          <div className={openFile ? 'flex w-1/2 flex-col border-r' : 'flex flex-1 flex-col'}>
            <FileList
              files={files}
              loading={loading}
              error={error}
              selection={selection}
              onRowClick={onRowClick}
              onOpen={openEntry}
              onDragStartItem={onDragStartItem}
              onDropUpload={handleUpload}
              onRename={(name) => setPrompt({ kind: 'rename', initial: name, oldName: name })}
              onDelete={(name) => setDeleteTargets([joinPath(currentDir, name)])}
              onDownload={downloadSingle}
              onCut={() => cutSelection(selectedNames.length ? selectedNames : [])}
              onCopy={() => copySelection(selectedNames.length ? selectedNames : [])}
            />
          </div>

          {/* 编辑器 */}
          {openFile && (
            <div className="flex w-1/2 min-w-0 flex-col">
              <div className="flex items-center justify-between border-b bg-muted/30 px-2 py-1 text-sm">
                <span className="truncate font-medium">
                  {openFile.name}
                  {dirty && <span className="ml-1 text-amber-500">•</span>}
                </span>
                <div className="flex items-center gap-1">
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
                    disabled={!dirty}
                    onClick={() => void saveOpenFile()}
                  >
                    <Save className="size-3.5" /> {t('files.save')}
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    className="h-7 px-1.5"
                    title={t('common.close')}
                    onClick={() => setOpenFile(null)}
                  >
                    <X className="size-3.5" />
                  </Button>
                </div>
              </div>
              <div className="min-h-0 flex-1">
                <CodeEditor
                  value={openFile.draft}
                  filename={openFile.name}
                  onChange={(v) => setOpenFile((f) => (f ? { ...f, draft: v } : f))}
                  onSave={() => void saveOpenFile()}
                />
              </div>
            </div>
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

      {/* 历史版本抽屉（FR-051 复用）。 */}
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
    </div>
  )
}

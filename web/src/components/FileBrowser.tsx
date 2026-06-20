import { useState, useEffect, useCallback, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'
import CodeEditor from './CodeEditor'
import FileVersionPanel from './FileVersionPanel'

interface FileInfo {
  name: string
  isDir: boolean
  size: number
  modTime: number
}

interface FileBrowserProps {
  instanceId: number
}

export default function FileBrowser({ instanceId }: FileBrowserProps) {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const [path, setPath] = useState('')
  const [files, setFiles] = useState<FileInfo[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [selectedFile, setSelectedFile] = useState<string | null>(null)
  const [fileContent, setFileContent] = useState<string | null>(null)
  const [editing, setEditing] = useState(false)
  const [editContent, setEditContent] = useState('')
  const [renaming, setRenaming] = useState<string | null>(null)
  const [newName, setNewName] = useState('')
  const [deleteTarget, setDeleteTarget] = useState<string | null>(null)
  const [showVersions, setShowVersions] = useState(false)
  const fileInputRef = useRef<HTMLInputElement>(null)

  // 选中文件的完整路径（含当前目录前缀），供版本面板使用。
  const selectedPath = selectedFile ? (path ? `${path}/${selectedFile}` : selectedFile) : null

  const loadFiles = useCallback(async (dirPath: string) => {
    setLoading(true)
    setError('')
    try {
      const { data } = await api.get<FileInfo[]>(`/instances/${instanceId}/files`, {
        params: { path: dirPath },
      })
      setFiles(data)
      setPath(dirPath)
      setSelectedFile(null)
      setFileContent(null)
    } catch (err: unknown) {
      const axiosMsg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
      const msg = axiosMsg || (err instanceof Error ? err.message : t('files.loadFailed'))
      setError(msg)
    } finally {
      setLoading(false)
    }
  }, [instanceId, t])

  useEffect(() => {
    loadFiles('')
  }, [loadFiles])

  const navigateTo = (dirName: string) => {
    const newPath = path ? `${path}/${dirName}` : dirName
    loadFiles(newPath)
  }

  const navigateUp = () => {
    const parts = path.split('/')
    parts.pop()
    loadFiles(parts.join('/'))
  }

  // reloadContent 重新拉取当前选中文件内容，不影响版本面板开合状态（供回滚后刷新使用）。
  const reloadContent = useCallback(async (filePath: string) => {
    try {
      const { data } = await api.get(`/instances/${instanceId}/files/read`, {
        params: { path: filePath },
        responseType: 'text',
      })
      setFileContent(data)
      setEditContent(data)
    } catch {
      toast.error(t('files.loadFailed'))
    }
  }, [instanceId, t])

  const readFile = async (fileName: string) => {
    const filePath = path ? `${path}/${fileName}` : fileName
    setSelectedFile(fileName)
    setEditing(false)
    setShowVersions(false)
    await reloadContent(filePath)
  }

  const saveFile = async () => {
    if (!selectedFile) return
    const filePath = path ? `${path}/${selectedFile}` : selectedFile
    try {
      await api.post(`/instances/${instanceId}/files/write`, {
        path: filePath,
        content: editContent,
      })
      setFileContent(editContent)
      setEditing(false)
      // 保存会在改前生成快照（FR-051），失效版本列表缓存以便面板刷新。
      qc.invalidateQueries({ queryKey: ['fileVersions', instanceId, filePath] })
      toast.success(t('files.saved'))
    } catch {
      toast.error(t('files.saveFailed'))
    }
  }

  const confirmDelete = async () => {
    if (!deleteTarget) return
    const filePath = path ? `${path}/${deleteTarget}` : deleteTarget
    try {
      await api.delete(`/instances/${instanceId}/files`, { data: { path: filePath } })
      toast.success(t('files.deleted'))
      if (selectedFile === deleteTarget) {
        setSelectedFile(null)
        setFileContent(null)
      }
      loadFiles(path)
    } catch {
      toast.error(t('files.deleteFailed'))
    }
    setDeleteTarget(null)
  }

  const renameFile = async (oldName: string) => {
    if (!newName || newName === oldName) { setRenaming(null); return }
    const oldPath = path ? `${path}/${oldName}` : oldName
    const newPath = path ? `${path}/${newName}` : newName
    try {
      await api.post(`/instances/${instanceId}/files/rename`, { oldPath, newPath })
      setRenaming(null)
      setNewName('')
      toast.success(t('files.renamed'))
      loadFiles(path)
    } catch {
      toast.error(t('files.renameFailed'))
      setRenaming(null)
    }
  }

  const uploadFile = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0]
    if (!file) return
    const formData = new FormData()
    formData.append('file', file)
    formData.append('path', path ? `${path}/${file.name}` : file.name)
    try {
      await api.post(`/instances/${instanceId}/files/upload`, formData, {
        headers: { 'Content-Type': 'multipart/form-data' },
      })
      // 覆盖已存在文件会在改前快照（FR-051），失效该文件版本缓存。
      const uploadedPath = path ? `${path}/${file.name}` : file.name
      qc.invalidateQueries({ queryKey: ['fileVersions', instanceId, uploadedPath] })
      toast.success(t('files.uploaded'))
      loadFiles(path)
    } catch {
      toast.error(t('files.uploadFailed'))
    }
    if (fileInputRef.current) fileInputRef.current.value = ''
  }

  const isEditable = (name: string) => {
    return /\.(yml|yaml|json|txt|conf|cfg|properties|toml|log|md|sh|bat)$/i.test(name)
  }

  const formatSize = (bytes: number) => {
    if (bytes < 1024) return `${bytes} B`
    if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
    return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
  }

  return (
    <>
      <div className="flex gap-4 h-[500px]">
        {/* 文件列表 */}
        <div className="w-64 border rounded-lg overflow-hidden flex flex-col">
          <div className="p-2 border-b bg-muted/50 flex items-center gap-2 text-sm">
            <button onClick={navigateUp} className="px-2 py-0.5 hover:bg-accent rounded" disabled={!path}>
              ↑
            </button>
            <span className="text-muted-foreground truncate flex-1">/{path || ''}</span>
            <button onClick={() => fileInputRef.current?.click()} className="px-2 py-0.5 text-xs bg-green-500/10 text-green-600 rounded hover:bg-green-500/20">
              {t('files.upload')}
            </button>
            <input ref={fileInputRef} type="file" className="hidden" onChange={uploadFile} />
          </div>
          <div className="flex-1 overflow-auto">
            {loading ? (
              <p className="p-3 text-sm text-muted-foreground">{t('files.loading')}</p>
            ) : error ? (
              <p className="p-3 text-sm text-red-500">{error}</p>
            ) : (
              files.map((f) => (
                <div
                  key={f.name}
                  className={`group flex items-center justify-between px-3 py-1.5 text-sm cursor-pointer hover:bg-accent/50 ${
                    selectedFile === f.name ? 'bg-accent' : ''
                  }`}
                  onClick={() => f.isDir ? navigateTo(f.name) : readFile(f.name)}
                  onDoubleClick={(e) => { e.stopPropagation(); setRenaming(f.name); setNewName(f.name) }}
                >
                  {renaming === f.name ? (
                    <input
                      autoFocus
                      value={newName}
                      onChange={(e) => setNewName(e.target.value)}
                      onBlur={() => renameFile(f.name)}
                      onKeyDown={(e) => { if (e.key === 'Enter') renameFile(f.name); if (e.key === 'Escape') setRenaming(null) }}
                      onClick={(e) => e.stopPropagation()}
                      className="flex-1 px-1 py-0 text-sm border rounded bg-background"
                    />
                  ) : (
                    <span className="truncate">
                      {f.isDir ? '📁 ' : '📄 '}{f.name}
                    </span>
                  )}
                  <span className="text-xs text-muted-foreground ml-2 shrink-0">
                    {f.isDir ? '' : formatSize(f.size)}
                  </span>
                </div>
              ))
            )}
            {!loading && files.length === 0 && !error && (
              <p className="p-3 text-sm text-muted-foreground">{t('files.emptyDir')}</p>
            )}
          </div>
        </div>

        {/* 文件内容 */}
        <div className="flex-1 border rounded-lg overflow-hidden flex flex-col">
          {selectedFile ? (
            <>
              <div className="p-2 border-b bg-muted/50 flex items-center justify-between text-sm">
                <span className="font-medium">{selectedFile}</span>
                <div className="flex gap-1">
                  <button
                    onClick={() => setShowVersions((v) => !v)}
                    className={`px-2 py-0.5 text-xs rounded hover:bg-accent ${
                      showVersions ? 'bg-accent' : 'bg-muted/60'
                    }`}
                  >
                    {showVersions ? t('fileVersions.hide') : t('fileVersions.show')}
                  </button>
                  {isEditable(selectedFile) && !editing && (
                    <button
                      onClick={() => { setEditContent(fileContent ?? ''); setEditing(true) }}
                      className="px-2 py-0.5 text-xs bg-blue-500/10 text-blue-600 rounded hover:bg-blue-500/20"
                    >
                      {t('files.edit')}
                    </button>
                  )}
                  {editing && (
                    <>
                      <button
                        onClick={saveFile}
                        className="px-2 py-0.5 text-xs bg-green-500/10 text-green-600 rounded hover:bg-green-500/20"
                      >
                        {t('files.save')}
                      </button>
                      <button
                        onClick={() => { setEditing(false); setEditContent(fileContent ?? '') }}
                        className="px-2 py-0.5 text-xs bg-gray-500/10 rounded hover:bg-gray-500/20"
                      >
                        {t('files.cancel')}
                      </button>
                    </>
                  )}
                  <button
                    onClick={() => setDeleteTarget(selectedFile)}
                    className="px-2 py-0.5 text-xs bg-red-500/10 text-red-600 rounded hover:bg-red-500/20"
                  >
                    {t('files.delete')}
                  </button>
                </div>
              </div>
              <div className={`overflow-hidden p-0 ${showVersions ? 'flex-1 min-h-0' : 'flex-1'}`}>
                <CodeEditor
                  value={editing ? editContent : (fileContent ?? '')}
                  filename={selectedFile}
                  readOnly={!editing}
                  onChange={setEditContent}
                />
              </div>
              {showVersions && selectedPath && (
                <FileVersionPanel
                  instanceId={instanceId}
                  filePath={selectedPath}
                  onRolledBack={() => reloadContent(selectedPath)}
                />
              )}
            </>
          ) : (
            <div className="flex items-center justify-center h-full text-muted-foreground text-sm">
              {t('files.selectFile')}
            </div>
          )}
        </div>
      </div>

      {/* 删除确认对话框 */}
      {deleteTarget && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
          <div className="bg-background border rounded-lg p-6 w-full max-w-sm shadow-lg">
            <h3 className="text-lg font-bold mb-2">确认删除</h3>
            <p className="text-sm text-muted-foreground mb-4">
              确定要删除 <span className="font-mono">{deleteTarget}</span> 吗？此操作不可撤销。
            </p>
            <div className="flex justify-end gap-2">
              <button
                onClick={() => setDeleteTarget(null)}
                className="px-4 py-2 text-sm border rounded-md hover:bg-accent"
              >
                取消
              </button>
              <button
                onClick={confirmDelete}
                className="px-4 py-2 text-sm bg-red-600 text-white rounded-md hover:bg-red-700"
              >
                删除
              </button>
            </div>
          </div>
        </div>
      )}
    </>
  )
}

import { useState, useEffect, useCallback, useRef } from 'react'
import api from '@/api/client'

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
  const fileInputRef = useRef<HTMLInputElement>(null)

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
      const msg = err instanceof Error ? err.message : '加载失败'
      setError(msg)
    } finally {
      setLoading(false)
    }
  }, [instanceId])

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

  const readFile = async (fileName: string) => {
    const filePath = path ? `${path}/${fileName}` : fileName
    setSelectedFile(fileName)
    setEditing(false)
    try {
      const { data } = await api.get(`/instances/${instanceId}/files/read`, {
        params: { path: filePath },
        responseType: 'text',
      })
      setFileContent(data)
      setEditContent(data)
    } catch {
      setFileContent('读取失败')
    }
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
    } catch {
      setError('保存失败')
    }
  }

  const deleteFile = async (fileName: string) => {
    if (!confirm(`确定删除 ${fileName}？`)) return
    const filePath = path ? `${path}/${fileName}` : fileName
    try {
      await api.delete(`/instances/${instanceId}/files`, { data: { path: filePath } })
      loadFiles(path)
    } catch {
      setError('删除失败')
    }
  }

  const renameFile = async (oldName: string) => {
    if (!newName || newName === oldName) { setRenaming(null); return }
    const oldPath = path ? `${path}/${oldName}` : oldName
    const newPath = path ? `${path}/${newName}` : newName
    try {
      await api.post(`/instances/${instanceId}/files/rename`, { oldPath, newPath })
      setRenaming(null)
      setNewName('')
      loadFiles(path)
    } catch {
      setError('重命名失败')
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
      loadFiles(path)
    } catch {
      setError('上传失败')
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
    <div className="flex gap-4 h-[500px]">
      {/* 文件列表 */}
      <div className="w-64 border rounded-lg overflow-hidden flex flex-col">
        <div className="p-2 border-b bg-muted/50 flex items-center gap-2 text-sm">
          <button onClick={navigateUp} className="px-2 py-0.5 hover:bg-accent rounded" disabled={!path}>
            ↑
          </button>
          <span className="text-muted-foreground truncate flex-1">/{path || ''}</span>
          <button onClick={() => fileInputRef.current?.click()} className="px-2 py-0.5 text-xs bg-green-500/10 text-green-600 rounded hover:bg-green-500/20">
            上传
          </button>
          <input ref={fileInputRef} type="file" className="hidden" onChange={uploadFile} />
        </div>
        <div className="flex-1 overflow-auto">
          {loading ? (
            <p className="p-3 text-sm text-muted-foreground">加载中...</p>
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
          {!loading && files.length === 0 && (
            <p className="p-3 text-sm text-muted-foreground">空目录</p>
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
                {isEditable(selectedFile) && !editing && (
                  <button
                    onClick={() => setEditing(true)}
                    className="px-2 py-0.5 text-xs bg-blue-500/10 text-blue-600 rounded hover:bg-blue-500/20"
                  >
                    编辑
                  </button>
                )}
                {editing && (
                  <>
                    <button
                      onClick={saveFile}
                      className="px-2 py-0.5 text-xs bg-green-500/10 text-green-600 rounded hover:bg-green-500/20"
                    >
                      保存
                    </button>
                    <button
                      onClick={() => { setEditing(false); setEditContent(fileContent ?? '') }}
                      className="px-2 py-0.5 text-xs bg-gray-500/10 rounded hover:bg-gray-500/20"
                    >
                      取消
                    </button>
                  </>
                )}
                <button
                  onClick={() => deleteFile(selectedFile)}
                  className="px-2 py-0.5 text-xs bg-red-500/10 text-red-600 rounded hover:bg-red-500/20"
                >
                  删除
                </button>
              </div>
            </div>
            <div className="flex-1 overflow-auto p-0">
              {editing ? (
                <textarea
                  value={editContent}
                  onChange={(e) => setEditContent(e.target.value)}
                  className="w-full h-full p-3 font-mono text-sm bg-background resize-none outline-none"
                  spellCheck={false}
                />
              ) : (
                <pre className="p-3 font-mono text-sm whitespace-pre-wrap break-all">
                  {fileContent}
                </pre>
              )}
            </div>
          </>
        ) : (
          <div className="flex items-center justify-center h-full text-muted-foreground text-sm">
            选择文件查看内容
          </div>
        )}
      </div>
    </div>
  )
}

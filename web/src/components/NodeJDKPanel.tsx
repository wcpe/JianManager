import { useState, type FormEvent } from 'react'
import { toast } from 'sonner'
import { useNodeJDKs, useCreateJDK, useDeleteJDK, useInstallJDK } from '@/api/jdks'

interface NodeJDKPanelProps {
  nodeId: number
}

export default function NodeJDKPanel({ nodeId }: NodeJDKPanelProps) {
  const { data: jdks, isLoading } = useNodeJDKs(nodeId)
  const create = useCreateJDK(nodeId)
  const del = useDeleteJDK(nodeId)
  const install = useInstallJDK(nodeId)

  const [vendor, setVendor] = useState('Temurin')
  const [major, setMajor] = useState('21')
  const [version, setVersion] = useState('')
  const [arch, setArch] = useState('x64')
  const [path, setPath] = useState('')
  const [managed, setManaged] = useState(false)
  const [showRegister, setShowRegister] = useState(false)

  const onRegister = (e: FormEvent) => {
    e.preventDefault()
    create.mutate(
      { vendor, majorVersion: Number(major), version, arch, path, managed },
      {
        onSuccess: () => {
          toast.success('JDK 已登记')
          setVersion('')
          setPath('')
          setShowRegister(false)
        },
        onError: (err: Error & { response?: { data?: { message?: string } } }) => {
          toast.error(err.response?.data?.message || '登记失败')
        },
      }
    )
  }

  const onInstall = () => {
    install.mutate(
      { vendor, majorVersion: Number(major), arch },
      {
        onSuccess: () => toast.success('JDK 安装任务已下发'),
        onError: (err: Error & { response?: { data?: { message?: string } } }) => {
          toast.error(err.response?.data?.message || '安装失败')
        },
      }
    )
  }

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <h3 className="text-sm font-semibold">JDK 注册表</h3>
        <div className="flex gap-2">
          <button
            className="px-3 py-1 text-sm border rounded-md hover:bg-accent"
            onClick={() => setShowRegister((v) => !v)}
          >
            {showRegister ? '收起' : '登记已有'}
          </button>
          <button
            className="px-3 py-1 text-sm bg-primary text-primary-foreground rounded-md disabled:opacity-50"
            onClick={onInstall}
            disabled={install.isPending}
          >
            一键下载
          </button>
        </div>
      </div>

      {showRegister && (
        <form onSubmit={onRegister} className="border rounded-md p-3 grid grid-cols-2 gap-2 text-sm">
          <label>厂商
            <input value={vendor} onChange={(e) => setVendor(e.target.value)} className="w-full mt-1 px-2 py-1 border rounded" />
          </label>
          <label>大版本
            <input value={major} onChange={(e) => setMajor(e.target.value)} className="w-full mt-1 px-2 py-1 border rounded" />
          </label>
          <label>版本号
            <input value={version} onChange={(e) => setVersion(e.target.value)} className="w-full mt-1 px-2 py-1 border rounded" placeholder="21.0.4" />
          </label>
          <label>架构
            <input value={arch} onChange={(e) => setArch(e.target.value)} className="w-full mt-1 px-2 py-1 border rounded" />
          </label>
          <label className="col-span-2">本地路径
            <input value={path} onChange={(e) => setPath(e.target.value)} className="w-full mt-1 px-2 py-1 border rounded" placeholder="C:/Java/jdk-21" required />
          </label>
          <label className="flex items-center gap-2 col-span-2">
            <input type="checkbox" checked={managed} onChange={(e) => setManaged(e.target.checked)} />
            标记为 Worker 托管（仅作记录）
          </label>
          <div className="col-span-2 flex justify-end">
            <button type="submit" className="px-3 py-1 bg-primary text-primary-foreground rounded text-sm disabled:opacity-50" disabled={create.isPending}>
              {create.isPending ? '登记中…' : '保存'}
            </button>
          </div>
        </form>
      )}

      {isLoading ? (
        <p className="text-sm text-muted-foreground">加载中…</p>
      ) : !jdks || jdks.length === 0 ? (
        <p className="text-sm text-muted-foreground">尚未登记 JDK。请用「登记已有」或「一键下载」。</p>
      ) : (
        <table className="w-full text-sm border">
          <thead className="bg-muted">
            <tr>
              <th className="text-left px-2 py-1">厂商</th>
              <th className="text-left px-2 py-1">大版本</th>
              <th className="text-left px-2 py-1">版本</th>
              <th className="text-left px-2 py-1">架构</th>
              <th className="text-left px-2 py-1">路径</th>
              <th className="text-left px-2 py-1">来源</th>
              <th className="text-right px-2 py-1">操作</th>
            </tr>
          </thead>
          <tbody>
            {jdks.map((j) => (
              <tr key={j.id} className="border-t">
                <td className="px-2 py-1">{j.vendor}</td>
                <td className="px-2 py-1">{j.majorVersion}</td>
                <td className="px-2 py-1">{j.version || '—'}</td>
                <td className="px-2 py-1">{j.arch || '—'}</td>
                <td className="px-2 py-1 font-mono text-xs">{j.path}</td>
                <td className="px-2 py-1 text-xs">{j.managed ? '托管' : '外部'}</td>
                <td className="px-2 py-1 text-right">
                  <button
                    className="text-xs text-red-600 hover:underline"
                    onClick={() => {
                      if (!confirm(`确认删除 ${j.vendor} ${j.majorVersion}？`)) return
                      del.mutate(j.id, {
                        onSuccess: () => toast.success('已删除'),
                        onError: (err: Error & { response?: { data?: { message?: string } } }) => {
                          toast.error(err.response?.data?.message || '删除失败')
                        },
                      })
                    }}
                  >
                    删除
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}

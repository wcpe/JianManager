import { useState, type FormEvent } from 'react'
import { useQueryClient, useMutation } from '@tanstack/react-query'
import api from '@/api/client'

interface CreateUserDialogProps {
  open: boolean
  onClose: () => void
}

export default function CreateUserDialog({ open, onClose }: CreateUserDialogProps) {
  const qc = useQueryClient()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [role, setRole] = useState(0)
  const [error, setError] = useState('')

  const create = useMutation({
    mutationFn: (body: { username: string; password: string }) =>
      api.post('/auth/register', body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['users'] })
      onClose()
      resetForm()
    },
    onError: (err: Error & { response?: { data?: { message?: string } } }) => {
      setError(err.response?.data?.message || '创建失败')
    },
  })

  const resetForm = () => {
    setUsername('')
    setPassword('')
    setRole(0)
    setError('')
  }

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault()
    setError('')
    create.mutate({ username, password })
  }

  if (!open) return null

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
      <div className="bg-background border rounded-lg p-6 w-full max-w-sm shadow-lg">
        <h2 className="text-lg font-bold mb-4">创建用户</h2>

        {error && (
          <div className="mb-3 p-2 text-sm text-destructive bg-destructive/10 rounded">{error}</div>
        )}

        <form onSubmit={handleSubmit} className="space-y-3">
          <div>
            <label className="text-sm font-medium">用户名</label>
            <input
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm"
              required
              minLength={3}
            />
          </div>

          <div>
            <label className="text-sm font-medium">密码</label>
            <input
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm"
              required
              minLength={6}
            />
          </div>

          <div>
            <label className="text-sm font-medium">角色</label>
            <select
              value={role}
              onChange={(e) => setRole(Number(e.target.value))}
              className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm"
            >
              <option value={0}>组成员</option>
              <option value={1}>组管理员</option>
              <option value={10}>平台管理员</option>
            </select>
          </div>

          <div className="flex justify-end gap-2 pt-2">
            <button
              type="button"
              onClick={() => { onClose(); resetForm() }}
              className="px-4 py-2 text-sm border rounded-md hover:bg-accent"
            >
              取消
            </button>
            <button
              type="submit"
              disabled={create.isPending}
              className="px-4 py-2 text-sm bg-primary text-primary-foreground rounded-md disabled:opacity-50"
            >
              {create.isPending ? '创建中...' : '创建'}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}

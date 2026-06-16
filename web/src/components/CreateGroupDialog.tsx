import { useState, type FormEvent } from 'react'
import { useCreateGroup } from '@/api/groups'

interface CreateGroupDialogProps {
  open: boolean
  onClose: () => void
}

export default function CreateGroupDialog({ open, onClose }: CreateGroupDialogProps) {
  const create = useCreateGroup()
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [error, setError] = useState('')

  const resetForm = () => {
    setName('')
    setDescription('')
    setError('')
  }

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault()
    setError('')
    create.mutate(
      { name, description },
      {
        onSuccess: () => {
          onClose()
          resetForm()
        },
        onError: (err: Error & { response?: { data?: { message?: string } } }) => {
          setError(err.response?.data?.message || '创建失败')
        },
      },
    )
  }

  if (!open) return null

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
      <div className="bg-background border rounded-lg p-6 w-full max-w-sm shadow-lg">
        <h2 className="text-lg font-bold mb-4">创建用户组</h2>

        {error && (
          <div className="mb-3 p-2 text-sm text-destructive bg-destructive/10 rounded">{error}</div>
        )}

        <form onSubmit={handleSubmit} className="space-y-3">
          <div>
            <label className="text-sm font-medium">组名</label>
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm"
              required
            />
          </div>

          <div>
            <label className="text-sm font-medium">描述</label>
            <textarea
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm"
              rows={3}
            />
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

import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { useQueryClient, useMutation } from '@tanstack/react-query'
import api from '@/api/client'
import { useNodes } from '@/api/nodes'
import { useGroups } from '@/api/groups'
import { useTemplates } from '@/api/templates'

interface CreateInstanceDialogProps {
  open: boolean
  onClose: () => void
}

export default function CreateInstanceDialog({ open, onClose }: CreateInstanceDialogProps) {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const { data: nodes } = useNodes()
  const { data: groups } = useGroups()
  const { data: templates } = useTemplates()

  const [name, setName] = useState('')
  const [nodeId, setNodeId] = useState('')
  const [type, setType] = useState('minecraft_java')
  const [processType, setProcessType] = useState('daemon')
  const [startCommand, setStartCommand] = useState('')
  const [workDir, setWorkDir] = useState('')
  const [autoRestart, setAutoRestart] = useState(true)
  const [groupId, setGroupId] = useState('')
  const [templateId, setTemplateId] = useState('')
  const [error, setError] = useState('')

  const create = useMutation({
    mutationFn: (body: Record<string, unknown>) => api.post('/instances', body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['instances'] })
      onClose()
      resetForm()
    },
    onError: (err: Error & { response?: { data?: { message?: string } } }) => {
      setError(err.response?.data?.message || t('instances.createFailed'))
    },
  })

  const resetForm = () => {
    setName('')
    setNodeId('')
    setType('minecraft_java')
    setProcessType('daemon')
    setStartCommand('')
    setWorkDir('')
    setAutoRestart(true)
    setGroupId('')
    setTemplateId('')
    setError('')
  }

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault()
    setError('')
    create.mutate({
      nodeId: Number(nodeId),
      name,
      type,
      processType,
      startCommand,
      workDir,
      autoRestart,
      groupId: groupId ? Number(groupId) : undefined,
    })
  }

  if (!open) return null

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
      <div className="bg-background border rounded-lg p-6 w-full max-w-md shadow-lg">
        <h2 className="text-lg font-bold mb-4">{t('instances.createInstance')}</h2>

        {error && (
          <div className="mb-3 p-2 text-sm text-destructive bg-destructive/10 rounded">{error}</div>
        )}

        <form onSubmit={handleSubmit} className="space-y-3">
          <div>
            <label className="text-sm font-medium">{t('instances.instanceName')}</label>
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm"
              placeholder="Survival Server"
              required
            />
          </div>

          <div>
            <label className="text-sm font-medium">{t('instances.node')}</label>
            <select
              value={nodeId}
              onChange={(e) => setNodeId(e.target.value)}
              className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm"
              required
            >
              <option value="">{t('instances.selectNode')}</option>
              {nodes?.filter(n => n.status === 1).map((n) => (
                <option key={n.id} value={n.id}>{n.name}</option>
              ))}
            </select>
          </div>

          <div className="grid grid-cols-2 gap-3">
            <div>
              <label className="text-sm font-medium">{t('instances.type')}</label>
              <select
                value={type}
                onChange={(e) => setType(e.target.value)}
                className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm"
              >
                <option value="minecraft_java">Minecraft Java</option>
                <option value="generic">{t('common.type')}</option>
              </select>
            </div>
            <div>
              <label className="text-sm font-medium">{t('templates.selectTemplate').replace(/[（(].*[）)]/, '').trim()}</label>
              <select
                value={templateId}
                onChange={(e) => {
                  const tid = e.target.value
                  setTemplateId(tid)
                  if (tid) {
                    const tpl = templates?.find(t => String(t.id) === tid)
                    if (tpl) {
                      setStartCommand(tpl.startCommand)
                      setType(tpl.type || type)
                    }
                  }
                }}
                className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm"
              >
                <option value="">{t('templates.noTemplate')}</option>
                {templates?.map((t) => (
                  <option key={t.id} value={t.id}>{t.name}</option>
                ))}
              </select>
            </div>
            <div>
              <label className="text-sm font-medium">{t('instanceDetail.processType')}</label>
              <select
                value={processType}
                onChange={(e) => setProcessType(e.target.value)}
                className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm"
              >
                <option value="daemon">daemon ({t('common.enabled')})</option>
                <option value="direct">direct</option>
                <option value="docker">docker</option>
              </select>
            </div>
          </div>

          <div>
            <label className="text-sm font-medium">{t('instanceDetail.startCommand')}</label>
            <input
              value={startCommand}
              onChange={(e) => setStartCommand(e.target.value)}
              className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm font-mono"
              placeholder="java -Xmx2G -jar paper.jar nogui"
              required
            />
          </div>

          <div>
            <label className="text-sm font-medium">{t('instanceDetail.workDir')}</label>
            <input
              value={workDir}
              onChange={(e) => setWorkDir(e.target.value)}
              className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm"
              placeholder="/servers/survival"
            />
          </div>

          <div>
            <label className="text-sm font-medium">{t('instances.group')}</label>
            <select
              value={groupId}
              onChange={(e) => setGroupId(e.target.value)}
              className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm"
            >
              <option value="">{t('instances.noGroup')}</option>
              {groups?.map((g) => (
                <option key={g.id} value={g.id}>{g.name}</option>
              ))}
            </select>
          </div>

          <label className="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={autoRestart}
              onChange={(e) => setAutoRestart(e.target.checked)}
            />
            {t('instanceDetail.autoRestart')}
          </label>

          <div className="flex justify-end gap-2 pt-2">
            <button
              type="button"
              onClick={() => { onClose(); resetForm() }}
              className="px-4 py-2 text-sm border rounded-md hover:bg-accent"
            >
              {t('common.cancel')}
            </button>
            <button
              type="submit"
              disabled={create.isPending}
              className="px-4 py-2 text-sm bg-primary text-primary-foreground rounded-md disabled:opacity-50"
            >
              {create.isPending ? t('common.creating') : t('common.create')}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}

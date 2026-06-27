import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useInstances } from '@/api/instances'
import { useCloneInstance, type CloneResult } from '@/api/clone'
import { MODAL_OVERLAY, MODAL_PANEL } from '@/components/ui/scrollable-dialog'
import { Checkbox } from '@/components/ui/checkbox'
import { FieldLabel, FieldError } from '@/components/ui/field-label'
import { validateRequired } from '@/lib/form-validation'

interface CloneInstanceDialogProps {
  sourceId: number
  sourceName: string
  onClose: () => void
}

/**
 * 一键复制子服向导（FR-036）：复制为独立新实例，系统分配新目录/端口，排除运行态文件，
 * 修正端口/motd 并可选注册进代理。支持预检（dryRun）。
 */
export default function CloneInstanceDialog({ sourceId, sourceName, onClose }: CloneInstanceDialogProps) {
  const { t } = useTranslation()
  const { data: proxies } = useInstances({ role: 'proxy' })
  const clone = useCloneInstance(sourceId)

  const [name, setName] = useState(`${sourceName}-copy`)
  const [motd, setMotd] = useState('')
  const [levelName, setLevelName] = useState('')
  const [proxyIds, setProxyIds] = useState<number[]>([])
  const [preview, setPreview] = useState<CloneResult | null>(null)

  const nameError = validateRequired(name)

  const body = () => ({
    name,
    motd: motd.trim() || undefined,
    levelName: levelName.trim() || undefined,
    registerToProxyIds: proxyIds.length ? proxyIds : undefined,
  })

  const runPreview = () => {
    clone.mutate(
      { ...body(), dryRun: true },
      {
        onSuccess: (res) => setPreview(res),
        onError: (err: Error & { response?: { data?: { error?: string; message?: string } } }) => {
          const code = err.response?.data?.error
          toast.error(code === 'SOURCE_RUNNING' ? t('clone.sourceRunning') : err.response?.data?.message || t('clone.failed'))
        },
      },
    )
  }

  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (nameError) return
    clone.mutate(body(), {
      onSuccess: (res) => {
        toast.success(t('clone.success', { name }))
        ;(res.warnings || []).forEach((w) => toast.warning(w))
        onClose()
      },
      onError: (err: Error & { response?: { data?: { error?: string; message?: string } } }) => {
        const code = err.response?.data?.error
        toast.error(code === 'SOURCE_RUNNING' ? t('clone.sourceRunning') : err.response?.data?.message || t('clone.failed'))
      },
    })
  }

  return (
    <div className={MODAL_OVERLAY}>
      <div className={`${MODAL_PANEL} max-w-md`}>
        <h2 className="text-lg font-bold mb-4">{t('clone.title', { name: sourceName })}</h2>
        <form onSubmit={submit} className="space-y-3">
          <div>
            <FieldLabel required>{t('clone.name')}</FieldLabel>
            <input value={name} onChange={(e) => setName(e.target.value)}
              aria-invalid={!!nameError}
              className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm aria-invalid:border-destructive" />
            <FieldError error={nameError} />
          </div>
          <div className="grid grid-cols-2 gap-3">
            <div>
              <FieldLabel>{t('clone.motd')}</FieldLabel>
              <input value={motd} onChange={(e) => setMotd(e.target.value)}
                className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm" />
            </div>
            <div>
              <FieldLabel>{t('clone.levelName')}</FieldLabel>
              <input value={levelName} onChange={(e) => setLevelName(e.target.value)} placeholder="world"
                className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm" />
            </div>
          </div>

          <div>
            <FieldLabel>{t('clone.registerTo')}</FieldLabel>
            {proxies && proxies.length > 0 ? (
              <div className="mt-1 border rounded-md p-2 space-y-1 max-h-32 overflow-y-auto">
                {proxies.map((p) => (
                  <label key={p.id} className="flex items-center gap-2 text-sm">
                    <Checkbox checked={proxyIds.includes(p.id)} aria-label={p.name}
                      onCheckedChange={(v) => setProxyIds((prev) => (v === true ? [...prev, p.id] : prev.filter((x) => x !== p.id)))} />
                    {p.name}
                  </label>
                ))}
              </div>
            ) : (
              <p className="mt-1 text-xs text-muted-foreground">{t('clone.noProxies')}</p>
            )}
          </div>

          {preview && (
            <div className="text-xs bg-muted/40 rounded-md p-3 space-y-1">
              <p>{t('clone.allocated', {
                server: preview.allocated.serverPort,
                query: preview.allocated.queryPort,
                workDir: preview.allocated.workDir,
              })}</p>
              <p className="text-muted-foreground">{t('clone.excluded', { list: preview.excluded.join(', ') })}</p>
              {(preview.warnings || []).map((w, i) => (<p key={i} className="text-amber-600">⚠ {w}</p>))}
            </div>
          )}

          <div className="flex justify-end gap-2 pt-2">
            <button type="button" onClick={onClose} className="px-4 py-2 text-sm border rounded-md hover:bg-accent">
              {t('common.cancel')}
            </button>
            <button type="button" onClick={runPreview} disabled={clone.isPending || !name}
              className="px-4 py-2 text-sm border rounded-md hover:bg-accent disabled:opacity-50">
              {t('clone.preview')}
            </button>
            <button type="submit" disabled={clone.isPending || !name}
              className="px-4 py-2 text-sm bg-primary text-primary-foreground rounded-md disabled:opacity-50">
              {clone.isPending ? t('clone.cloning') : t('clone.submit')}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}

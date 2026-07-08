import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useInstances } from '@/api/instances'
import { useCloneInstance, type CloneResult } from '@/api/clone'
import { Button } from '@jianmanager/ui/components/button'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { scrollableDialogContentClass, ScrollableDialogBody } from '@jianmanager/ui/components/scrollable-dialog'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
import { FieldLabel, FieldError } from '@jianmanager/ui/components/field-label'
import { validateRequired } from '@/lib/form-validation'
import { cn } from '@jianmanager/ui'

/** 把逗号/换行分隔的 glob 串解析为数组（FR-231 高级复制筛选）。 */
const parseGlobs = (s: string) => s.split(/[\n,]/).map((x) => x.trim()).filter(Boolean)

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
  // 复制模式（FR-231）：quick=核心+插件+根配置；advanced=按 include/exclude 筛选。
  const [mode, setMode] = useState<'quick' | 'advanced'>('quick')
  const [include, setInclude] = useState('')
  const [exclude, setExclude] = useState('')

  const nameError = validateRequired(name)

  const body = () => ({
    name,
    motd: motd.trim() || undefined,
    levelName: levelName.trim() || undefined,
    registerToProxyIds: proxyIds.length ? proxyIds : undefined,
    mode,
    include: mode === 'advanced' ? parseGlobs(include) : undefined,
    exclude: mode === 'advanced' ? parseGlobs(exclude) : undefined,
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
    <Dialog open onOpenChange={(open) => { if (!open) onClose() }}>
      <DialogContent
        className={`${scrollableDialogContentClass} sm:max-w-md`}
        showCloseButton={false}
        onInteractOutside={(event) => event.preventDefault()}
      >
        <form onSubmit={submit} className="flex min-h-0 flex-1 flex-col">
          <DialogHeader>
            <DialogTitle>{t('clone.title', { name: sourceName })}</DialogTitle>
          </DialogHeader>

          <ScrollableDialogBody className="space-y-3 py-2">
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

            {/* 复制范围：快速 / 高级（FR-231） */}
            <div>
              <FieldLabel>{t('clone.mode', '复制范围')}</FieldLabel>
              <div className="mt-1 flex gap-1 rounded-md border bg-muted/30 p-1 text-sm">
                {(['quick', 'advanced'] as const).map((m) => (
                  <button
                    key={m}
                    type="button"
                    onClick={() => setMode(m)}
                    className={cn(
                      'flex-1 rounded px-2 py-1.5 transition-colors',
                      mode === m ? 'bg-background font-medium shadow-sm' : 'text-muted-foreground hover:text-foreground',
                    )}
                  >
                    {t(`clone.mode_${m}`, m === 'quick' ? '快速复制' : '高级复制')}
                  </button>
                ))}
              </div>
              {mode === 'quick' ? (
                <p className="mt-1 text-xs text-muted-foreground">{t('clone.quickHint', '仅复制核心 jar + 所有插件 + 根配置（server.properties 及根 *.yml/*.properties），不含世界 / 日志 / 缓存。')}</p>
              ) : (
                <div className="mt-2 space-y-2">
                  <div>
                    <FieldLabel>{t('clone.include', '包含（顶层项，留空=全部）')}</FieldLabel>
                    <input value={include} onChange={(e) => setInclude(e.target.value)} placeholder="plugins, *.jar, world"
                      className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm" />
                  </div>
                  <div>
                    <FieldLabel>{t('clone.exclude', '排除（顶层项）')}</FieldLabel>
                    <input value={exclude} onChange={(e) => setExclude(e.target.value)} placeholder="world, dynmap"
                      className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm" />
                  </div>
                  <p className="text-xs text-muted-foreground">{t('clone.advancedHint', '按逗号 / 换行分隔的顶层项（目录名或 *.jar 等通配）；运行态垃圾始终排除。')}</p>
                </div>
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
          </ScrollableDialogBody>

          <DialogFooter className="flex-row justify-end pt-2">
            <Button type="button" variant="outline" onClick={onClose}>
              {t('common.cancel')}
            </Button>
            <Button type="button" variant="outline" onClick={runPreview} disabled={clone.isPending || !name}>
              {t('clone.preview')}
            </Button>
            <Button type="submit" disabled={clone.isPending || !name}>
              {clone.isPending ? t('clone.cloning') : t('clone.submit')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

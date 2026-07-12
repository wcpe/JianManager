import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router'
import { toast } from 'sonner'
import { HardDriveDownload, MapPin, Truck } from 'lucide-react'
import { useNodes } from '@/api/nodes'
import { useNodeJDKs } from '@/api/jdks'
import {
  useInspectImportDir,
  useImportServer,
  type ImportInspectResult,
} from '@/api/importServer'
import DirectoryPicker from '@/components/DirectoryPicker'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { scrollableDialogContentClass, ScrollableDialogBody } from '@jianmanager/ui/components/scrollable-dialog'
import { Combobox, type ComboboxOption } from '@jianmanager/ui/components/combobox'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
import { FieldLabel } from '@jianmanager/ui/components/field-label'
import { Button } from '@jianmanager/ui/components/button'

interface ImportServerWizardProps {
  open: boolean
  onClose: () => void
  /** 预选节点（节点页入口传当前节点；实例列表入口不传，由向导内选择）。 */
  initialNodeId?: number
}

type Step = 'dir' | 'inspect' | 'mode' | 'config'

/** 人类可读文件大小（jar 候选展示）。 */
function formatSize(bytes: number): string {
  if (bytes >= 1024 * 1024) return `${(bytes / 1024 / 1024).toFixed(1)} MB`
  if (bytes >= 1024) return `${(bytes / 1024).toFixed(0)} KB`
  return `${bytes} B`
}

/** 从目录路径提取默认实例名（最后一段）。 */
function dirBaseName(path: string): string {
  const parts = path.replace(/[\\/]+$/, '').split(/[\\/]/)
  return parts[parts.length - 1] || 'imported-server'
}

/**
 * 导入现有服务器向导（FR-302，见 ADR-XXXX）：选目录 → 探测结果（jar 单选 / JDK 勾选 /
 * 端口 eula 展示）→ 模式二选一（就地接管 / 搬进托管区，含后果说明）→ 名称/内存/JDK → 提交跳实例页。
 * 模态承载 + 内容自适应（ui-modals 纪律，scrollable-dialog 壳）。
 */
export default function ImportServerWizard({ open, onClose, initialNodeId }: ImportServerWizardProps) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { data: nodes } = useNodes()

  const [step, setStep] = useState<Step>('dir')
  const [nodeId, setNodeId] = useState(initialNodeId ? String(initialNodeId) : '')
  const [path, setPath] = useState('')
  const [result, setResult] = useState<ImportInspectResult | null>(null)
  const [jarPath, setJarPath] = useState('')
  const [jdkPaths, setJdkPaths] = useState<string[]>([])
  const [mode, setMode] = useState<'in_place' | 'migrate'>('in_place')
  const [name, setName] = useState('')
  const [memoryMb, setMemoryMb] = useState('2048')
  const [jdkId, setJdkId] = useState('')

  const { data: jdks } = useNodeJDKs(nodeId ? Number(nodeId) : 0)
  const inspect = useInspectImportDir()
  const importServer = useImportServer()

  const nodeOptions: ComboboxOption[] = (nodes ?? [])
    .filter((n) => n.status === 1)
    .map((n) => ({ value: String(n.id), label: n.name }))
  const jdkOptions: ComboboxOption[] = (jdks ?? []).map((j) => ({
    value: String(j.id),
    label: `${j.vendor} ${j.majorVersion} (${j.version})`,
  }))

  const reset = () => {
    setStep('dir')
    setNodeId(initialNodeId ? String(initialNodeId) : '')
    setPath('')
    setResult(null)
    setJarPath('')
    setJdkPaths([])
    setMode('in_place')
    setName('')
    setMemoryMb('2048')
    setJdkId('')
    inspect.reset()
  }

  const close = () => {
    onClose()
    reset()
  }

  const runInspect = (picked: string) => {
    setPath(picked)
    inspect.mutate(
      { nodeId: Number(nodeId), path: picked },
      {
        onSuccess: (res) => {
          setResult(res)
          setJarPath(res.jars[0]?.path ?? '')
          setJdkPaths([])
          if (!name) setName(dirBaseName(picked))
          setStep('inspect')
        },
        onError: (err: Error & { response?: { data?: { message?: string } } }) => {
          toast.error(err.response?.data?.message || t('importServer.inspectFailed'))
        },
      },
    )
  }

  const toggleJdk = (p: string) =>
    setJdkPaths((prev) => (prev.includes(p) ? prev.filter((x) => x !== p) : [...prev, p]))

  const submit = () => {
    importServer.mutate(
      {
        nodeId: Number(nodeId),
        path,
        mode,
        name: name.trim(),
        jarPath,
        jdkId: jdkId ? Number(jdkId) : undefined,
        registerJdkPaths: jdkPaths.length > 0 ? jdkPaths : undefined,
        memoryMb: memoryMb ? Number(memoryMb) : undefined,
      },
      {
        onSuccess: (inst) => {
          toast.success(t('importServer.success', { name: inst.name }))
          close()
          navigate(`/instances/${inst.id}`)
        },
        onError: (err: Error & { response?: { data?: { message?: string } } }) => {
          toast.error(err.response?.data?.message || t('importServer.failed'))
        },
      },
    )
  }

  if (!open) return null

  const stepTitle: Record<Step, string> = {
    dir: t('importServer.stepDir'),
    inspect: t('importServer.stepInspect'),
    mode: t('importServer.stepMode'),
    config: t('importServer.stepConfig'),
  }

  return (
    <Dialog open={open} onOpenChange={(next) => { if (!next) close() }}>
      <DialogContent
        showCloseButton={false}
        onPointerDownOutside={(event) => event.preventDefault()}
        className={`${scrollableDialogContentClass} sm:max-w-lg`}
      >
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <HardDriveDownload className="size-4" />
            {t('importServer.title')}
          </DialogTitle>
          <DialogDescription className="text-xs">{stepTitle[step]}</DialogDescription>
        </DialogHeader>

        <ScrollableDialogBody className="space-y-3 py-2">
          {step === 'dir' && (
            <>
              <div>
                <FieldLabel required>{t('importServer.nodeLabel')}</FieldLabel>
                <div className="mt-1">
                  <Combobox
                    options={nodeOptions}
                    value={nodeId}
                    onChange={(v) => { setNodeId(v); setPath('') }}
                    allowCustom={false}
                    placeholder={t('importServer.selectNode')}
                  />
                </div>
              </div>
              {nodeId ? (
                <div>
                  <FieldLabel>{t('importServer.dirLabel')}</FieldLabel>
                  <div className="mt-1">
                    <DirectoryPicker
                      key={nodeId}
                      nodeId={Number(nodeId)}
                      onPick={runInspect}
                      onCancel={close}
                    />
                  </div>
                  {inspect.isPending && (
                    <p className="mt-1 text-xs text-muted-foreground">{t('importServer.inspecting')}</p>
                  )}
                </div>
              ) : (
                <p className="text-xs text-muted-foreground">{t('importServer.selectNodeFirst')}</p>
              )}
            </>
          )}

          {step === 'inspect' && result && (
            <>
              <p className="break-all rounded bg-muted/40 px-2 py-1 font-mono text-xs" title={path}>{path}</p>

              <div>
                <FieldLabel required>{t('importServer.jarSection')}</FieldLabel>
                {result.jars.length === 0 ? (
                  <p className="mt-1 text-sm text-destructive">{t('importServer.noJars')}</p>
                ) : (
                  <div className="mt-1 max-h-48 space-y-1 overflow-y-auto rounded border p-1.5">
                    {result.jars.map((j) => (
                      <label key={j.path} className="flex cursor-pointer items-start gap-2 rounded px-1.5 py-1 text-sm hover:bg-accent">
                        <input
                          type="radio"
                          name="import-jar"
                          className="mt-1"
                          checked={jarPath === j.path}
                          onChange={() => setJarPath(j.path)}
                        />
                        <span className="min-w-0 flex-1">
                          <span className="block truncate font-mono text-xs">{j.path}</span>
                          <span className="block text-xs text-muted-foreground">
                            {formatSize(j.size)}
                            {j.mainClassHint ? ` · Main-Class: ${j.mainClassHint}` : ''}
                          </span>
                        </span>
                      </label>
                    ))}
                  </div>
                )}
              </div>

              <div>
                <FieldLabel>{t('importServer.jdkSection')}</FieldLabel>
                {result.jdks.length === 0 ? (
                  <p className="mt-1 text-xs text-muted-foreground">{t('importServer.noJdks')}</p>
                ) : (
                  <div className="mt-1 space-y-1 rounded border p-1.5">
                    {result.jdks.map((j) => (
                      <label key={j.path} className="flex cursor-pointer items-start gap-2 rounded px-1.5 py-1 text-sm hover:bg-accent">
                        <Checkbox
                          checked={jdkPaths.includes(j.path)}
                          onCheckedChange={() => toggleJdk(j.path)}
                          aria-label={j.path}
                        />
                        <span className="min-w-0 flex-1">
                          <span className="block text-sm">{j.vendor} {j.majorVersion} ({j.version}, {j.arch})</span>
                          <span className="block truncate font-mono text-xs text-muted-foreground">{j.path}</span>
                        </span>
                      </label>
                    ))}
                  </div>
                )}
              </div>

              <div className="grid grid-cols-2 gap-3 text-sm">
                <div>
                  <span className="text-xs text-muted-foreground">{t('importServer.portLabel')}</span>
                  <p>{result.propsFound && result.serverPort > 0 ? result.serverPort : t('importServer.portUnknown')}</p>
                </div>
                <div>
                  <span className="text-xs text-muted-foreground">{t('importServer.eulaLabel')}</span>
                  <p className={result.eulaAccepted ? '' : 'text-yellow-600'}>
                    {result.eulaAccepted ? t('importServer.eulaAccepted') : t('importServer.eulaMissing')}
                  </p>
                </div>
              </div>
            </>
          )}

          {step === 'mode' && (
            <div className="space-y-2">
              {([
                { value: 'in_place' as const, icon: MapPin, title: t('importServer.modeInPlace'), desc: t('importServer.modeInPlaceDesc') },
                { value: 'migrate' as const, icon: Truck, title: t('importServer.modeMigrate'), desc: t('importServer.modeMigrateDesc') },
              ]).map((opt) => (
                <label
                  key={opt.value}
                  className={`flex cursor-pointer items-start gap-3 rounded-lg border p-3 transition-colors ${
                    mode === opt.value ? 'border-primary bg-primary/5' : 'hover:bg-accent/50'
                  }`}
                >
                  <input
                    type="radio"
                    name="import-mode"
                    className="mt-1"
                    checked={mode === opt.value}
                    onChange={() => setMode(opt.value)}
                  />
                  <span className="min-w-0 flex-1">
                    <span className="flex items-center gap-1.5 text-sm font-medium">
                      <opt.icon className="size-3.5" /> {opt.title}
                    </span>
                    <span className="mt-0.5 block text-xs text-muted-foreground">{opt.desc}</span>
                  </span>
                </label>
              ))}
            </div>
          )}

          {step === 'config' && (
            <>
              <div>
                <FieldLabel required>{t('importServer.nameLabel')}</FieldLabel>
                <input
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  className="mt-1 w-full rounded-md border bg-background px-3 py-2 text-sm"
                  placeholder="old-server"
                  aria-label={t('importServer.nameLabel')}
                />
              </div>
              <div className="grid grid-cols-2 gap-3">
                <div>
                  <FieldLabel>{t('importServer.memoryLabel')}</FieldLabel>
                  <input
                    value={memoryMb}
                    onChange={(e) => setMemoryMb(e.target.value)}
                    inputMode="numeric"
                    className="mt-1 w-full rounded-md border bg-background px-3 py-2 text-sm"
                    placeholder="2048"
                    aria-label={t('importServer.memoryLabel')}
                  />
                </div>
                <div>
                  <FieldLabel>{t('importServer.jdkBindLabel')}</FieldLabel>
                  <div className="mt-1">
                    <Combobox
                      options={jdkOptions}
                      value={jdkId}
                      onChange={setJdkId}
                      allowCustom={false}
                      placeholder={t('importServer.noJdkBind')}
                    />
                  </div>
                </div>
              </div>
              <p className="rounded bg-muted/40 px-2 py-1.5 text-xs text-muted-foreground">
                {mode === 'in_place' ? t('importServer.modeInPlaceDesc') : t('importServer.modeMigrateDesc')}
              </p>
            </>
          )}
        </ScrollableDialogBody>

        <DialogFooter className="flex-row justify-end gap-2 pt-2">
          <Button type="button" variant="outline" onClick={close}>
            {t('common.cancel')}
          </Button>
          {step !== 'dir' && (
            <Button
              type="button"
              variant="outline"
              onClick={() => setStep(step === 'config' ? 'mode' : step === 'mode' ? 'inspect' : 'dir')}
            >
              {t('importServer.back')}
            </Button>
          )}
          {(step === 'inspect' || step === 'mode') && (
            <Button
              type="button"
              disabled={step === 'inspect' && !jarPath}
              onClick={() => setStep(step === 'inspect' ? 'mode' : 'config')}
            >
              {t('importServer.next')}
            </Button>
          )}
          {step === 'config' && (
            <Button
              type="button"
              disabled={importServer.isPending || !name.trim() || !jarPath}
              onClick={submit}
            >
              {importServer.isPending ? t('importServer.importing') : t('importServer.submit')}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

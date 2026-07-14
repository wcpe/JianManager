import { useState, useEffect, useRef, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Copy } from 'lucide-react'
import { toast } from 'sonner'
import { useNodes } from '@/api/nodes'
import { useGroups } from '@/api/groups'
import { useNodeJDKs } from '@/api/jdks'
import { useCoreVersions, useResolvedCore } from '@/api/provision'
import { useProvisionProxy } from '@/api/proxy'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { Button } from '@jianmanager/ui/components/button'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
import { Combobox, type ComboboxOption } from '@jianmanager/ui/components/combobox'
import { FieldLabel, FieldError } from '@jianmanager/ui/components/field-label'
import { copyToClipboard } from '@/lib/clipboard'
import { validateRequired, validatePositiveInt, validateFields, hasErrors } from '@/lib/form-validation'
import { useFieldGate } from '@/lib/use-field-gate'

interface ProvisionProxyDialogProps {
  open: boolean
  onClose: () => void
}

/**
 * 搭建代理向导（FR-035）：选代理类型/版本/资源，系统分配监听端口与工作目录，
 * 后端下载核心、生成转发配置；Velocity 生成 forwarding secret 并返回一次供留存。
 * 注册后端在创建后经「管理后端」完成。
 */
export default function ProvisionProxyDialog({ open, onClose }: ProvisionProxyDialogProps) {
  const { t } = useTranslation()
  const { data: nodes } = useNodes()
  const { data: groups } = useGroups()

  const [name, setName] = useState('')
  const [nodeId, setNodeId] = useState('')
  const [proxyType, setProxyType] = useState('velocity')
  const [version, setVersion] = useState('')
  const [jdkId, setJdkId] = useState('')
  const [memoryMb, setMemoryMb] = useState('1024')
  const [jvmArgs, setJvmArgs] = useState('')
  const [groupId, setGroupId] = useState('')
  const [onlineMode, setOnlineMode] = useState(true) // 默认正版网络
  const [forwardingSecret, setForwardingSecret] = useState('')
  const gate = useFieldGate()

  const { data: jdks } = useNodeJDKs(nodeId ? Number(nodeId) : 0)
  // bungeecord 无版本选择（仅 latest）；velocity/waterfall 走 PaperMC 版本列表。
  const needsVersion = proxyType !== 'bungeecord'
  const { data: versions, isLoading: versionsLoading } = useCoreVersions(open && needsVersion ? proxyType : '')
  const effectiveVersion = needsVersion ? version : 'latest'
  const { data: resolved } = useResolvedCore(open ? proxyType : '', effectiveVersion, 0)

  const provision = useProvisionProxy()

  // 系统可获取项 → 下拉选项（FR-072）。代理类型/版本允许自定义。
  const nodeOptions: ComboboxOption[] = (nodes ?? [])
    .filter((n) => n.status === 1)
    .map((n) => ({ value: String(n.id), label: n.name }))
  const proxyTypeOptions: ComboboxOption[] = [
    { value: 'velocity', label: 'Velocity (modern)' },
    { value: 'waterfall', label: 'Waterfall' },
    { value: 'bungeecord', label: 'BungeeCord' },
  ]
  const versionOptions: ComboboxOption[] = (versions ?? []).map((v) => ({ value: v }))
  const jdkOptions: ComboboxOption[] = (jdks ?? []).map((j) => ({
    value: String(j.id),
    label: `${j.vendor} ${j.majorVersion} (${j.version})`,
  }))
  const groupOptions: ComboboxOption[] = (groups ?? []).map((g) => ({ value: String(g.id), label: g.name }))

  const errors = validateFields(
    { name, nodeId, version, memoryMb },
    {
      name: [validateRequired],
      nodeId: [validateRequired],
      // 仅当该代理类型需要版本时才把版本设为必填
      version: needsVersion ? [validateRequired] : [],
      memoryMb: [validatePositiveInt],
    },
  )

  const jdkDefaultNodeRef = useRef('')
  useEffect(() => {
    if (nodeId && jdks && jdks.length > 0 && jdkDefaultNodeRef.current !== nodeId) {
      jdkDefaultNodeRef.current = nodeId
      const best = [...jdks].sort((a, b) => b.majorVersion - a.majorVersion)[0]
      setJdkId(String(best.id))
    }
  }, [nodeId, jdks])

  const reset = () => {
    setName(''); setNodeId(''); setProxyType('velocity'); setVersion('')
    setJdkId(''); setMemoryMb('1024'); setJvmArgs(''); setGroupId(''); setOnlineMode(true); setForwardingSecret('')
    jdkDefaultNodeRef.current = ''
    gate.reset()
  }
  const close = () => { onClose(); reset() }
  const copySecret = async () => {
    const ok = await copyToClipboard(forwardingSecret)
    if (ok) toast.success(t('proxy.secretCopied'))
    else toast.error(t('proxy.secretCopyFailed'))
  }

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault()
    gate.submit()
    if (hasErrors(errors)) return
    const args = jvmArgs.trim() ? jvmArgs.trim().split(/\s+/).filter(Boolean) : undefined
    provision.mutate(
      {
        nodeId: Number(nodeId),
        name,
        proxyType,
        version: needsVersion ? version : undefined,
        jdkId: jdkId ? Number(jdkId) : undefined,
        memoryMb: memoryMb ? Number(memoryMb) : undefined,
        jvmArgs: args,
        groupId: groupId ? Number(groupId) : undefined,
        onlineMode,
      },
      {
        onSuccess: (res) => {
          toast.success(t('proxy.success', { name }))
          if (res.forwardingSecret) {
            setForwardingSecret(res.forwardingSecret)
          } else {
            close()
          }
          ;(res.warnings || []).forEach((w) => toast.warning(w))
        },
        onError: (err: Error & { response?: { data?: { message?: string } } }) => {
          toast.error(err.response?.data?.message || t('proxy.failed'))
        },
      },
    )
  }

  return (
    <Dialog open={open} onOpenChange={(next) => { if (!next) close() }}>
      <DialogContent className="max-h-[90vh] max-w-md overflow-y-auto">
        {forwardingSecret ? (
          <>
            <DialogHeader>
              <DialogTitle>{t('proxy.secretTitle')}</DialogTitle>
              <DialogDescription>{t('proxy.secretDesc')}</DialogDescription>
            </DialogHeader>
            <div className="rounded-md border bg-muted/40 p-3 font-mono text-sm break-all">
              {forwardingSecret}
            </div>
            <DialogFooter>
              <Button type="button" variant="outline" onClick={copySecret}>
                <Copy className="size-4" /> {t('proxy.copySecret')}
              </Button>
              <Button type="button" onClick={close}>{t('common.done')}</Button>
            </DialogFooter>
          </>
        ) : (
          <>
            <DialogHeader>
              <DialogTitle>{t('proxy.title')}</DialogTitle>
              <DialogDescription>{t('provision.systemAssigned')}</DialogDescription>
            </DialogHeader>

        <form onSubmit={handleSubmit} className="space-y-3">
          <div>
            <FieldLabel required>{t('instances.instanceName')}</FieldLabel>
            <input value={name} onChange={(e) => setName(e.target.value)}
              onBlur={() => gate.touch('name')}
              aria-invalid={!!gate.show('name', errors.name)}
              className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm aria-invalid:border-destructive" placeholder="velocity-main" />
            <FieldError error={gate.show('name', errors.name)} />
          </div>

          <div>
            <FieldLabel required>{t('instances.node')}</FieldLabel>
            <div className="mt-1">
              <Combobox
                options={nodeOptions}
                value={nodeId}
                onChange={(v) => { gate.touch('nodeId'); setNodeId(v) }}
                allowCustom={false}
                placeholder={t('instances.selectNode')}
                invalid={!!gate.show('nodeId', errors.nodeId)}
              />
            </div>
            <FieldError error={gate.show('nodeId', errors.nodeId)} />
          </div>

          <div className="grid grid-cols-2 gap-3">
            <div>
              <FieldLabel>{t('proxy.type')}</FieldLabel>
              <div className="mt-1">
                <Combobox
                  options={proxyTypeOptions}
                  value={proxyType}
                  onChange={(v) => { setProxyType(v); setVersion('') }}
                />
              </div>
            </div>
            <div>
              <FieldLabel required={needsVersion}>{t('proxy.version')}</FieldLabel>
              <div className="mt-1">
                <Combobox
                  options={versionOptions}
                  value={needsVersion ? version : ''}
                  onChange={(v) => { gate.touch('version'); setVersion(v) }}
                  disabled={!needsVersion || versionsLoading}
                  invalid={!!gate.show('version', errors.version)}
                  placeholder={needsVersion ? (versionsLoading ? t('provision.loadingVersions') : t('provision.selectVersion')) : t('proxy.latestOnly')}
                />
              </div>
              <FieldError error={gate.show('version', errors.version)} />
            </div>
          </div>
          {resolved && (
            <p className="text-xs text-muted-foreground">{t('provision.willDownload')}: {resolved.filename}</p>
          )}

          <div className="grid grid-cols-2 gap-3">
            <div>
              <FieldLabel>{t('provision.memory')}</FieldLabel>
              <input value={memoryMb} onChange={(e) => setMemoryMb(e.target.value)} inputMode="numeric"
                onBlur={() => gate.touch('memoryMb')}
                aria-invalid={!!gate.show('memoryMb', errors.memoryMb)}
                className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm aria-invalid:border-destructive" placeholder="1024" />
              <FieldError error={gate.show('memoryMb', errors.memoryMb)} />
            </div>
            <div>
              <FieldLabel>JDK</FieldLabel>
              <div className="mt-1">
                <Combobox
                  options={jdkOptions}
                  value={jdkId}
                  onChange={setJdkId}
                  allowCustom={false}
                  placeholder={t('provision.noJdk')}
                />
              </div>
            </div>
          </div>

          <div>
            <FieldLabel>{t('provision.jvmArgs')}</FieldLabel>
            <input value={jvmArgs} onChange={(e) => setJvmArgs(e.target.value)}
              className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm font-mono" placeholder="-XX:+UseG1GC" />
          </div>

          <div>
            <FieldLabel>{t('instances.group')}</FieldLabel>
            <div className="mt-1">
              <Combobox
                options={groupOptions}
                value={groupId}
                onChange={setGroupId}
                allowCustom={false}
                placeholder={t('instances.noGroup')}
              />
            </div>
          </div>

          <div>
            <label className="flex items-center gap-2 text-sm">
              <Checkbox checked={onlineMode} onCheckedChange={(v) => setOnlineMode(v === true)} aria-label={t('proxy.onlineMode')} />
              {t('proxy.onlineMode')}
            </label>
            <p className="mt-1 text-xs text-muted-foreground">{t('proxy.onlineModeHint')}</p>
          </div>

          <div className="flex justify-end gap-2 pt-2">
            <Button type="button" variant="outline" onClick={close}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" disabled={provision.isPending || hasErrors(errors)}>
              {provision.isPending ? t('proxy.provisioning') : t('proxy.submit')}
            </Button>
          </div>
        </form>
          </>
        )}
      </DialogContent>
    </Dialog>
  )
}

import { useState, useEffect, useRef, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useNodes } from '@/api/nodes'
import { useGroups } from '@/api/groups'
import { useNodeJDKs } from '@/api/jdks'
import { useCoreVersions, useResolvedCore } from '@/api/provision'
import { useProvisionProxy } from '@/api/proxy'
import { MODAL_OVERLAY, MODAL_PANEL } from '@/components/ui/scrollable-dialog'
import { Combobox, type ComboboxOption } from '@/components/ui/combobox'
import { FieldLabel, FieldError } from '@/components/ui/field-label'
import { validateRequired, validatePositiveInt, validateFields, hasErrors } from '@/lib/form-validation'

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
    setJdkId(''); setMemoryMb('1024'); setJvmArgs(''); setGroupId('')
    jdkDefaultNodeRef.current = ''
  }
  const close = () => { onClose(); reset() }

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault()
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
            toast.info(t('proxy.secretSaved', { secret: res.forwardingSecret }), { duration: 15000 })
          }
          ;(res.warnings || []).forEach((w) => toast.warning(w))
          close()
        },
        onError: (err: Error & { response?: { data?: { message?: string } } }) => {
          toast.error(err.response?.data?.message || t('proxy.failed'))
        },
      },
    )
  }

  if (!open) return null

  return (
    <div className={MODAL_OVERLAY}>
      <div className={`${MODAL_PANEL} max-w-md`}>
        <h2 className="text-lg font-bold mb-1">{t('proxy.title')}</h2>
        <p className="text-xs text-muted-foreground mb-4">{t('provision.systemAssigned')}</p>

        <form onSubmit={handleSubmit} className="space-y-3">
          <div>
            <FieldLabel required>{t('instances.instanceName')}</FieldLabel>
            <input value={name} onChange={(e) => setName(e.target.value)}
              aria-invalid={!!errors.name}
              className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm aria-invalid:border-destructive" placeholder="velocity-main" />
            <FieldError error={errors.name} />
          </div>

          <div>
            <FieldLabel required>{t('instances.node')}</FieldLabel>
            <div className="mt-1">
              <Combobox
                options={nodeOptions}
                value={nodeId}
                onChange={setNodeId}
                allowCustom={false}
                placeholder={t('instances.selectNode')}
                invalid={!!errors.nodeId}
              />
            </div>
            <FieldError error={errors.nodeId} />
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
                  onChange={setVersion}
                  disabled={!needsVersion || versionsLoading}
                  invalid={!!errors.version}
                  placeholder={needsVersion ? (versionsLoading ? t('provision.loadingVersions') : t('provision.selectVersion')) : t('proxy.latestOnly')}
                />
              </div>
              <FieldError error={errors.version} />
            </div>
          </div>
          {resolved && (
            <p className="text-xs text-muted-foreground">{t('provision.willDownload')}: {resolved.filename}</p>
          )}

          <div className="grid grid-cols-2 gap-3">
            <div>
              <FieldLabel>{t('provision.memory')}</FieldLabel>
              <input value={memoryMb} onChange={(e) => setMemoryMb(e.target.value)} inputMode="numeric"
                aria-invalid={!!errors.memoryMb}
                className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm aria-invalid:border-destructive" placeholder="1024" />
              <FieldError error={errors.memoryMb} />
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
              <input type="checkbox" checked={onlineMode} onChange={(e) => setOnlineMode(e.target.checked)} />
              {t('proxy.onlineMode')}
            </label>
            <p className="mt-1 text-xs text-muted-foreground">{t('proxy.onlineModeHint')}</p>
          </div>

          <div className="flex justify-end gap-2 pt-2">
            <button type="button" onClick={close} className="px-4 py-2 text-sm border rounded-md hover:bg-accent">
              {t('common.cancel')}
            </button>
            <button type="submit" disabled={provision.isPending || hasErrors(errors)}
              className="px-4 py-2 text-sm bg-primary text-primary-foreground rounded-md disabled:opacity-50">
              {provision.isPending ? t('proxy.provisioning') : t('proxy.submit')}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}

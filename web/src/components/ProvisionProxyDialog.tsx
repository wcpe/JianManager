import { useState, useEffect, useRef, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useNodes } from '@/api/nodes'
import { useGroups } from '@/api/groups'
import { useNodeJDKs } from '@/api/jdks'
import { useCoreVersions, useResolvedCore } from '@/api/provision'
import { useProvisionProxy } from '@/api/proxy'

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

  const { data: jdks } = useNodeJDKs(nodeId ? Number(nodeId) : 0)
  // bungeecord 无版本选择（仅 latest）；velocity/waterfall 走 PaperMC 版本列表。
  const needsVersion = proxyType !== 'bungeecord'
  const { data: versions, isLoading: versionsLoading } = useCoreVersions(open && needsVersion ? proxyType : '')
  const effectiveVersion = needsVersion ? version : 'latest'
  const { data: resolved } = useResolvedCore(open ? proxyType : '', effectiveVersion, 0)

  const provision = useProvisionProxy()

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
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
      <div className="bg-background border rounded-lg p-6 w-full max-w-md shadow-lg max-h-[88vh] overflow-y-auto">
        <h2 className="text-lg font-bold mb-1">{t('proxy.title')}</h2>
        <p className="text-xs text-muted-foreground mb-4">{t('provision.systemAssigned')}</p>

        <form onSubmit={handleSubmit} className="space-y-3">
          <div>
            <label className="text-sm font-medium">{t('instances.instanceName')}</label>
            <input value={name} onChange={(e) => setName(e.target.value)} required
              className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm" placeholder="velocity-main" />
          </div>

          <div>
            <label className="text-sm font-medium">{t('instances.node')}</label>
            <select value={nodeId} onChange={(e) => setNodeId(e.target.value)} required
              className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm">
              <option value="">{t('instances.selectNode')}</option>
              {nodes?.filter((n) => n.status === 1).map((n) => (
                <option key={n.id} value={n.id}>{n.name}</option>
              ))}
            </select>
          </div>

          <div className="grid grid-cols-2 gap-3">
            <div>
              <label className="text-sm font-medium">{t('proxy.type')}</label>
              <select value={proxyType} onChange={(e) => { setProxyType(e.target.value); setVersion('') }}
                className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm">
                <option value="velocity">Velocity (modern)</option>
                <option value="waterfall">Waterfall</option>
                <option value="bungeecord">BungeeCord</option>
              </select>
            </div>
            <div>
              <label className="text-sm font-medium">{t('proxy.version')}</label>
              <select value={version} onChange={(e) => setVersion(e.target.value)}
                disabled={!needsVersion || versionsLoading} required={needsVersion}
                className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm disabled:opacity-60">
                <option value="">{needsVersion ? (versionsLoading ? t('provision.loadingVersions') : t('provision.selectVersion')) : t('proxy.latestOnly')}</option>
                {versions?.map((v) => (<option key={v} value={v}>{v}</option>))}
              </select>
            </div>
          </div>
          {resolved && (
            <p className="text-xs text-muted-foreground">{t('provision.willDownload')}: {resolved.filename}</p>
          )}

          <div className="grid grid-cols-2 gap-3">
            <div>
              <label className="text-sm font-medium">{t('provision.memory')}</label>
              <input value={memoryMb} onChange={(e) => setMemoryMb(e.target.value)} inputMode="numeric"
                className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm" placeholder="1024" />
            </div>
            <div>
              <label className="text-sm font-medium">JDK</label>
              <select value={jdkId} onChange={(e) => setJdkId(e.target.value)}
                className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm">
                <option value="">{t('provision.noJdk')}</option>
                {jdks?.map((j) => (<option key={j.id} value={j.id}>{j.vendor} {j.majorVersion} ({j.version})</option>))}
              </select>
            </div>
          </div>

          <div>
            <label className="text-sm font-medium">{t('provision.jvmArgs')}</label>
            <input value={jvmArgs} onChange={(e) => setJvmArgs(e.target.value)}
              className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm font-mono" placeholder="-XX:+UseG1GC" />
          </div>

          <div>
            <label className="text-sm font-medium">{t('instances.group')}</label>
            <select value={groupId} onChange={(e) => setGroupId(e.target.value)}
              className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm">
              <option value="">{t('instances.noGroup')}</option>
              {groups?.map((g) => (<option key={g.id} value={g.id}>{g.name}</option>))}
            </select>
          </div>

          <div className="flex justify-end gap-2 pt-2">
            <button type="button" onClick={close} className="px-4 py-2 text-sm border rounded-md hover:bg-accent">
              {t('common.cancel')}
            </button>
            <button type="submit" disabled={provision.isPending || !nodeId || !name || (needsVersion && !version)}
              className="px-4 py-2 text-sm bg-primary text-primary-foreground rounded-md disabled:opacity-50">
              {provision.isPending ? t('proxy.provisioning') : t('proxy.submit')}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}

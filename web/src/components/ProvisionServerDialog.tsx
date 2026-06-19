import { useState, useEffect, useRef, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useNodes } from '@/api/nodes'
import { useGroups } from '@/api/groups'
import { useNodeJDKs } from '@/api/jdks'
import { useCoreVersions, useResolvedCore, useProvisionBukkit } from '@/api/provision'

interface ProvisionServerDialogProps {
  open: boolean
  onClose: () => void
}

/**
 * 一键搭建 Paper 子服向导：用户只需选版本/资源，端口与工作目录由系统分配，
 * 核心由后端从 PaperMC 下载并写入基础配置（FR-034）。
 */
export default function ProvisionServerDialog({ open, onClose }: ProvisionServerDialogProps) {
  const { t } = useTranslation()
  const { data: nodes } = useNodes()
  const { data: groups } = useGroups()

  const [name, setName] = useState('')
  const [nodeId, setNodeId] = useState('')
  const coreType = 'paper' // 目前仅支持 paper，后续可扩展 purpur/spigot
  const [mcVersion, setMcVersion] = useState('')
  const [build, setBuild] = useState('')
  const [jdkId, setJdkId] = useState('')
  const [memoryMb, setMemoryMb] = useState('2048')
  const [jvmArgs, setJvmArgs] = useState('')
  const [groupId, setGroupId] = useState('')

  const { data: jdks } = useNodeJDKs(nodeId ? Number(nodeId) : 0)
  const { data: versions, isLoading: versionsLoading, isError: versionsError } = useCoreVersions(
    open ? coreType : '',
  )
  const buildNum = build.trim() && Number.isFinite(Number(build)) ? Number(build) : 0
  const { data: resolved, isFetching: resolving } = useResolvedCore(
    open ? coreType : '',
    mcVersion,
    buildNum,
  )

  const provision = useProvisionBukkit()

  // 默认选最高版本的已装 JDK：漏选会用系统 Java，现代 Paper 起不来。
  // 仅在 jdks 变化（选/换节点）时设，用户随后仍可改回「不指定」。
  useEffect(() => {
    if (jdks && jdks.length > 0) {
      const best = [...jdks].sort((a, b) => b.majorVersion - a.majorVersion)[0]
      setJdkId(String(best.id))
    }
  }, [jdks])

  // 选节点后默认绑定该节点最高版本的已装 JDK：现代 Paper 需 Java 17/21，
  // 默认「不指定」会用系统 Java（常为 8）导致一键搭建出的服跑不起来。每节点只默认一次，用户仍可改。
  const jdkDefaultNodeRef = useRef('')
  useEffect(() => {
    if (nodeId && jdks && jdks.length > 0 && jdkDefaultNodeRef.current !== nodeId) {
      jdkDefaultNodeRef.current = nodeId
      const best = [...jdks].sort((a, b) => b.majorVersion - a.majorVersion)[0]
      setJdkId(String(best.id))
    }
  }, [nodeId, jdks])

  const resetForm = () => {
    setName('')
    setNodeId('')
    setMcVersion('')
    setBuild('')
    setJdkId('')
    setMemoryMb('2048')
    setJvmArgs('')
    setGroupId('')
    jdkDefaultNodeRef.current = ''
  }

  const close = () => {
    onClose()
    resetForm()
  }

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault()
    const args = jvmArgs.trim() ? jvmArgs.trim().split(/\s+/).filter(Boolean) : undefined
    provision.mutate(
      {
        nodeId: Number(nodeId),
        name,
        coreType,
        mcVersion,
        build: buildNum > 0 ? buildNum : undefined,
        jdkId: jdkId ? Number(jdkId) : undefined,
        memoryMb: memoryMb ? Number(memoryMb) : undefined,
        jvmArgs: args,
        groupId: groupId ? Number(groupId) : undefined,
      },
      {
        onSuccess: () => {
          toast.success(t('provision.success', { name }))
          close()
        },
        onError: (err: Error & { response?: { data?: { message?: string; instance?: unknown } } }) => {
          const data = err.response?.data
          // 部分失败：实例已建但核心下载/配置写入未完成，仍关闭并提示用户去重试。
          if (data?.instance) {
            toast.warning(t('provision.partialFailure'))
            close()
            return
          }
          toast.error(data?.message || t('provision.failed'))
        },
      },
    )
  }

  if (!open) return null

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
      <div className="bg-background border rounded-lg p-6 w-full max-w-md shadow-lg max-h-[88vh] overflow-y-auto">
        <h2 className="text-lg font-bold mb-1">{t('provision.title')}</h2>
        <p className="text-xs text-muted-foreground mb-4">{t('provision.systemAssigned')}</p>

        <form onSubmit={handleSubmit} className="space-y-3">
          <div>
            <label className="text-sm font-medium">{t('instances.instanceName')}</label>
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm"
              placeholder="lobby"
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
              {nodes?.filter((n) => n.status === 1).map((n) => (
                <option key={n.id} value={n.id}>{n.name}</option>
              ))}
            </select>
          </div>

          <div className="grid grid-cols-2 gap-3">
            <div>
              <label className="text-sm font-medium">{t('provision.coreType')}</label>
              <select
                value={coreType}
                disabled
                className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm disabled:opacity-70"
              >
                <option value="paper">Paper</option>
              </select>
            </div>
            <div>
              <label className="text-sm font-medium">{t('provision.mcVersion')}</label>
              <select
                value={mcVersion}
                onChange={(e) => setMcVersion(e.target.value)}
                className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm"
                required
                disabled={versionsLoading || versionsError}
              >
                <option value="">
                  {versionsLoading
                    ? t('provision.loadingVersions')
                    : versionsError
                      ? t('provision.versionsError')
                      : t('provision.selectVersion')}
                </option>
                {versions?.map((v) => (
                  <option key={v} value={v}>{v}</option>
                ))}
              </select>
            </div>
          </div>

          <div>
            <label className="text-sm font-medium">{t('provision.build')}</label>
            <input
              value={build}
              onChange={(e) => setBuild(e.target.value)}
              inputMode="numeric"
              className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm"
              placeholder={t('provision.latestBuild')}
            />
            {mcVersion && (
              <p className="mt-1 text-xs text-muted-foreground">
                {resolving
                  ? t('common.loading')
                  : resolved
                    ? `${t('provision.willDownload')}: ${resolved.filename} (build #${resolved.build})`
                    : t('provision.versionsError')}
              </p>
            )}
          </div>

          <div className="grid grid-cols-2 gap-3">
            <div>
              <label className="text-sm font-medium">{t('provision.memory')}</label>
              <input
                value={memoryMb}
                onChange={(e) => setMemoryMb(e.target.value)}
                inputMode="numeric"
                className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm"
                placeholder="2048"
              />
            </div>
            <div>
              <label className="text-sm font-medium">JDK</label>
              <select
                value={jdkId}
                onChange={(e) => setJdkId(e.target.value)}
                className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm"
              >
                <option value="">{t('provision.noJdk')}</option>
                {jdks?.map((j) => (
                  <option key={j.id} value={j.id}>
                    {j.vendor} {j.majorVersion} ({j.version})
                  </option>
                ))}
              </select>
            </div>
          </div>

          <div>
            <label className="text-sm font-medium">{t('provision.jvmArgs')}</label>
            <input
              value={jvmArgs}
              onChange={(e) => setJvmArgs(e.target.value)}
              className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm font-mono"
              placeholder="-XX:+UseG1GC"
            />
            <p className="mt-1 text-xs text-muted-foreground">{t('provision.jvmArgsHint')}</p>
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

          <div className="flex justify-end gap-2 pt-2">
            <button
              type="button"
              onClick={close}
              className="px-4 py-2 text-sm border rounded-md hover:bg-accent"
            >
              {t('common.cancel')}
            </button>
            <button
              type="submit"
              disabled={provision.isPending || !nodeId || !mcVersion || !name}
              className="px-4 py-2 text-sm bg-primary text-primary-foreground rounded-md disabled:opacity-50"
            >
              {provision.isPending ? t('provision.provisioning') : t('provision.submit')}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}

import { useState, useEffect, useRef, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useNodes } from '@/api/nodes'
import { useGroups } from '@/api/groups'
import { useNodeJDKs } from '@/api/jdks'
import { useCoreVersions, useResolvedCore, useProvisionBukkit } from '@/api/provision'
import { MODAL_OVERLAY, MODAL_PANEL } from '@/components/ui/scrollable-dialog'
import { Combobox, type ComboboxOption } from '@/components/ui/combobox'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Checkbox } from '@/components/ui/checkbox'
import { FieldLabel, FieldError } from '@/components/ui/field-label'
import { validateRequired, validatePositiveInt, validateFields, hasErrors } from '@/lib/form-validation'

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
  const [onlineMode, setOnlineMode] = useState(false) // 默认代理就绪（离线）

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

  // 系统可获取项 → 下拉选项（FR-072）。版本允许自定义（PaperMC 列表外的版本）。
  const nodeOptions: ComboboxOption[] = (nodes ?? [])
    .filter((n) => n.status === 1)
    .map((n) => ({ value: String(n.id), label: n.name }))
  const versionOptions: ComboboxOption[] = (versions ?? []).map((v) => ({ value: v }))
  const jdkOptions: ComboboxOption[] = (jdks ?? []).map((j) => ({
    value: String(j.id),
    label: `${j.vendor} ${j.majorVersion} (${j.version})`,
  }))
  const groupOptions: ComboboxOption[] = (groups ?? []).map((g) => ({ value: String(g.id), label: g.name }))

  const errors = validateFields(
    { name, nodeId, mcVersion, memoryMb },
    {
      name: [validateRequired],
      nodeId: [validateRequired],
      mcVersion: [validateRequired],
      memoryMb: [validatePositiveInt],
    },
  )

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
    setOnlineMode(false)
    jdkDefaultNodeRef.current = ''
  }

  const close = () => {
    onClose()
    resetForm()
  }

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault()
    if (hasErrors(errors)) return
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
        onlineMode,
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
    <div className={MODAL_OVERLAY}>
      <div className={`${MODAL_PANEL} max-w-md`}>
        <h2 className="text-lg font-bold mb-1">{t('provision.title')}</h2>
        <p className="text-xs text-muted-foreground mb-4">{t('provision.systemAssigned')}</p>

        <form onSubmit={handleSubmit} className="space-y-3">
          <div>
            <FieldLabel required>{t('instances.instanceName')}</FieldLabel>
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm aria-invalid:border-destructive"
              placeholder="lobby"
              aria-invalid={!!errors.name}
            />
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
              <FieldLabel>{t('provision.coreType')}</FieldLabel>
              <Select value={coreType} disabled>
                <SelectTrigger className="w-full mt-1">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="paper">Paper</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div>
              <FieldLabel required>{t('provision.mcVersion')}</FieldLabel>
              <div className="mt-1">
                <Combobox
                  options={versionOptions}
                  value={mcVersion}
                  onChange={setMcVersion}
                  disabled={versionsLoading || versionsError}
                  invalid={!!errors.mcVersion}
                  placeholder={
                    versionsLoading
                      ? t('provision.loadingVersions')
                      : versionsError
                        ? t('provision.versionsError')
                        : t('provision.selectVersion')
                  }
                />
              </div>
              <FieldError error={errors.mcVersion} />
            </div>
          </div>

          <div>
            <FieldLabel>{t('provision.build')}</FieldLabel>
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
              <FieldLabel>{t('provision.memory')}</FieldLabel>
              <input
                value={memoryMb}
                onChange={(e) => setMemoryMb(e.target.value)}
                inputMode="numeric"
                className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm aria-invalid:border-destructive"
                placeholder="2048"
                aria-invalid={!!errors.memoryMb}
              />
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
            <input
              value={jvmArgs}
              onChange={(e) => setJvmArgs(e.target.value)}
              className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm font-mono"
              placeholder="-XX:+UseG1GC"
            />
            <p className="mt-1 text-xs text-muted-foreground">{t('provision.jvmArgsHint')}</p>
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
              <Checkbox
                checked={onlineMode}
                onCheckedChange={(v) => setOnlineMode(v === true)}
                aria-label={t('provision.onlineMode')}
              />
              {t('provision.onlineMode')}
            </label>
            <p className="mt-1 text-xs text-muted-foreground">{t('provision.onlineModeHint')}</p>
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
              disabled={provision.isPending || hasErrors(errors)}
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

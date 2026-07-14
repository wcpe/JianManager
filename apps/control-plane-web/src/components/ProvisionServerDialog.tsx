import { useState, useEffect, useRef, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router'
import { toast } from 'sonner'
import { useNodes } from '@/api/nodes'
import { useGroups } from '@/api/groups'
import { useNodeJDKs } from '@/api/jdks'
import { useCoreVersions, useResolvedCore, useProvisionServer } from '@/api/provision'
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
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@jianmanager/ui/components/select'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
import { FieldLabel, FieldError } from '@jianmanager/ui/components/field-label'
import { validateRequired, validatePositiveInt, validateFields, hasErrors } from '@/lib/form-validation'
import { useFieldGate } from '@/lib/use-field-gate'

interface ProvisionServerDialogProps {
  open: boolean
  onClose: () => void
}

/**
 * 一键搭建后端子服向导：用户只需选核心/版本/资源，端口与工作目录由系统分配，
 * 核心由后端解析并写入基础配置（FR-034/FR-046）。
 */
export default function ProvisionServerDialog({ open, onClose }: ProvisionServerDialogProps) {
  const { t } = useTranslation()
  const { data: nodes } = useNodes()
  const { data: groups } = useGroups()

  const [name, setName] = useState('')
  const [nodeId, setNodeId] = useState('')
  const [coreType, setCoreType] = useState('paper')
  const [mcVersion, setMcVersion] = useState('')
  const [build, setBuild] = useState('')
  const [jdkId, setJdkId] = useState('')
  const [memoryMb, setMemoryMb] = useState('2048')
  const [jvmArgs, setJvmArgs] = useState('')
  const [groupId, setGroupId] = useState('')
  const [onlineMode, setOnlineMode] = useState(false) // 默认代理就绪（离线）
  const gate = useFieldGate()

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

  const provision = useProvisionServer()

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

  // FR-316 版本-JDK 兼容预检：解析响应携带该 MC 版本所需最低 Java 大版本（CP 单一真值），
  // 对所选/默认 JDK 表单级阻断，与 FR-314 启动预检互补（搭建时拦 vs 启动时拦）。
  // 阻断：节点无任何 JDK；或所选 JDK 大版本低于需求（真机事故：MC 26.1 + 最高 Temurin 21 必崩）。
  // 仅警示不阻断：节点有 JDK 但选了「不指定」——系统 Java 版本未知，宁漏勿误伤，交 FR-314 兜底。
  const requiredJava = resolved?.javaMajorRequired ?? 0
  const selectedJdk = (jdks ?? []).find((j) => String(j.id) === jdkId)
  let jdkBlockText: string | null = null
  let jdkWarnText: string | null = null
  if (nodeId && mcVersion && requiredJava > 0 && jdks !== undefined) {
    if (jdks.length === 0) {
      jdkBlockText = t('provision.javaReqNoJdk', { version: mcVersion, java: requiredJava })
    } else if (!selectedJdk) {
      jdkWarnText = t('provision.javaReqUnverified', { version: mcVersion, java: requiredJava })
    } else if (selectedJdk.majorVersion < requiredJava) {
      jdkBlockText = t('provision.javaReqTooLow', {
        version: mcVersion,
        java: requiredJava,
        current: selectedJdk.majorVersion,
        gap: requiredJava - selectedJdk.majorVersion,
      })
    }
  }

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
    setCoreType('paper')
    setMcVersion('')
    setBuild('')
    setJdkId('')
    setMemoryMb('2048')
    setJvmArgs('')
    setGroupId('')
    setOnlineMode(false)
    jdkDefaultNodeRef.current = ''
    gate.reset()
  }

  const changeCoreType = (next: string) => {
    setCoreType(next)
    setMcVersion('')
    setBuild('')
  }

  const close = () => {
    onClose()
    resetForm()
  }

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault()
    gate.submit()
    if (hasErrors(errors) || jdkBlockText) return
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
          // FR-319 异步化：实例已建，核心下载在后台任务推进（慢源可能数分钟），进度看任务中心。
          toast.success(t('provision.submitted'))
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
    <Dialog open={open} onOpenChange={(next) => { if (!next) close() }}>
      <DialogContent
        showCloseButton={false}
        onPointerDownOutside={(event) => event.preventDefault()}
        className={`${scrollableDialogContentClass} sm:max-w-md`}
      >
        <DialogHeader>
          <DialogTitle>{t('provision.title')}</DialogTitle>
          <DialogDescription className="text-xs">{t('provision.systemAssigned')}</DialogDescription>
        </DialogHeader>

        <form onSubmit={handleSubmit} className="flex min-h-0 flex-1 flex-col">
          <ScrollableDialogBody className="space-y-3 py-2">
          <div>
            <FieldLabel required>{t('instances.instanceName')}</FieldLabel>
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              onBlur={() => gate.touch('name')}
              className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm aria-invalid:border-destructive"
              placeholder="lobby"
              aria-invalid={!!gate.show('name', errors.name)}
            />
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
              <FieldLabel>{t('provision.coreType')}</FieldLabel>
              <Select value={coreType} onValueChange={changeCoreType}>
                <SelectTrigger className="w-full mt-1">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="paper">Paper</SelectItem>
                  <SelectItem value="spongevanilla">{t('provision.coreTypeSpongeVanilla')}</SelectItem>
                  <SelectItem value="spongeforge">{t('provision.coreTypeSpongeForge')}</SelectItem>
                </SelectContent>
              </Select>
              {coreType === 'spongeforge' && (
                <p className="mt-1 text-xs text-muted-foreground">{t('provision.spongeForgeHint')}</p>
              )}
            </div>
            <div>
              <FieldLabel required>{t('provision.mcVersion')}</FieldLabel>
              <div className="mt-1">
                <Combobox
                  options={versionOptions}
                  value={mcVersion}
                  onChange={(v) => { gate.touch('mcVersion'); setMcVersion(v) }}
                  disabled={versionsLoading || versionsError}
                  invalid={!!gate.show('mcVersion', errors.mcVersion)}
                  placeholder={
                    versionsLoading
                      ? t('provision.loadingVersions')
                      : versionsError
                        ? t('provision.versionsError')
                        : t('provision.selectVersion')
                  }
                />
              </div>
              <FieldError error={gate.show('mcVersion', errors.mcVersion)} />
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
                onBlur={() => gate.touch('memoryMb')}
                inputMode="numeric"
                className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm aria-invalid:border-destructive"
                placeholder="2048"
                aria-invalid={!!gate.show('memoryMb', errors.memoryMb)}
              />
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
                  invalid={!!jdkBlockText}
                />
              </div>
            </div>
          </div>

          {jdkBlockText && (
            <div className="rounded-md border border-destructive/40 bg-destructive/10 px-2.5 py-2 text-xs text-destructive">
              <span>{jdkBlockText}</span>{' '}
              <Link to="/runtime-assets" className="font-medium underline underline-offset-2">
                {t('provision.goInstallJdk')}
              </Link>
            </div>
          )}
          {!jdkBlockText && jdkWarnText && (
            <p className="text-xs text-amber-600 dark:text-amber-400">{jdkWarnText}</p>
          )}

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
          </ScrollableDialogBody>

          <DialogFooter className="flex-row justify-end pt-2">
            <button
              type="button"
              onClick={close}
              className="px-4 py-2 text-sm border rounded-md hover:bg-accent"
            >
              {t('common.cancel')}
            </button>
            <button
              type="submit"
              disabled={provision.isPending || hasErrors(errors) || !!jdkBlockText}
              className="px-4 py-2 text-sm bg-primary text-primary-foreground rounded-md disabled:opacity-50"
            >
              {provision.isPending ? t('provision.provisioning') : t('provision.submit')}
            </button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

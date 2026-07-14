import { useState, type FormEvent, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { useQueryClient, useMutation } from '@tanstack/react-query'
import { toast } from 'sonner'
import api from '@/api/client'
import { useNodes } from '@/api/nodes'
import { useGroups } from '@/api/groups'
import { useTemplates } from '@/api/templates'
import { useNodeJDKs } from '@/api/jdks'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { Button } from '@jianmanager/ui/components/button'
import { scrollableDialogContentClass, ScrollableDialogBody } from '@jianmanager/ui/components/scrollable-dialog'
import { Combobox, type ComboboxOption } from '@jianmanager/ui/components/combobox'
import { FieldLabel, FieldError } from '@jianmanager/ui/components/field-label'
import {
  validateRequired,
  validateAbsPath,
  validateResourceLimitNumber,
  validateFields,
  hasErrors,
} from '@/lib/form-validation'
import { useFieldGate } from '@/lib/use-field-gate'

interface CreateInstanceDialogProps {
  open: boolean
  onClose: () => void
}

/** 分区标题块：在双列网格表单中分隔「基本 / 启动 / 高级」字段组（FR-189，遵循 ui-modals）。 */
function FormSection({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="space-y-3">
      <h3 className="text-sm font-semibold text-foreground">{title}</h3>
      {children}
    </section>
  )
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
  // image 仅 docker 模式使用：容器镜像引用（FR-078，ADR-019）。
  const [image, setImage] = useState('')
  // cpuLimit/memLimitMb/diskLimitMb 仅 docker 模式使用：资源限额，空/0/负值=不限制（FR-079，ADR-019）。
  const [cpuLimit, setCpuLimit] = useState('')
  const [memLimitMb, setMemLimitMb] = useState('')
  const [diskLimitMb, setDiskLimitMb] = useState('')
  const [startCommand, setStartCommand] = useState('')
  const [workDir, setWorkDir] = useState('')
  const [autoRestart, setAutoRestart] = useState(true)
  const [groupId, setGroupId] = useState('')
  const [templateId, setTemplateId] = useState('')
  const [jdkId, setJdkId] = useState('')
  const gate = useFieldGate()

  const { data: jdks } = useNodeJDKs(nodeId ? Number(nodeId) : 0)

  // 系统可获取项 → 可编辑/可搜索下拉选项（FR-072）。ID 绑定项关闭自定义，字符串项允许自定义。
  const nodeOptions: ComboboxOption[] = (nodes ?? [])
    .filter((n) => n.status === 1)
    .map((n) => ({ value: String(n.id), label: n.name }))
  const jdkOptions: ComboboxOption[] = (jdks ?? []).map((j) => ({
    value: String(j.id),
    label: `${j.vendor} ${j.majorVersion} (${j.version})`,
  }))
  const groupOptions: ComboboxOption[] = (groups ?? []).map((g) => ({ value: String(g.id), label: g.name }))
  const templateOptions: ComboboxOption[] = (templates ?? []).map((tpl) => ({ value: String(tpl.id), label: tpl.name }))
  const typeOptions: ComboboxOption[] = [
    { value: 'minecraft_java', label: 'Minecraft Java' },
    { value: 'generic', label: t('common.type') },
  ]
  const processTypeOptions: ComboboxOption[] = [
    { value: 'daemon', label: `daemon (${t('common.enabled')})` },
    { value: 'direct', label: 'direct' },
    { value: 'docker', label: 'docker' },
  ]

  // 提交前校验：名称/启动命令必填；非 MC 类型工作目录必填且须为绝对路径（MC 由系统分配）。
  // docker 模式额外要求镜像必填（ADR-019）。
  const needWorkDir = type !== 'minecraft_java'
  const isDocker = processType === 'docker'
  const errors = validateFields(
    { name, nodeId, startCommand, workDir, image, cpuLimit, memLimitMb, diskLimitMb },
    {
      name: [validateRequired],
      nodeId: [validateRequired],
      startCommand: [validateRequired],
      workDir: needWorkDir ? [validateRequired, validateAbsPath] : [],
      image: isDocker ? [validateRequired] : [],
      // 资源限额选填，仅 docker 模式校验；空/0/负值=不限制（FR-079）。
      cpuLimit: isDocker ? [validateResourceLimitNumber] : [],
      memLimitMb: isDocker ? [validateResourceLimitNumber] : [],
      diskLimitMb: isDocker ? [validateResourceLimitNumber] : [],
    },
  )

  const create = useMutation({
    mutationFn: (body: Record<string, unknown>) => api.post('/instances', body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['instances'] })
      toast.success('实例已创建')
      onClose()
      resetForm()
    },
    onError: (err: Error & { response?: { data?: { message?: string } } }) => {
      toast.error(err.response?.data?.message || t('instances.createFailed'))
    },
  })

  const resetForm = () => {
    setName('')
    setNodeId('')
    setType('minecraft_java')
    setProcessType('daemon')
    setImage('')
    setCpuLimit('')
    setMemLimitMb('')
    setDiskLimitMb('')
    setStartCommand('')
    setWorkDir('')
    setAutoRestart(true)
    setGroupId('')
    setTemplateId('')
    setJdkId('')
    gate.reset()
  }

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault()
    gate.submit()
    if (hasErrors(errors)) return
    create.mutate({
      nodeId: Number(nodeId),
      name,
      type,
      processType,
      startCommand,
      workDir,
      autoRestart,
      groupId: groupId ? Number(groupId) : undefined,
      jdkId: jdkId ? Number(jdkId) : undefined,
      // docker 模式下发镜像（ADR-019）；其它模式不传。
      image: isDocker ? image : undefined,
      // docker 模式下发资源限额（FR-079）；空传 0，0/负值由后端视为不限制；非 docker 模式不传。
      cpuLimit: isDocker ? (cpuLimit.trim() ? Number(cpuLimit) : 0) : undefined,
      memLimitMb: isDocker ? (memLimitMb.trim() ? Number(memLimitMb) : 0) : undefined,
      diskLimitMb: isDocker ? (diskLimitMb.trim() ? Number(diskLimitMb) : 0) : undefined,
    })
  }

  const onPickTemplate = (tid: string) => {
    setTemplateId(tid)
    if (tid) {
      const tpl = templates?.find((tpl) => String(tpl.id) === tid)
      if (tpl) {
        setStartCommand(tpl.startCommand)
        setType(tpl.type || type)
        if (tpl.defaultWorkDir) setWorkDir(tpl.defaultWorkDir)
      }
    }
  }

  const handleClose = () => {
    onClose()
    resetForm()
  }

  return (
    <Dialog open={open} onOpenChange={(v: boolean) => { if (!v) handleClose() }}>
      <DialogContent className={`${scrollableDialogContentClass} sm:max-w-2xl`}>
        <form onSubmit={handleSubmit} className="flex min-h-0 flex-1 flex-col">
          <DialogHeader>
            <DialogTitle>{t('instances.createInstance')}</DialogTitle>
          </DialogHeader>

          <ScrollableDialogBody className="space-y-5 py-2">
            {/* 基本：名称 / 节点 / 类型 / 模板 / 用户组 */}
            <FormSection title={t('instances.sectionBasic')}>
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                <div>
                  <FieldLabel required>{t('instances.instanceName')}</FieldLabel>
                  <input
                    value={name}
                    onChange={(e) => setName(e.target.value)}
                    onBlur={() => gate.touch('name')}
                    className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm aria-invalid:border-destructive"
                    placeholder="Survival Server"
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

                <div>
                  <FieldLabel>{t('instances.type')}</FieldLabel>
                  <div className="mt-1">
                    <Combobox options={typeOptions} value={type} onChange={setType} />
                  </div>
                </div>

                <div>
                  <FieldLabel>{t('templates.selectTemplate').replace(/[（(].*[）)]/, '').trim()}</FieldLabel>
                  <div className="mt-1">
                    <Combobox
                      options={templateOptions}
                      value={templateId}
                      onChange={onPickTemplate}
                      allowCustom={false}
                      placeholder={t('templates.noTemplate')}
                    />
                  </div>
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
              </div>
            </FormSection>

            {/* 启动：启动方式 / 启动命令 / JDK / 工作目录 */}
            <FormSection title={t('instances.sectionLaunch')}>
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                <div>
                  <FieldLabel>{t('instanceDetail.processType')}</FieldLabel>
                  <div className="mt-1">
                    <Combobox
                      options={processTypeOptions}
                      value={processType}
                      onChange={setProcessType}
                      allowCustom={false}
                    />
                  </div>
                </div>

                <div>
                  <FieldLabel>{t('instances.jdkOptional')}</FieldLabel>
                  <div className="mt-1">
                    <Combobox
                      options={jdkOptions}
                      value={jdkId}
                      onChange={setJdkId}
                      allowCustom={false}
                      placeholder={t('instances.jdkSystemDefault')}
                    />
                  </div>
                  <p className="mt-1 text-xs text-muted-foreground">绑定后启动实例时会自动注入 JAVA_HOME 与 PATH</p>
                </div>
              </div>

              <div>
                <FieldLabel required>{t('instanceDetail.startCommand')}</FieldLabel>
                <input
                  value={startCommand}
                  onChange={(e) => setStartCommand(e.target.value)}
                  onBlur={() => gate.touch('startCommand')}
                  className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm font-mono aria-invalid:border-destructive"
                  placeholder="java -Xmx2G -jar paper.jar nogui"
                  aria-invalid={!!gate.show('startCommand', errors.startCommand)}
                />
                {gate.show('startCommand', errors.startCommand) ? (
                  <FieldError error={gate.show('startCommand', errors.startCommand)} />
                ) : (
                  <p className="mt-1 text-xs text-muted-foreground">直接填写命令，不要用引号包裹整个命令</p>
                )}
              </div>

              {type === 'minecraft_java' ? (
                <div>
                  <FieldLabel>{t('instanceDetail.workDir')}</FieldLabel>
                  <div className="w-full mt-1 px-3 py-2 border rounded-md bg-muted/40 text-sm text-muted-foreground">
                    {t('instances.workDirSystemAssigned')}
                  </div>
                </div>
              ) : (
                <div>
                  <FieldLabel required>{t('instanceDetail.workDir')}</FieldLabel>
                  <input
                    value={workDir}
                    onChange={(e) => setWorkDir(e.target.value)}
                    onBlur={() => gate.touch('workDir')}
                    className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm aria-invalid:border-destructive"
                    placeholder="/servers/survival"
                    aria-invalid={!!gate.show('workDir', errors.workDir)}
                  />
                  {gate.show('workDir', errors.workDir) ? (
                    <FieldError error={gate.show('workDir', errors.workDir)} />
                  ) : (
                    <p className="mt-1 text-xs text-muted-foreground">{t('instances.workDirHint')}</p>
                  )}
                </div>
              )}

              <label className="flex items-center gap-2 text-sm">
                <input
                  type="checkbox"
                  checked={autoRestart}
                  onChange={(e) => setAutoRestart(e.target.checked)}
                />
                {t('instanceDetail.autoRestart')}
              </label>
            </FormSection>

            {/* 高级：Docker 镜像 + 资源限额（FR-079）。仅启动方式=docker 时出现，去掉非 docker 噪音占位。 */}
            {isDocker && (
              <FormSection title={t('instances.sectionAdvanced')}>
                <div>
                  <FieldLabel required>{t('instances.dockerImage')}</FieldLabel>
                  <input
                    value={image}
                    onChange={(e) => setImage(e.target.value)}
                    onBlur={() => gate.touch('image')}
                    className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm font-mono aria-invalid:border-destructive"
                    placeholder="itzg/minecraft-server:latest"
                    aria-invalid={!!gate.show('image', errors.image)}
                  />
                  {gate.show('image', errors.image) ? (
                    <FieldError error={gate.show('image', errors.image)} />
                  ) : (
                    <p className="mt-1 text-xs text-muted-foreground">{t('instances.dockerImageHint')}</p>
                  )}
                </div>

                <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
                  <div>
                    <FieldLabel>{t('instances.cpuLimit')}</FieldLabel>
                    <input
                      value={cpuLimit}
                      onChange={(e) => setCpuLimit(e.target.value)}
                      onBlur={() => gate.touch('cpuLimit')}
                      className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm aria-invalid:border-destructive"
                      placeholder="1.5"
                      inputMode="decimal"
                      aria-invalid={!!gate.show('cpuLimit', errors.cpuLimit)}
                    />
                    {gate.show('cpuLimit', errors.cpuLimit) ? (
                      <FieldError error={gate.show('cpuLimit', errors.cpuLimit)} />
                    ) : (
                      <p className="mt-1 text-xs text-muted-foreground">{t('instances.resourceLimitHint')}</p>
                    )}
                  </div>
                  <div>
                    <FieldLabel>{t('instances.memLimit')}</FieldLabel>
                    <input
                      value={memLimitMb}
                      onChange={(e) => setMemLimitMb(e.target.value)}
                      onBlur={() => gate.touch('memLimitMb')}
                      className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm aria-invalid:border-destructive"
                      placeholder="2048"
                      inputMode="numeric"
                      aria-invalid={!!gate.show('memLimitMb', errors.memLimitMb)}
                    />
                    {gate.show('memLimitMb', errors.memLimitMb) ? (
                      <FieldError error={gate.show('memLimitMb', errors.memLimitMb)} />
                    ) : (
                      <p className="mt-1 text-xs text-muted-foreground">{t('instances.resourceLimitHint')}</p>
                    )}
                  </div>
                  <div>
                    <FieldLabel>{t('instances.diskLimit')}</FieldLabel>
                    <input
                      value={diskLimitMb}
                      onChange={(e) => setDiskLimitMb(e.target.value)}
                      onBlur={() => gate.touch('diskLimitMb')}
                      className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm aria-invalid:border-destructive"
                      placeholder="10240"
                      inputMode="numeric"
                      aria-invalid={!!gate.show('diskLimitMb', errors.diskLimitMb)}
                    />
                    {gate.show('diskLimitMb', errors.diskLimitMb) ? (
                      <FieldError error={gate.show('diskLimitMb', errors.diskLimitMb)} />
                    ) : (
                      <p className="mt-1 text-xs text-muted-foreground">{t('instances.diskLimitHint')}</p>
                    )}
                  </div>
                </div>
              </FormSection>
            )}
          </ScrollableDialogBody>

          <DialogFooter>
            <Button type="button" variant="outline" onClick={handleClose}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" disabled={create.isPending || hasErrors(errors)}>
              {create.isPending ? t('common.creating') : t('common.create')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

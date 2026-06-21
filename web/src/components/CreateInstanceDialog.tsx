import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { useQueryClient, useMutation } from '@tanstack/react-query'
import { toast } from 'sonner'
import api from '@/api/client'
import { useNodes } from '@/api/nodes'
import { useGroups } from '@/api/groups'
import { useTemplates } from '@/api/templates'
import { useNodeJDKs } from '@/api/jdks'
import { MODAL_OVERLAY, MODAL_PANEL } from '@/components/ui/scrollable-dialog'
import { Combobox, type ComboboxOption } from '@/components/ui/combobox'
import { FieldLabel, FieldError } from '@/components/ui/field-label'
import {
  validateRequired,
  validateAbsPath,
  validateFields,
  hasErrors,
} from '@/lib/form-validation'

interface CreateInstanceDialogProps {
  open: boolean
  onClose: () => void
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
  const [startCommand, setStartCommand] = useState('')
  const [workDir, setWorkDir] = useState('')
  const [autoRestart, setAutoRestart] = useState(true)
  const [groupId, setGroupId] = useState('')
  const [templateId, setTemplateId] = useState('')
  const [jdkId, setJdkId] = useState('')

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
  const needWorkDir = type !== 'minecraft_java'
  const errors = validateFields(
    { name, nodeId, startCommand, workDir },
    {
      name: [validateRequired],
      nodeId: [validateRequired],
      startCommand: [validateRequired],
      workDir: needWorkDir ? [validateRequired, validateAbsPath] : [],
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
    setStartCommand('')
    setWorkDir('')
    setAutoRestart(true)
    setGroupId('')
    setTemplateId('')
    setJdkId('')
  }

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault()
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

  if (!open) return null

  return (
    <div className={MODAL_OVERLAY}>
      <div className={`${MODAL_PANEL} max-w-md`}>
        <h2 className="text-lg font-bold mb-4">{t('instances.createInstance')}</h2>

        <form onSubmit={handleSubmit} className="space-y-3">
          <div>
            <FieldLabel required>{t('instances.instanceName')}</FieldLabel>
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm aria-invalid:border-destructive"
              placeholder="Survival Server"
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
          </div>

          <div>
            <FieldLabel required>{t('instanceDetail.startCommand')}</FieldLabel>
            <input
              value={startCommand}
              onChange={(e) => setStartCommand(e.target.value)}
              className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm font-mono aria-invalid:border-destructive"
              placeholder="java -Xmx2G -jar paper.jar nogui"
              aria-invalid={!!errors.startCommand}
            />
            {errors.startCommand ? (
              <FieldError error={errors.startCommand} />
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
                className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm aria-invalid:border-destructive"
                placeholder="/servers/survival"
                aria-invalid={!!errors.workDir}
              />
              {errors.workDir ? (
                <FieldError error={errors.workDir} />
              ) : (
                <p className="mt-1 text-xs text-muted-foreground">{t('instances.workDirHint')}</p>
              )}
            </div>
          )}

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

          <label className="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={autoRestart}
              onChange={(e) => setAutoRestart(e.target.checked)}
            />
            {t('instanceDetail.autoRestart')}
          </label>

          <div className="flex justify-end gap-2 pt-2">
            <button
              type="button"
              onClick={() => { onClose(); resetForm() }}
              className="px-4 py-2 text-sm border rounded-md hover:bg-accent"
            >
              {t('common.cancel')}
            </button>
            <button
              type="submit"
              disabled={create.isPending || hasErrors(errors)}
              className="px-4 py-2 text-sm bg-primary text-primary-foreground rounded-md disabled:opacity-50"
            >
              {create.isPending ? t('common.creating') : t('common.create')}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}

import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate, useSearchParams } from 'react-router'
import { useQueryClient, useMutation } from '@tanstack/react-query'
import { toast } from 'sonner'
import { ArrowLeft, Boxes, ChevronLeft, ChevronRight, Check, Plus, X } from 'lucide-react'
import api from '@/api/client'
import { useNodes } from '@/api/nodes'
import { useNodeDockerCheck } from '@/api/docker'
import { buildNodeOptions, initialWizardNodeId } from './instance-wizard-options'
import { useGroups } from '@/api/groups'
import { useTemplates } from '@/api/templates'
import { useNodeJDKs } from '@/api/jdks'
import { Panel } from '@jianmanager/ui/components/panel'
import { Button } from '@jianmanager/ui/components/button'
import { Combobox, type ComboboxOption } from '@jianmanager/ui/components/combobox'
import { FieldLabel, FieldError } from '@jianmanager/ui/components/field-label'
import { useConsoleStore } from '@/stores/console'
import { cn } from '@jianmanager/ui'
import {
  validateRequired,
  validateResourceLimitNumber,
  validateFields,
} from '@/lib/form-validation'
import { useFieldGate } from '@/lib/use-field-gate'

/** 向导步骤键。advanced 仅 docker 启动方式出现。 */
type StepKey = 'basic' | 'launch' | 'advanced' | 'review'

const inputClass =
  'w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm aria-invalid:border-destructive'

/** 把 docker 环境变量键值对收敛为对象（去空键、key 去首尾空格）；无有效项返回 undefined。 */
function envVarsFromPairs(pairs: { key: string; value: string }[]): Record<string, string> | undefined {
  const out: Record<string, string> = {}
  for (const p of pairs) {
    const k = p.key.trim()
    if (k) out[k] = p.value
  }
  return Object.keys(out).length > 0 ? out : undefined
}

/**
 * 创建实例向导页（FR-230，取代 FR-189 的创建模态）：
 * 单独路由 `/instances/new`，分步引导（基本→启动→[高级]→确认）+ 每步大白话提示，
 * 复用既有 POST /instances 与字段校验。?node= / ?template= 支持从实例页/模板页预填。
 * 编辑等其它场景仍用模态（本页只接管「创建」）。
 */
export default function InstanceWizardPage() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [searchParams] = useSearchParams()

  const { data: nodes } = useNodes()
  const { data: groups } = useGroups()
  const { data: templates } = useTemplates()
  const selectedNodeId = useConsoleStore((s) => s.selectedNodeId)

  const [name, setName] = useState('')
  const [nodeId, setNodeId] = useState(initialWizardNodeId(searchParams.get('node'), selectedNodeId))
  const [type, setType] = useState('minecraft_java')
  const [processType, setProcessType] = useState('daemon')
  const [image, setImage] = useState('')
  const [cpuLimit, setCpuLimit] = useState('')
  const [memLimitMb, setMemLimitMb] = useState('')
  const [diskLimitMb, setDiskLimitMb] = useState('')
  const [startCommand, setStartCommand] = useState('java -Xmx2G -jar server.jar nogui')
  const [autoRestart, setAutoRestart] = useState(true)
  const [groupId, setGroupId] = useState('')
  const [templateId, setTemplateId] = useState(searchParams.get('template') ?? '')
  const [jdkId, setJdkId] = useState('')
  // docker 环境变量键值对（FR-236）：一键 Minecraft 预设注入 EULA=TRUE；可手动增删。
  const [envPairs, setEnvPairs] = useState<{ key: string; value: string }[]>([])
  const gate = useFieldGate()

  const { data: jdks } = useNodeJDKs(nodeId ? Number(nodeId) : 0)

  const isDocker = processType === 'docker'
  // Docker 可用性检测（FR-237）：选 docker 且已选节点时探目标节点，不可用或检测失败均阻止提交并提示。
  const dockerCheck = useNodeDockerCheck(nodeId ? Number(nodeId) : 0, isDocker)
  const dockerUnavailable = dockerCheck.data != null && !dockerCheck.data.available
  const dockerCheckFailed = dockerCheck.isError
  const dockerChecking = isDocker && dockerCheck.isFetching
  const dockerBlocked = isDocker && (dockerUnavailable || dockerCheckFailed)

  const nodeOptions: ComboboxOption[] = buildNodeOptions(nodes, {
    online: t('nodes.online'),
    offline: t('nodes.offline'),
    starting: t('nodes.starting'),
    maintenance: t('nodes.maintenance'),
  })
  const hasNoNodes = (nodes?.length ?? 0) === 0
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

  const errors = validateFields(
    { name, nodeId, startCommand, image, cpuLimit, memLimitMb, diskLimitMb },
    {
      name: [validateRequired],
      nodeId: [validateRequired],
      startCommand: isDocker ? [] : [validateRequired],
      image: isDocker ? [validateRequired] : [],
      cpuLimit: isDocker ? [validateResourceLimitNumber] : [],
      memLimitMb: isDocker ? [validateResourceLimitNumber] : [],
      diskLimitMb: isDocker ? [validateResourceLimitNumber] : [],
    },
  )

  const steps: StepKey[] = isDocker ? ['basic', 'launch', 'advanced', 'review'] : ['basic', 'launch', 'review']
  const [stepIdx, setStepIdx] = useState(0)
  const safeIdx = Math.min(stepIdx, steps.length - 1)
  const step = steps[safeIdx]
  const isLast = safeIdx === steps.length - 1

  // 单步是否可继续（仅校验该步内的必填项）。
  const stepValid = (k: StepKey): boolean => {
    if (k === 'basic') return !errors.name && !errors.nodeId
    if (k === 'launch') return !errors.startCommand
    if (k === 'advanced') return !errors.image && !errors.cpuLimit && !errors.memLimitMb && !errors.diskLimitMb && !dockerChecking && !dockerBlocked
    return true
  }

  // 各步受校验字段：「下一步」点击视为该步提交，仅揭示该步字段错误（不波及后续步）。
  const STEP_FIELDS: Record<StepKey, string[]> = {
    basic: ['name', 'nodeId'],
    launch: ['startCommand'],
    advanced: ['image', 'cpuLimit', 'memLimitMb', 'diskLimitMb'],
    review: [],
  }

  const create = useMutation({
    mutationFn: (body: Record<string, unknown>) => api.post('/instances', body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['instances'] })
      toast.success(t('instances.created', '实例已创建'))
      navigate('/instances')
    },
    onError: (err: Error & { response?: { data?: { message?: string } } }) => {
      toast.error(err.response?.data?.message || t('instances.createFailed'))
    },
  })

  const onPickTemplate = (tid: string) => {
    setTemplateId(tid)
    if (tid) {
      const tpl = templates?.find((tpl) => String(tpl.id) === tid)
      if (tpl) {
        setStartCommand(tpl.startCommand)
        setType(tpl.type || type)
      }
    }
  }

  // 一键 Minecraft docker 傻瓜建服（FR-236）：itzg 镜像 + EULA=TRUE + 空命令（交镜像 entrypoint 自管启动）。
  const applyMinecraftDockerPreset = () => {
    setType('minecraft_java')
    setImage('itzg/minecraft-server:latest')
    setEnvPairs([{ key: 'EULA', value: 'TRUE' }])
    setStartCommand('')
  }

  const submit = () => {
    gate.submit()
    if (!steps.every(stepValid)) return
    create.mutate({
      nodeId: Number(nodeId),
      name,
      type,
      processType,
      startCommand,
      autoRestart,
      groupId: groupId ? Number(groupId) : undefined,
      jdkId: jdkId ? Number(jdkId) : undefined,
      image: isDocker ? image : undefined,
      cpuLimit: isDocker ? (cpuLimit.trim() ? Number(cpuLimit) : 0) : undefined,
      memLimitMb: isDocker ? (memLimitMb.trim() ? Number(memLimitMb) : 0) : undefined,
      diskLimitMb: isDocker ? (diskLimitMb.trim() ? Number(diskLimitMb) : 0) : undefined,
      envVars: isDocker ? envVarsFromPairs(envPairs) : undefined,
    })
  }

  const nodeLabel = nodeOptions.find((o) => o.value === nodeId)?.label || nodeId
  const jdkLabel = jdkOptions.find((o) => o.value === jdkId)?.label || t('instances.jdkSystemDefault')

  return (
    <div className="mx-auto max-w-3xl space-y-5">
      <div className="flex items-center gap-2">
        <button
          type="button"
          onClick={() => navigate('/instances')}
          className="grid size-8 place-items-center rounded-md text-muted-foreground transition-colors hover:bg-accent/60 hover:text-foreground"
          aria-label={t('common.back')}
        >
          <ArrowLeft className="size-4" />
        </button>
        <div>
          <h1 className="text-xl font-bold">{t('instances.createInstance')}</h1>
          <p className="text-xs text-muted-foreground">{t('instances.wizardSubtitle', '按步骤填写，几步就能在某台节点上建好一个服。')}</p>
        </div>
      </div>

      {/* 步骤指示器 */}
      <ol className="flex flex-wrap items-center gap-x-2 gap-y-1 text-sm">
        {steps.map((k, i) => (
          <li key={k} className="flex items-center gap-2">
            <span
              className={cn(
                'flex size-6 shrink-0 items-center justify-center rounded-full text-xs font-semibold',
                i < safeIdx ? 'bg-primary text-primary-foreground'
                  : i === safeIdx ? 'bg-primary/15 text-primary ring-2 ring-primary/40'
                    : 'bg-muted text-muted-foreground',
              )}
            >
              {i < safeIdx ? <Check className="size-3.5" /> : i + 1}
            </span>
            <span className={cn(i === safeIdx ? 'font-medium text-foreground' : 'text-muted-foreground')}>
              {t(`instances.wizardStep.${k}`)}
            </span>
            {i < steps.length - 1 && <ChevronRight className="size-4 text-muted-foreground/50" />}
          </li>
        ))}
      </ol>

      <Panel bodyClassName="space-y-4 p-5">
        {/* 大白话提示 */}
        <p className="rounded-md bg-primary/5 px-3 py-2 text-xs text-muted-foreground">
          {t(`instances.wizardHint.${step}`)}
        </p>

        {step === 'basic' && (
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <Field label={t('instances.instanceName')} required error={gate.show('name', errors.name)}>
              <input value={name} onChange={(e) => setName(e.target.value)} onBlur={() => gate.touch('name')} className={inputClass} placeholder="Survival Server" aria-invalid={!!gate.show('name', errors.name)} />
            </Field>
            <Field label={t('instances.node')} required error={gate.show('nodeId', errors.nodeId)}>
              <div className="mt-1">
                <Combobox options={nodeOptions} value={nodeId} onChange={(v) => { gate.touch('nodeId'); setNodeId(v) }} allowCustom={false} placeholder={t('instances.selectNode')} invalid={!!gate.show('nodeId', errors.nodeId)} />
                {hasNoNodes && (
                  <p className="mt-1.5 text-xs text-muted-foreground">{t('instances.noNodesHint')}</p>
                )}
              </div>
            </Field>
            <Field label={t('instances.type')}>
              <div className="mt-1"><Combobox options={typeOptions} value={type} onChange={setType} /></div>
            </Field>
            <Field label={t('templates.selectTemplate').replace(/[（(].*[）)]/, '').trim()}>
              <div className="mt-1">
                <Combobox options={templateOptions} value={templateId} onChange={onPickTemplate} allowCustom={false} placeholder={t('templates.noTemplate')} />
              </div>
            </Field>
            <Field label={t('instances.group')}>
              <div className="mt-1">
                <Combobox options={groupOptions} value={groupId} onChange={setGroupId} allowCustom={false} placeholder={t('instances.noGroup')} />
              </div>
            </Field>
          </div>
        )}

        {step === 'launch' && (
          <div className="space-y-3">
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
              <Field label={t('instanceDetail.processType')}>
                <div className="mt-1"><Combobox options={processTypeOptions} value={processType} onChange={setProcessType} allowCustom={false} /></div>
              </Field>
              <Field label={t('instances.jdkOptional')}>
                <div className="mt-1"><Combobox options={jdkOptions} value={jdkId} onChange={setJdkId} allowCustom={false} placeholder={t('instances.jdkSystemDefault')} /></div>
              </Field>
            </div>
            <Field label={t('instanceDetail.startCommand')} required={!isDocker} error={gate.show('startCommand', errors.startCommand)}>
              <input value={startCommand} onChange={(e) => setStartCommand(e.target.value)} onBlur={() => gate.touch('startCommand')} className={cn(inputClass, 'font-mono')} placeholder="java -Xmx2G -jar server.jar nogui" aria-invalid={!!gate.show('startCommand', errors.startCommand)} />
              <p className="mt-1.5 text-xs text-muted-foreground">{isDocker ? t('instances.startCommandDockerHint') : t('instances.startCommandHint')}</p>
            </Field>
            <label className="flex items-center gap-2 text-sm">
              <input type="checkbox" checked={autoRestart} onChange={(e) => setAutoRestart(e.target.checked)} />
              {t('instanceDetail.autoRestart')}
            </label>
          </div>
        )}

        {step === 'advanced' && (
          <div className="space-y-3">
            {/* Docker 可用性检测（FR-237）：探目标节点 Docker，不可用提示并阻止提交 */}
            {dockerCheck.isFetching && (
              <p className="rounded-md border bg-muted/40 px-3 py-2 text-xs text-muted-foreground">{t('instances.dockerChecking')}</p>
            )}
            {!dockerCheck.isFetching && dockerCheck.data?.available && (
              <p className="rounded-md border border-status-success/40 bg-status-success/5 px-3 py-2 text-xs text-status-success">
                {t('instances.dockerAvailable', { version: dockerCheck.data.version })}
              </p>
            )}
            {dockerBlocked && (
              <p className="rounded-md border border-status-danger/40 bg-status-danger/5 px-3 py-2 text-xs text-status-danger">
                {t('instances.dockerUnavailable')}
                {dockerCheck.data?.error ? `：${dockerCheck.data.error}` : ''}
              </p>
            )}
            {/* 一键 Minecraft 傻瓜建服（FR-236）：itzg 镜像 + EULA=TRUE + 空命令 */}
            <div className="flex items-center justify-between gap-2 rounded-md border border-primary/30 bg-primary/5 p-3">
              <div className="min-w-0">
                <p className="text-sm font-medium">{t('instances.dockerMcPresetTitle')}</p>
                <p className="text-xs text-muted-foreground">{t('instances.dockerMcPresetHint')}</p>
              </div>
              <Button type="button" size="sm" variant="outline" className="shrink-0" onClick={applyMinecraftDockerPreset}>
                <Boxes className="size-4" />
                {t('instances.dockerMcPresetApply')}
              </Button>
            </div>
            <Field label={t('instances.dockerImage')} required error={gate.show('image', errors.image)}>
              <input value={image} onChange={(e) => setImage(e.target.value)} onBlur={() => gate.touch('image')} className={cn(inputClass, 'font-mono')} placeholder="itzg/minecraft-server:latest" aria-invalid={!!gate.show('image', errors.image)} />
            </Field>
            <EnvVarsEditor pairs={envPairs} onChange={setEnvPairs} />
            <div>
              <p className="mb-2 text-xs text-muted-foreground">{t('instances.resourceLimitHint')}</p>
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
                <Field label={t('instances.cpuLimit')} error={gate.show('cpuLimit', errors.cpuLimit)}>
                  <input value={cpuLimit} onChange={(e) => setCpuLimit(e.target.value)} onBlur={() => gate.touch('cpuLimit')} className={inputClass} placeholder="1.5" inputMode="decimal" aria-invalid={!!gate.show('cpuLimit', errors.cpuLimit)} />
                </Field>
                <Field label={t('instances.memLimit')} error={gate.show('memLimitMb', errors.memLimitMb)}>
                  <input value={memLimitMb} onChange={(e) => setMemLimitMb(e.target.value)} onBlur={() => gate.touch('memLimitMb')} className={inputClass} placeholder="2048" inputMode="numeric" aria-invalid={!!gate.show('memLimitMb', errors.memLimitMb)} />
                </Field>
                <Field label={t('instances.diskLimit')} error={gate.show('diskLimitMb', errors.diskLimitMb)}>
                  <input value={diskLimitMb} onChange={(e) => setDiskLimitMb(e.target.value)} onBlur={() => gate.touch('diskLimitMb')} className={inputClass} placeholder="10240" inputMode="numeric" aria-invalid={!!gate.show('diskLimitMb', errors.diskLimitMb)} />
                  <p className="mt-1.5 text-xs text-muted-foreground">{t('instances.diskLimitHint')}</p>
                </Field>
              </div>
            </div>
          </div>
        )}

        {step === 'review' && (
          <dl className="divide-y rounded-md border text-sm">
            <ReviewRow label={t('instances.instanceName')} value={name} />
            <ReviewRow label={t('instances.node')} value={nodeLabel} />
            <ReviewRow label={t('instances.type')} value={type} />
            <ReviewRow label={t('instanceDetail.processType')} value={processType} />
            <ReviewRow label={t('instanceDetail.startCommand')} value={startCommand} mono />
            <ReviewRow label={t('instances.jdkOptional')} value={jdkLabel} />
            {isDocker && <ReviewRow label={t('instances.dockerImage')} value={image} mono />}
            {isDocker && <ReviewRow label={t('instances.cpuLimit')} value={cpuLimit || t('instances.unlimited')} />}
            {isDocker && <ReviewRow label={t('instances.memLimit')} value={memLimitMb || t('instances.unlimited')} />}
            {isDocker && <ReviewRow label={t('instances.diskLimit')} value={diskLimitMb || t('instances.unlimited')} />}
            <ReviewRow label={t('instanceDetail.autoRestart')} value={autoRestart ? t('common.enabled') : t('common.disabled', '关闭')} />
          </dl>
        )}
      </Panel>

      {/* 底部导航 */}
      <div className="flex items-center justify-between">
        <Button type="button" variant="outline" onClick={() => (safeIdx === 0 ? navigate('/instances') : setStepIdx(safeIdx - 1))}>
          <ChevronLeft className="size-4" />
          {safeIdx === 0 ? t('common.cancel') : t('common.back')}
        </Button>
        {isLast ? (
          <Button type="button" onClick={submit} disabled={create.isPending || !steps.every(stepValid)}>
            {create.isPending ? t('common.creating') : t('common.create')}
          </Button>
        ) : (
          <Button type="button" onClick={() => { STEP_FIELDS[step].forEach(gate.touch); setStepIdx(safeIdx + 1) }} disabled={!stepValid(step)}>
            {t('instances.wizardNext')}
            <ChevronRight className="size-4" />
          </Button>
        )}
      </div>
    </div>
  )
}

/** 字段壳：标签 + 必填星 + 子控件 + 错误。 */
function Field({ label, required, error, children }: { label: string; required?: boolean; error?: string; children: ReactNode }) {
  return (
    <div>
      <FieldLabel required={required}>{label}</FieldLabel>
      {children}
      <FieldError error={error} />
    </div>
  )
}

/** 确认页一行：标签 + 值（空显占位）。 */
function ReviewRow({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex items-start justify-between gap-4 px-3 py-2">
      <dt className="shrink-0 text-muted-foreground">{label}</dt>
      <dd className={cn('min-w-0 break-all text-right', mono && 'font-mono text-xs')}>{value || '—'}</dd>
    </div>
  )
}

/** docker 环境变量键值对编辑器（FR-236）：itzg 需 EULA=TRUE 等；增删行、空键提交时忽略。 */
function EnvVarsEditor({
  pairs,
  onChange,
}: {
  pairs: { key: string; value: string }[]
  onChange: (p: { key: string; value: string }[]) => void
}) {
  const { t } = useTranslation()
  const setAt = (i: number, patch: Partial<{ key: string; value: string }>) =>
    onChange(pairs.map((p, idx) => (idx === i ? { ...p, ...patch } : p)))
  return (
    <Field label={t('instances.dockerEnvVars')}>
      <div className="mt-1 space-y-2">
        {pairs.length === 0 && <p className="text-xs text-muted-foreground">{t('instances.dockerEnvEmpty')}</p>}
        {pairs.map((p, i) => (
          <div key={i} className="flex items-center gap-2">
            <input
              value={p.key}
              onChange={(e) => setAt(i, { key: e.target.value })}
              placeholder="KEY"
              aria-label={t('instances.dockerEnvKey')}
              className={cn(inputClass, 'mt-0 font-mono')}
            />
            <span className="shrink-0 text-muted-foreground">=</span>
            <input
              value={p.value}
              onChange={(e) => setAt(i, { value: e.target.value })}
              placeholder="VALUE"
              aria-label={t('instances.dockerEnvValue')}
              className={cn(inputClass, 'mt-0 font-mono')}
            />
            <button
              type="button"
              onClick={() => onChange(pairs.filter((_, idx) => idx !== i))}
              aria-label={t('common.delete')}
              className="shrink-0 rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-accent/60 hover:text-destructive"
            >
              <X className="size-4" />
            </button>
          </div>
        ))}
        <Button type="button" size="sm" variant="outline" onClick={() => onChange([...pairs, { key: '', value: '' }])}>
          <Plus className="size-4" />
          {t('instances.dockerEnvAdd')}
        </Button>
      </div>
    </Field>
  )
}

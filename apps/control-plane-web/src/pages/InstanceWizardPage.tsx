import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate, useSearchParams } from 'react-router'
import { useQueryClient, useMutation } from '@tanstack/react-query'
import { toast } from 'sonner'

import type { WizardTemplateOption } from '@/components/views/InstanceWizardPage'
import { initialWizardNodeId } from '@/lib/instance-wizard-options'
import { InstanceWizardPage as InstanceWizardView } from '@/components/views/InstanceWizardPage'
import type { ComboboxOption } from '@jianmanager/ui/components/combobox'
import api from '@/api/client'
import { useNodes } from '@/api/nodes'
import { useNodeDockerCheck } from '@/api/docker'
import { useGroups } from '@/api/groups'
import { useTemplates } from '@/api/templates'
import { useNodeJDKs } from '@/api/jdks'
import { useConsoleStore } from '@/stores/console'

/**
 * 创建实例向导接线层（ADR-097）：取数、Docker 检测、创建请求与导航留在这里，
 * 视图（包内 `InstanceWizardPage`）只做受控渲染。
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

  // 向导内选中的节点：驱动 JDK 候选与 Docker 检测（初始值同视图，含 ?node= 预填）。
  const [nodeId, setNodeId] = useState(initialWizardNodeId(searchParams.get('node'), selectedNodeId))
  const { data: jdks } = useNodeJDKs(nodeId ? Number(nodeId) : 0)

  // 启动方式由视图上报（视图持有该状态），据此开关 Docker 检测。
  const [processType, setProcessType] = useState('daemon')
  const isDocker = processType === 'docker'
  const dockerCheck = useNodeDockerCheck(nodeId ? Number(nodeId) : 0, isDocker)

  const create = useMutation({
    mutationFn: (body: Record<string, unknown>) => api.post('/instances', body),
  })

  const jdkOptions: ComboboxOption[] = (jdks ?? []).map((j) => ({
    value: String(j.id),
    label: `${j.vendor} ${j.majorVersion} (${j.version})`,
  }))
  const groupOptions: ComboboxOption[] = (groups ?? []).map((g) => ({ value: String(g.id), label: g.name }))
  const templateOptions: WizardTemplateOption[] = (templates ?? []).map((tpl) => ({
    id: tpl.id,
    name: tpl.name,
    startCommand: tpl.startCommand,
    type: tpl.type,
  }))

  return (
    <InstanceWizardView
      initialNodeId={nodeId}
      initialTemplateId={searchParams.get('template') ?? ''}
      nodes={nodes}
      nodeStatusLabels={{
        online: t('nodes.online'),
        offline: t('nodes.offline'),
        starting: t('nodes.starting'),
        maintenance: t('nodes.maintenance'),
      }}
      hasNoNodes={(nodes?.length ?? 0) === 0}
      jdkOptions={jdkOptions}
      groupOptions={groupOptions}
      templates={templateOptions}
      onNodeChange={setNodeId}
      onProcessTypeChange={setProcessType}
      dockerCheck={{
        fetching: dockerCheck.isFetching,
        available: dockerCheck.data?.available,
        version: dockerCheck.data?.version,
        error: dockerCheck.data?.error,
        blocked: dockerCheck.isError || (dockerCheck.data != null && !dockerCheck.data.available),
      }}
      onCreate={async (payload) => {
        await create.mutateAsync(payload)
        qc.invalidateQueries({ queryKey: ['instances'] })
      }}
      onCancel={() => navigate('/instances')}
      notify={(kind, message) => (kind === 'success' ? toast.success(message) : toast.error(message))}
    />
  )
}

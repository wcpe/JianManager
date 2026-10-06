import { useMutation, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'

import { TemplatesPage as TemplatesView } from '@jianmanager/ui'
import type { ComboboxOption } from '@jianmanager/ui/components/combobox'
import api from '@/api/client'
import { useTemplates, useCreateTemplate, useDeleteTemplate } from '@/api/templates'
import { useNodes } from '@/api/nodes'
import { useGroups } from '@/api/groups'

/**
 * 服务端模板页接线层（ADR-097）：取数（模板/节点/分组）、三个写动作与缓存失效留在这里，
 * 视图（包内 `TemplatesPage`）只做受控渲染。
 */
export default function TemplatesPage() {
  const qc = useQueryClient()
  const { data: templates, isLoading } = useTemplates()
  const { data: nodes } = useNodes()
  const { data: groups } = useGroups()

  const createTemplate = useCreateTemplate()
  const deleteTemplate = useDeleteTemplate()

  // 「用此模板建实例」与 CreateInstanceDialog 同流：直接 POST /instances。
  const createInstance = useMutation({
    mutationFn: (body: Record<string, unknown>) => api.post('/instances', body),
  })

  // 只列在线节点（与既有行为一致：离线节点无法立即落盘启动）。
  const nodeOptions: ComboboxOption[] = (nodes ?? [])
    .filter((n) => n.status === 1)
    .map((n) => ({ value: String(n.id), label: n.name }))
  const groupOptions: ComboboxOption[] = (groups ?? []).map((g) => ({ value: String(g.id), label: g.name }))

  return (
    <TemplatesView
      templates={templates}
      isLoading={isLoading}
      nodeOptions={nodeOptions}
      groupOptions={groupOptions}
      onDelete={async (id) => {
        await deleteTemplate.mutateAsync(id)
      }}
      onCreate={async (payload) => {
        await createTemplate.mutateAsync(payload)
      }}
      onApply={async (payload) => {
        await createInstance.mutateAsync(payload)
        qc.invalidateQueries({ queryKey: ['instances'] })
      }}
      notify={(kind, message) => (kind === 'success' ? toast.success(message) : toast.error(message))}
    />
  )
}

import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import api from '@/api/client'
import { useNodes } from '@/api/nodes'
import { useGroups } from '@/api/groups'
import { useTemplates } from '@/api/templates'
import { useNodeJDKs } from '@/api/jdks'
import type { ComboboxOption } from '@jianmanager/ui/components/combobox'
import CreateInstanceDialogView from '@jianmanager/ui/components/views/CreateInstanceDialog'

/**
 * 新建实例对话框的应用接线层（ADR-097 b 范式）。
 *
 * 对话框本体已迁入组件库并受控；本层取四份候选（启用态节点 / 该节点 JDK / 用户组 / 模板）、
 * 发创建请求并失效实例列表。保留同路径的默认导出与同一套 props，调用点无需改动。
 */
export default function CreateInstanceDialog({
  open,
  onClose,
}: {
  open: boolean
  onClose: () => void
}) {
  const qc = useQueryClient()
  const { data: nodes } = useNodes()
  const { data: groups } = useGroups()
  const { data: templates } = useTemplates()
  // JDK 候选随选中节点变（未选节点时不查）。
  const [nodeId, setNodeId] = useState('')
  const { data: jdks } = useNodeJDKs(nodeId ? Number(nodeId) : 0)

  const create = useMutation({
    mutationFn: (body: Record<string, unknown>) => api.post('/instances', body),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['instances'] })
    },
  })

  // 系统可获取项 → 可编辑/可搜索下拉选项（FR-072）。ID 绑定项关闭自定义，字符串项允许自定义。
  const nodeOptions: ComboboxOption[] = (nodes ?? [])
    .filter((n) => n.status === 1)
    .map((n) => ({ value: String(n.id), label: n.name }))
  const jdkOptions: ComboboxOption[] = (jdks ?? []).map((j) => ({
    value: String(j.id),
    label: `${j.vendor} ${j.majorVersion} (${j.version})`,
  }))
  const groupOptions: ComboboxOption[] = (groups ?? []).map((g) => ({ value: String(g.id), label: g.name }))

  return (
    <CreateInstanceDialogView
      open={open}
      onClose={onClose}
      nodeOptions={nodeOptions}
      jdkOptions={jdkOptions}
      groupOptions={groupOptions}
      templates={(templates ?? []).map((tpl) => ({
        id: tpl.id,
        name: tpl.name,
        startCommand: tpl.startCommand,
        type: tpl.type,
        defaultWorkDir: tpl.defaultWorkDir,
      }))}
      onNodeChange={setNodeId}
      onCreate={(payload) => create.mutateAsync(payload).then(() => undefined)}
      notify={(kind, message) => {
        if (kind === 'success') toast.success(message)
        else toast.error(message)
      }}
    />
  )
}

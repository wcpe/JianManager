// 视图已迁至 @jianmanager/ui（ADR-097）；本层只做候选取数、搭建写请求、提示文案与 secret 复制回执。
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useNodes } from '@/api/nodes'
import { useGroups } from '@/api/groups'
import { useNodeJDKs } from '@/api/jdks'
import { useCoreVersions, useResolvedCore } from '@/api/provision'
import { useProvisionProxy } from '@/api/proxy'
import type { ComboboxOption } from '@jianmanager/ui/components/combobox'
import ProvisionProxyDialogView, {
  PROVISION_PROXY_QUERY_INIT,
  needsProxyVersion,
} from '@/components/views/provision/ProvisionProxyDialogView'

/** 搭建代理端点的错误形态（服务端 message）。 */
type ProvisionProxyError = Error & { response?: { data?: { message?: string } } }

interface ProvisionProxyDialogProps {
  open: boolean
  onClose: () => void
}

/**
 * 搭建代理向导接线层（ADR-097 b 范式，原 FR-035 搭建代理 / FR-072 系统可获取项）。
 *
 * 视图本体已迁入组件库并受控（不取数、不发请求、不弹 toast）；本层负责：
 * - 候选取数：`useNodes()` 过滤启用态后映射节点选项、`useGroups()` 映射用户组选项、
 *   `useNodeJDKs(nodeId)` 注入 JDK 记录、`useCoreVersions(proxyType)` 注入版本列表（bungeecord 仅 latest，不下发）；
 * - 核心解析：`useResolvedCore(proxyType, effectiveVersion, 0)` 注入预览（bungeecord 折算为 latest）；
 * - 「草稿 → 请求体」组装：Number 化、空值省略、`jvmArgs` 空白拆分为数组、bungeecord 不下发 version；
 * - 一次写请求（POST /instances/provision/proxy）与错误文案；成功文案与逐条告警提示；
 *   成功回带的 forwarding secret 交给视图展示留存（视图据此切步骤而非关窗）。
 *
 * 保留同路径默认导出与同名 props，调用点（InstancesPage）无需改动。
 */
export default function ProvisionProxyDialog({ open, onClose }: ProvisionProxyDialogProps) {
  const { t } = useTranslation()
  const { data: nodes } = useNodes()
  const { data: groups } = useGroups()
  // 取数查询键由视图上报（表单草稿留在视图内），本层只保存查询键本身。
  const [query, setQuery] = useState(PROVISION_PROXY_QUERY_INIT)

  const { data: jdks } = useNodeJDKs(query.nodeId ? Number(query.nodeId) : 0)
  const needsVersion = needsProxyVersion(query.proxyType)
  const { data: versions, isLoading: versionsLoading } = useCoreVersions(
    open && needsVersion ? query.proxyType : '',
  )
  const effectiveVersion = needsVersion ? query.version : 'latest'
  const { data: resolved } = useResolvedCore(open ? query.proxyType : '', effectiveVersion, 0)

  const provision = useProvisionProxy()

  // 系统可获取项 → 下拉选项（FR-072）。代理类型/版本允许自定义。
  const nodeOptions: ComboboxOption[] = (nodes ?? [])
    .filter((n) => n.status === 1)
    .map((n) => ({ value: String(n.id), label: n.name }))
  const groupOptions: ComboboxOption[] = (groups ?? []).map((g) => ({ value: String(g.id), label: g.name }))

  return (
    <ProvisionProxyDialogView
      open={open}
      nodeOptions={nodeOptions}
      versions={versions ?? []}
      versionsLoading={versionsLoading}
      jdks={jdks}
      resolved={resolved}
      groupOptions={groupOptions}
      submitting={provision.isPending}
      onQueryChange={setQuery}
      onClose={onClose}
      onCopyResult={(ok) => {
        if (ok) toast.success(t('proxy.secretCopied'))
        else toast.error(t('proxy.secretCopyFailed'))
      }}
      onSubmit={async (draft) => {
        const args = draft.jvmArgs.trim() ? draft.jvmArgs.trim().split(/\s+/).filter(Boolean) : undefined
        try {
          const res = await provision.mutateAsync({
            nodeId: Number(draft.nodeId),
            name: draft.name,
            proxyType: draft.proxyType,
            version: needsProxyVersion(draft.proxyType) ? draft.version : undefined,
            jdkId: draft.jdkId ? Number(draft.jdkId) : undefined,
            memoryMb: draft.memoryMb ? Number(draft.memoryMb) : undefined,
            jvmArgs: args,
            groupId: draft.groupId ? Number(draft.groupId) : undefined,
            onlineMode: draft.onlineMode,
          })
          toast.success(t('proxy.submitted', '代理搭建任务已提交，进度见任务中心'))
          ;(res.warnings || []).forEach((w) => toast.warning(w))
          // 有 secret 时视图切到留存步骤（不关窗），无 secret 时视图自行关窗。
          return { forwardingSecret: res.forwardingSecret }
        } catch (err) {
          toast.error((err as ProvisionProxyError).response?.data?.message || t('proxy.failed'))
          return null
        }
      }}
    />
  )
}

// 视图已迁至 @jianmanager/ui（ADR-097）；本层只做候选取数、核心解析、搭建写请求与提示文案。
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router'
import { toast } from 'sonner'
import { useNodes } from '@/api/nodes'
import { useGroups } from '@/api/groups'
import { useNodeJDKs } from '@/api/jdks'
import { useCoreVersions, useResolvedCore, useProvisionServer } from '@/api/provision'
import type { ComboboxOption } from '@jianmanager/ui/components/combobox'
import ProvisionServerDialogView, {
  PROVISION_SERVER_QUERY_INIT,
} from '@/components/views/provision/ProvisionServerDialogView'

/** 搭建端点的错误形态（服务端 message + 部分失败时回带的 instance）。 */
type ProvisionError = Error & { response?: { data?: { message?: string; instance?: unknown } } }

interface ProvisionServerDialogProps {
  open: boolean
  onClose: () => void
}

/**
 * 一键搭建后端子服向导接线层（ADR-097 b 范式，原 FR-034 一键搭建 / FR-046 核心解析 / FR-316 版本-JDK 预检）。
 *
 * 视图本体已迁入组件库并受控（不取数、不发请求、不弹 toast）；本层负责：
 * - 候选取数：`useNodes()` 过滤启用态后映射节点选项、`useGroups()` 映射用户组选项、
 *   `useNodeJDKs(nodeId)` 注入 JDK 记录（未取到时为 undefined，视图据此跳过无 JDK 阻断）、
 *   `useCoreVersions(coreType)` 注入版本列表；
 * - 核心解析：`useResolvedCore(coreType, mcVersion, build)` 注入预览与所需 Java 大版本（FR-316），
 *   查询以「对话框开着」为前提（关闭时不下发，与原实现一致）；
 * - 「草稿 → 请求体」组装：Number 化、空值省略、`jvmArgs` 空白拆分为数组、build 非正数取最新；
 * - 一次写请求（POST /instances/provision/server）与错误文案：部分失败（已建实例但下载/写配置未完成）
 *   仍关窗并提示去任务中心重试，其余取服务端 message。
 *
 * 保留同路径默认导出与同名 props，调用点（InstancesPage）无需改动。
 */
export default function ProvisionServerDialog({ open, onClose }: ProvisionServerDialogProps) {
  const { t } = useTranslation()
  const { data: nodes } = useNodes()
  const { data: groups } = useGroups()
  // 取数查询键由视图上报（表单草稿留在视图内），本层只保存查询键本身。
  const [query, setQuery] = useState(PROVISION_SERVER_QUERY_INIT)

  const { data: jdks } = useNodeJDKs(query.nodeId ? Number(query.nodeId) : 0)
  const { data: versions, isLoading: versionsLoading, isError: versionsError } = useCoreVersions(
    open ? query.coreType : '',
  )
  const buildNum = query.build.trim() && Number.isFinite(Number(query.build)) ? Number(query.build) : 0
  const { data: resolved, isFetching: resolving } = useResolvedCore(
    open ? query.coreType : '',
    query.mcVersion,
    buildNum,
  )

  const provision = useProvisionServer()

  // 系统可获取项 → 下拉选项（FR-072）。版本允许自定义（PaperMC 列表外的版本）。
  const nodeOptions: ComboboxOption[] = (nodes ?? [])
    .filter((n) => n.status === 1)
    .map((n) => ({ value: String(n.id), label: n.name }))
  const groupOptions: ComboboxOption[] = (groups ?? []).map((g) => ({ value: String(g.id), label: g.name }))

  return (
    <ProvisionServerDialogView
      open={open}
      nodeOptions={nodeOptions}
      versions={versions ?? []}
      versionsLoading={versionsLoading}
      versionsError={versionsError}
      jdks={jdks}
      resolved={resolved}
      resolving={resolving}
      groupOptions={groupOptions}
      submitting={provision.isPending}
      onQueryChange={setQuery}
      onClose={onClose}
      renderLink={({ to, className, children }) => (
        <Link to={to} className={className}>{children}</Link>
      )}
      onSubmit={async (draft) => {
        const args = draft.jvmArgs.trim() ? draft.jvmArgs.trim().split(/\s+/).filter(Boolean) : undefined
        const draftBuild = draft.build.trim() && Number.isFinite(Number(draft.build)) ? Number(draft.build) : 0
        try {
          await provision.mutateAsync({
            nodeId: Number(draft.nodeId),
            name: draft.name,
            coreType: draft.coreType,
            mcVersion: draft.mcVersion,
            build: draftBuild > 0 ? draftBuild : undefined,
            jdkId: draft.jdkId ? Number(draft.jdkId) : undefined,
            memoryMb: draft.memoryMb ? Number(draft.memoryMb) : undefined,
            jvmArgs: args,
            groupId: draft.groupId ? Number(draft.groupId) : undefined,
            onlineMode: draft.onlineMode,
          })
          // FR-319 异步化：实例已建，核心下载在后台任务推进（慢源可能数分钟），进度看任务中心。
          toast.success(t('provision.submitted'))
          return true
        } catch (err) {
          const data = (err as ProvisionError).response?.data
          // 部分失败：实例已建但核心下载/配置写入未完成，仍关闭并提示用户去重试。
          if (data?.instance) {
            toast.warning(t('provision.partialFailure'))
            return true
          }
          toast.error(data?.message || t('provision.failed'))
          return false
        }
      }}
    />
  )
}

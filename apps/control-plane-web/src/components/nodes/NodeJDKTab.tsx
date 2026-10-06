import { useCallback, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import {
  useNodeJDKs,
  useCreateJDK,
  useDeleteJDK,
  useInstallJDK,
  useProbeJDK,
  useUpdateJDK,
} from '@/api/jdks'
import { useJDKCatalog } from '@/api/nodeRuntime'
import NodeJDKPanel from '@jianmanager/ui/components/views/nodes/NodeJDKPanel'
import { PingNodeButton } from '@/components/PingNodeButton'
import DirectoryPicker from '@/components/DirectoryPicker'
import NodeRuntimeTab from '@/components/nodes/NodeRuntimeTab'

/** 从 mutation 错误里取后端消息，缺省回落到兜底文案。 */
function errMessage(err: unknown, fallback: string): string {
  const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
  return msg || fallback
}

/**
 * 节点 JDK 管理面板外壳（ADR-097 b 范式）。
 *
 * 五个 mutation、目录查询与全部提示文案都在这里。三处子组件经注入进视图：
 * 存活测试按钮与目录选择器各自取数、运行时库自成一块——视图不必认识它们。
 *
 * foojay 目录的查询键（厂商 + 大版本）是视图内的表单草稿，故由视图经 `onCatalogQuery`
 * 上报；本层用幂等 setState 吸收重复上报，避免视图 effect 引发重渲染循环。
 */
export default function NodeJDKTab({
  nodeId,
  active = true,
}: {
  nodeId: number
  active?: boolean
}) {
  const { t } = useTranslation()
  const { data: jdks, isLoading, isFetching } = useNodeJDKs(nodeId)
  const create = useCreateJDK(nodeId)
  const del = useDeleteJDK(nodeId)
  const install = useInstallJDK(nodeId)
  const probe = useProbeJDK(nodeId)
  const update = useUpdateJDK(nodeId)

  const [catalogKey, setCatalogKey] = useState({ vendor: 'Temurin', major: 21, open: false })
  const handleCatalogQuery = useCallback((vendor: string, major: number, open: boolean) => {
    setCatalogKey((prev) =>
      prev.vendor === vendor && prev.major === major && prev.open === open ? prev : { vendor, major, open },
    )
  }, [])

  // foojay 版本目录：仅在「一键下载」分段、且厂商与大版本都有效时查询。
  const catalog = useJDKCatalog(nodeId, catalogKey.vendor, catalogKey.major, {
    enabled: active && catalogKey.open && catalogKey.major > 0,
  })

  return (
    <NodeJDKPanel
      jdks={jdks}
      isLoading={isLoading}
      isFetching={isFetching}
      catalogLoading={catalog.isLoading}
      // foojay 不可达/无结果 → 视图降级为手填具体版本（仍可下载）。
      catalogUnavailable={catalog.isError || !catalog.data || catalog.data.length === 0}
      catalogVersions={catalog.data ?? []}
      installing={install.isPending}
      creating={create.isPending}
      probing={probe.isPending}
      saving={update.isPending}
      onCatalogQuery={handleCatalogQuery}
      pingSlot={<PingNodeButton nodeId={nodeId} />}
      renderDirectoryPicker={({ onPick, onCancel }) => (
        <DirectoryPicker nodeId={nodeId} onPick={onPick} onCancel={onCancel} />
      )}
      runtimeSlot={<NodeRuntimeTab nodeId={nodeId} active={active} />}
      onInstall={async (body) => {
        try {
          await install.mutateAsync(body)
          toast.success(t('artifactCache.jdkInstallDispatched'))
          return true
        } catch (err) {
          toast.error(errMessage(err, t('nodes.jdkInstallFailed')))
          return false
        }
      }}
      onRegister={async (body) => {
        try {
          await create.mutateAsync(body)
          toast.success(t('nodes.jdkRegistered'))
          return true
        } catch (err) {
          toast.error(errMessage(err, t('nodes.jdkRegisterFailed')))
          return false
        }
      }}
      onProbe={async (path) => {
        try {
          return await probe.mutateAsync(path)
        } catch (err) {
          toast.error(errMessage(err, t('nodes.jdkProbeFailed', '探测失败')))
          return null
        }
      }}
      onUpdate={async (jdkId, body) => {
        try {
          await update.mutateAsync({ jdkId, body })
          toast.success(t('nodes.jdkEditSaved', '已保存 JDK 登记信息'))
          return true
        } catch (err) {
          toast.error(errMessage(err, t('nodes.jdkEditFailed', '保存失败')))
          return false
        }
      }}
      onDelete={async (jdk) => {
        try {
          await del.mutateAsync(jdk.id)
          toast.success(t('nodes.jdkDeleted'))
          return true
        } catch (err) {
          // 托管 JDK 被实例占用：后端回传 instances 列表，按名提示比原始消息可读。
          const insts = (err as { response?: { data?: { instances?: { name: string }[] } } })?.response?.data?.instances
          if (insts && insts.length > 0) {
            toast.error(t('nodes.jdkInUse', { names: insts.map((i) => i.name).join(', ') }))
          } else {
            toast.error(errMessage(err, t('nodes.jdkDeleteFailed')))
          }
          return false
        }
      }}
      onCopyResult={(ok) => {
        if (ok) toast.success(t('artifactCache.pathCopied'))
        else toast.error(t('common.copyFailed'))
      }}
    />
  )
}

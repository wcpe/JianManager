import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useNodeRuntimes, useScanRuntimes, useRegisterRuntime, useDeleteRuntime, useInstallRuntime } from '@/api/runtimes'
import NodeRuntimeSection from '@/components/views/nodes/NodeRuntimeSection'
import NodePMConfigSection from '@/components/NodePMConfigSection'
import NodeGlobalPackagesTab from '@/components/nodes/NodeGlobalPackagesTab'

/** 从 mutation 错误里取后端消息，缺省回落到兜底文案。 */
function errMessage(err: unknown, fallback: string): string {
  const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
  return msg || fallback
}

/**
 * 节点运行时库分区外壳（ADR-097 b 范式）。
 *
 * 五个动作（扫描 / 登记 / 删除 / 安装 / 复制）与它们各自的提示文案都在这里。
 * 登记是逐条 POST 且允许部分失败，因此由本层累加成功数并把每条失败单独提示，
 * 视图只拿「成功了几条」来决定是否关闭模态。
 *
 * 尾部的两块（包管理器配置、全局包管理）各自取数、不属运行时库，故经 `footer`
 * 槽注入而非让视图认识它们——否则受控视图就要反向依赖应用层组件。
 */
export default function NodeRuntimeTab({
  nodeId,
  active = true,
}: {
  nodeId: number
  active?: boolean
}) {
  const { t } = useTranslation()
  const { data: runtimes, isLoading } = useNodeRuntimes(nodeId, { enabled: active })
  const scan = useScanRuntimes(nodeId)
  const register = useRegisterRuntime(nodeId)
  const del = useDeleteRuntime(nodeId)
  const install = useInstallRuntime(nodeId)

  return (
    <NodeRuntimeSection
      runtimes={runtimes}
      isLoading={isLoading}
      onScan={async () => {
        try {
          return await scan.mutateAsync(undefined)
        } catch (err) {
          toast.error(errMessage(err, t('nodes.runtimeLib.scanFailed')))
          return null
        }
      }}
      onRegister={async (picked) => {
        let okCount = 0
        for (const c of picked) {
          try {
            // type=jdk 转发现有 JDK 登记链路（带 vendor、无名），其它落 node_runtimes（名由 vendor+大版本拼）。
            await register.mutateAsync({
              type: c.type,
              vendor: c.type === 'jdk' ? c.vendor : undefined,
              name: c.type === 'jdk' ? undefined : `${c.vendor} ${c.majorVersion}`,
              majorVersion: c.majorVersion,
              version: c.version,
              arch: c.arch,
              path: c.path,
            })
            okCount++
          } catch (err) {
            // 逐条失败单独提示（带路径），不打断其余候选的登记。
            toast.error(`${c.path}: ${errMessage(err, t('nodes.runtimeLib.registerFailed'))}`)
          }
        }
        if (okCount > 0) toast.success(t('nodes.runtimeLib.registerSuccess', { count: okCount }))
        return okCount
      }}
      onDelete={async (item) => {
        try {
          await del.mutateAsync({ id: item.id, type: item.type })
          toast.success(t('nodes.runtimeLib.deleted'))
          return true
        } catch (err) {
          // 托管 JDK 被实例占用：后端回传 instances 列表，按名提示比原始消息可读。
          const insts = (err as { response?: { data?: { instances?: { name: string }[] } } })?.response?.data?.instances
          if (insts && insts.length > 0) {
            toast.error(t('nodes.jdkInUse', { names: insts.map((i) => i.name).join(', ') }))
          } else {
            toast.error(errMessage(err, t('nodes.runtimeLib.deleteFailed')))
          }
          return false
        }
      }}
      onInstall={async (major) => {
        try {
          await install.mutateAsync({ type: 'nodejs', major })
          toast.success(t('nodes.runtimeLib.installDispatched'))
          return true
        } catch (err) {
          toast.error(errMessage(err, t('nodes.runtimeLib.installFailed')))
          return false
        }
      }}
      onCopyResult={(ok) => {
        if (ok) toast.success(t('artifactCache.pathCopied'))
        else toast.error(t('common.copyFailed'))
      }}
      footer={
        <>
          {/* 包管理器与 registry 配置（FR-306） */}
          <NodePMConfigSection nodeId={nodeId} active={active} />
          {/* 全局包管理（FR-307）：托管全局目录已装包 列表/搜索/安装/升级/卸载。 */}
          <NodeGlobalPackagesTab nodeId={nodeId} active={active} />
        </>
      }
    />
  )
}

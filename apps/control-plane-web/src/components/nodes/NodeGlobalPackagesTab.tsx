import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import {
  useGlobalPackages,
  useInstallGlobalPackage,
  useRemoveGlobalPackage,
} from '@/api/pmConfig'
import NodeGlobalPackagesSection from '@/components/views/nodes/NodeGlobalPackagesSection'

/** 从 mutation 错误里取后端消息，缺省回落到兜底文案。 */
function errMessage(err: unknown, fallback: string): string {
  const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
  return msg || fallback
}

/**
 * 节点全局包管理外壳（ADR-097 b 范式）。
 *
 * 取数、刷新、安装/升级、卸载四个动作全在这里——它们各自要弹什么提示、失败如何
 * 取后端消息，都是应用侧策略。受控视图只上报「用户想装/想卸哪个包」。
 *
 * `active` 沿用原语义：分段未展开时不发请求（避免后台轮询离屏节点）。
 */
export default function NodeGlobalPackagesTab({
  nodeId,
  active = true,
}: {
  nodeId: number
  active?: boolean
}) {
  const { t } = useTranslation()
  const { data, isLoading, isFetching, refetch, error } = useGlobalPackages(nodeId, { enabled: active })
  const install = useInstallGlobalPackage(nodeId)
  const removePkg = useRemoveGlobalPackage(nodeId)

  if (!active) return null

  return (
    <NodeGlobalPackagesSection
      data={data}
      isLoading={isLoading}
      isFetching={isFetching}
      errorMessage={
        error
          ? errMessage(error, t('pkg.listFailed', '获取全局包失败'))
          : undefined
      }
      installing={install.isPending}
      onRefresh={() => void refetch()}
      onInstall={async (name, version) => {
        try {
          await install.mutateAsync({ name: name.trim(), version: version?.trim() || undefined })
          toast.success(t('pkg.installSubmitted', '安装任务已提交，进度见任务中心'))
          return true
        } catch (err) {
          toast.error(errMessage(err, t('pkg.installFailed', '提交安装失败')))
          return false
        }
      }}
      onRemove={async (name) => {
        try {
          await removePkg.mutateAsync(name)
          toast.success(t('pkg.removed', '已卸载'))
          return true
        } catch (err) {
          toast.error(errMessage(err, t('pkg.removeFailed', '卸载失败')))
          return false
        }
      }}
    />
  )
}

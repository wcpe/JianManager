import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import {
  useArtifactCache,
  useEvictArtifactCache,
  useClearArtifactCache,
  useSetArtifactCacheCap,
} from '@/api/nodeRuntime'
import { describeCap } from '@jianmanager/ui/lib/artifact-cache'
import NodeArtifactCachePanel from '@/components/views/nodes/NodeArtifactCachePanel'

/** 从 mutation 错误里取后端消息，缺省回落到兜底文案。 */
function errMessage(err: unknown, fallback: string): string {
  const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
  return msg || fallback
}

/**
 * 节点制品缓存页签外壳（ADR-097 b 范式）。
 *
 * 写操作的全部决策都在这里：何时发请求、成功提示什么、失败如何取后端消息。
 * 受控视图只管把用户动作上报上来（`onEvict` / `onClear` / `onSaveCap`），
 * 并按回调返回值决定是否复位本地编辑态——它不知道也不关心请求去了哪里。
 */
export default function NodeArtifactCacheTab({ nodeId }: { nodeId: number }) {
  const { t } = useTranslation()
  const { data, isLoading, isError } = useArtifactCache(nodeId, { enabled: true })
  const evict = useEvictArtifactCache(nodeId)
  const clear = useClearArtifactCache(nodeId)
  const setCap = useSetArtifactCacheCap(nodeId)

  return (
    <NodeArtifactCachePanel
      data={data}
      isLoading={isLoading}
      isError={isError}
      capSaving={setCap.isPending}
      clearing={clear.isPending}
      onSaveCap={async (bytes) => {
        try {
          await setCap.mutateAsync(bytes)
          toast.success(t('artifactCache.capSaved', { cap: describeCap(bytes) }))
          return true
        } catch (err) {
          toast.error(errMessage(err, t('artifactCache.capFailed')))
          return false
        }
      }}
      onEvict={async (sha) => {
        try {
          await evict.mutateAsync(sha)
          toast.success(t('artifactCache.evicted'))
          return true
        } catch (err) {
          toast.error(errMessage(err, t('artifactCache.evictFailed')))
          return false
        }
      }}
      onClear={async () => {
        try {
          await clear.mutateAsync()
          toast.success(t('artifactCache.cleared'))
          return true
        } catch (err) {
          toast.error(errMessage(err, t('artifactCache.clearFailed')))
          return false
        }
      }}
      onCopyResult={(ok) => {
        if (ok) toast.success(t('artifactCache.shaCopied'))
        else toast.error(t('common.copyFailed'))
      }}
    />
  )
}

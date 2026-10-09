// 视图已迁至 @jianmanager/ui（ADR-097）；本层只做目录查询与四个写动作（含提示文案）的接线。
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import {
  useCacheServerProbeVersion,
  useServerProbeCatalog,
  useSetGlobalProbeVersion,
  useSyncServerProbeSource,
  useUploadServerProbeVersion,
} from '@/api/artifactVersions'
import { ArtifactVersionsPageView } from '@/components/views/artifacts/ArtifactVersionsPageView'

/** 从 mutation 错误里取后端消息，缺省回落到兜底文案。 */
function errMessage(err: unknown, fallback: string): string {
  const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
  return msg || fallback
}

/**
 * ServerProbe 制品版本库（FR-409 线上来源 / FR-411 本地上传）容器：目录查询与
 * 同步、上传、缓存、设全局默认四个 mutation（何时发请求、成功提示什么、失败如何取后端消息）
 * 全部留在此处；列表与上传表单交共享视图，它只上报用户动作并按返回值决定是否复位表单。
 * 保留同路径默认导出，路由表无需改动。
 */
export default function ArtifactVersionsPage() {
  const { t } = useTranslation()
  const { data: catalog, isLoading, isError } = useServerProbeCatalog()
  const sync = useSyncServerProbeSource()
  const upload = useUploadServerProbeVersion()
  const cache = useCacheServerProbeVersion()
  const setDefault = useSetGlobalProbeVersion()

  return (
    <ArtifactVersionsPageView
      catalog={catalog}
      isLoading={isLoading}
      isError={isError}
      syncPending={sync.isPending}
      uploadPending={upload.isPending}
      cachePending={cache.isPending}
      defaultPending={setDefault.isPending}
      onSync={(sourceId) => {
        sync.mutate(sourceId, {
          onSuccess: (result) => toast.success(t('artifactVersions.synced', { count: result.created })),
          onError: (error) => toast.error(errMessage(error, t('artifactVersions.actionFailed'))),
        })
      }}
      onUpload={async ({ version, file }) => {
        try {
          await upload.mutateAsync({ version, file })
          toast.success(t('artifactVersions.uploadSucceeded'))
          return true
        } catch (error) {
          toast.error(errMessage(error, t('artifactVersions.actionFailed')))
          return false
        }
      }}
      onCache={(versionId) => {
        cache.mutate(versionId, {
          onSuccess: () => toast.success(t('artifactVersions.cacheSucceeded')),
          onError: (error) => toast.error(errMessage(error, t('artifactVersions.actionFailed'))),
        })
      }}
      onSetGlobalDefault={(versionId) => {
        setDefault.mutate(versionId, {
          onSuccess: () => toast.success(t('artifactVersions.defaultSaved')),
          onError: (error) => toast.error(errMessage(error, t('artifactVersions.actionFailed'))),
        })
      }}
    />
  )
}

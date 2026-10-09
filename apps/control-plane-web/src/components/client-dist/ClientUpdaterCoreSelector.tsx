// 视图已回迁应用侧（原 ADR-097 迁包已撤销）；本层只做版本列表取数、切换/上传写请求与角色门禁注入。
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { ClientUpdaterCoreSelectorView } from '@/components/views/client-dist/ClientUpdaterCoreSelectorView'
import type { ClientUpdaterCoreUploadOutcome, ClientUpdaterCoreUploadPayload } from '@/components/views/client-dist/ClientUpdaterCoreSelectorView'
import { useUpdaterCoreVersions, useSelectUpdaterCore, useUploadUpdaterCore } from '@/api/clientChannels'
import { useDangerPermission } from '@/lib/shared/danger'

type ErrResp = { response?: { data?: { message?: string } } }
const errMsg = (e: unknown, fallback: string) => (e as ErrResp)?.response?.data?.message || fallback

/**
 * updater-core 版本选择器（FR-259）的接线层（ADR-097）。频道工作台「Core 版本」tab：
 * 列出所有归档 core 版本，当前选定版本高亮，一键切换实现回滚。切换后提示"客户端下次启动生效"。
 *
 * 展示层已回迁应用侧（`ClientUpdaterCoreSelectorView`），此处只保留应用侧职责：
 * - 取数：版本列表（`useUpdaterCoreVersions`）与加载态注入视图；
 * - 切换（`useSelectUpdaterCore`）：视图确认弹窗通过后落 PUT，成功/失败提示在此发；
 * - 上传（`useUploadUpdaterCore`）：视图交来弹窗内的表单草稿，这里发 POST 并把「是否成功」以
 *   `{ ok }` 回执（不抛出）返回，视图据回执决定清空关窗还是保留表单重试；
 * - 角色门禁：`useDangerPermission('platform')` 的结果注入视图，切换确认弹窗的 scope/allowed 语义不变。
 * 保留原路径与原默认导出、原 props 签名，调用点（频道工作台「Core 版本」Tab）零改动。
 */
export default function ClientUpdaterCoreSelector({ channelId }: { channelId: string }) {
  const { t } = useTranslation()
  const { data: versions, isLoading } = useUpdaterCoreVersions(channelId)
  const select = useSelectUpdaterCore()
  const upload = useUploadUpdaterCore()
  const { allowed: dangerAllowed } = useDangerPermission('platform')

  /** 确认切换版本：视图已完成二次确认，这里落 PUT 并提示结果。 */
  const doSelect = (sha256: string) => {
    select.mutate(
      { channelId, sha256 },
      {
        onSuccess: () => toast.success(t('clientCore.switched', '已切换，客户端下次启动生效')),
        onError: (e) => toast.error(errMsg(e, t('clientCore.switchFailed', '切换失败'))),
      },
    )
  }

  /**
   * 提交上传：结果一律以 `{ ok }` 落定（不抛出），视图据此清空关窗或保留表单重试；
   * 成功文案带归档版本与 sha 前缀，失败文案优先取服务端消息。
   */
  const doUpload = async (payload: ClientUpdaterCoreUploadPayload): Promise<ClientUpdaterCoreUploadOutcome> => {
    try {
      const res = await upload.mutateAsync({
        channelId,
        file: payload.file,
        version: payload.version,
        select: payload.select,
      })
      toast.success(
        t('clientCore.uploaded', 'updater-core 已上传：v{{version}} {{sha}}', {
          version: res.version,
          sha: `${res.sha256.slice(0, 12)}…`,
        }),
      )
      return { ok: true }
    } catch (e) {
      toast.error(errMsg(e, t('clientCore.uploadFailed', '上传 updater-core 失败')))
      return { ok: false }
    }
  }

  return (
    <ClientUpdaterCoreSelectorView
      versions={versions}
      loading={isLoading}
      selecting={select.isPending}
      onSelect={doSelect}
      uploading={upload.isPending}
      onUpload={doUpload}
      dangerAllowed={dangerAllowed}
    />
  )
}

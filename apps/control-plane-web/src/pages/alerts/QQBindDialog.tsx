import { QQ_BIND_POLL_MS, fetchQQBindResult, isBindTaskExpiredError, useCreateQQBindTask } from '@/api/alerts'
import { QQBindDialogView } from '@/components/views/alerts/QQBindDialogView'
import type { QQBindFill } from '@/lib/alert-contracts'
import { QQBindQrCode } from './qq-badge'

// 实现已回迁应用侧（原 ADR-097 迁包已撤销），以下保留 re-export 维持既有导入路径。
export type { QQBindFill } from '@/lib/alert-contracts'
export { MAX_QQ_BIND_AUTO_REFRESH } from '@/components/views/alerts/QQBindDialogView'

interface QQBindDialogProps {
  /** 扫码成功且三要素齐全时回调（父表单据此回填并关闭本弹窗）。 */
  onBound: (fill: QQBindFill) => void
  onClose: () => void
}

/**
 * QQ 扫码绑定弹窗（取数容器）。
 *
 * 轮询状态机与渲染已回迁应用侧（原 ADR-097 迁包已撤销）；此处注入绑定任务的
 * 申请/查询接口、过期错误判定与二维码组件。
 */
export function QQBindDialog({ onBound, onClose }: QQBindDialogProps) {
  const create = useCreateQQBindTask()

  return (
    <QQBindDialogView
      onBound={onBound}
      onClose={onClose}
      createTask={() => create.mutateAsync()}
      fetchResult={fetchQQBindResult}
      isExpiredError={isBindTaskExpiredError}
      pollMs={QQ_BIND_POLL_MS}
      creating={create.isPending}
      renderQrCode={({ url, size }) => <QQBindQrCode url={url} size={size} />}
    />
  )
}

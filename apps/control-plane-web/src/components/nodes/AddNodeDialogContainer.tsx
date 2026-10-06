import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useIssueEnrollToken } from '@/api/nodes'
import AddNodeDialog from '@jianmanager/ui/components/views/nodes/AddNodeDialog'

/**
 * 「添加节点」对话框容器（ADR-097 b 范式）。
 *
 * 签发动作与它两处提示（签发失败、复制结果）都在这里。视图拿回调返回值决定
 * 「是停在表单态还是切到结果态」——一次性凭据只此一次，必须交回视图。
 */
export default function AddNodeDialogContainer({
  open,
  onClose,
}: {
  open: boolean
  onClose: () => void
}) {
  const { t } = useTranslation()
  const issue = useIssueEnrollToken()

  return (
    <AddNodeDialog
      open={open}
      onClose={onClose}
      issuing={issue.isPending}
      onIssue={async (req) => {
        try {
          return await issue.mutateAsync(req)
        } catch (err) {
          const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
          toast.error(msg || t('nodes.enroll.issueFailed', '签发失败'))
          return null
        }
      }}
      onCopyResult={(ok) => {
        if (ok) toast.success(t('nodes.enroll.copied', '已复制到剪贴板'))
        else toast.error(t('nodes.enroll.copyFailed', '复制失败，请手动选择复制'))
      }}
    />
  )
}

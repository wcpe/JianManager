import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Copy } from 'lucide-react'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { useIssueEnrollToken, type IssuedEnrollToken } from '@/api/nodes'

/** 「添加节点」向导对话框：签发一次性 enrollment token，展示一键安装命令供运维复制（FR-080）。 */
interface AddNodeDialogProps {
  /** 是否打开。 */
  open: boolean
  /** 关闭回调。 */
  onClose: () => void
}

/** 复制按钮：写剪贴板 + toast 反馈，复制失败提示手动选择。 */
function CopyButton({ text, label }: { text: string; label: string }) {
  const { t } = useTranslation()
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text)
      toast.success(t('nodes.enroll.copied', '已复制到剪贴板'))
    } catch {
      toast.error(t('nodes.enroll.copyFailed', '复制失败，请手动选择复制'))
    }
  }
  return (
    <Button type="button" variant="outline" size="sm" onClick={copy} className="shrink-0">
      <Copy className="size-4" /> {label}
    </Button>
  )
}

/** 一键命令展示块：标题 + 等宽命令 + 复制按钮。 */
function CommandBlock({ title, command }: { title: string; command: string }) {
  const { t } = useTranslation()
  return (
    <div className="space-y-1">
      <div className="text-xs font-medium text-muted-foreground">{title}</div>
      <div className="flex items-start gap-2 rounded-md border bg-muted/50 p-2">
        <code className="flex-1 break-all font-mono text-xs leading-relaxed">{command}</code>
        <CopyButton text={command} label={t('nodes.enroll.copy', '复制')} />
      </div>
    </div>
  )
}

export default function AddNodeDialog({ open, onClose }: AddNodeDialogProps) {
  const { t } = useTranslation()
  const issue = useIssueEnrollToken()
  const [nodeName, setNodeName] = useState('')
  const [ttlMinutes, setTtlMinutes] = useState(30)
  const [issued, setIssued] = useState<IssuedEnrollToken | null>(null)

  const reset = () => {
    setNodeName('')
    setTtlMinutes(30)
    setIssued(null)
  }

  const handleClose = () => {
    reset()
    onClose()
  }

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault()
    if (issue.isPending) return
    try {
      const res = await issue.mutateAsync({
        nodeName: nodeName.trim() || undefined,
        ttlMinutes,
      })
      setIssued(res)
    } catch (err) {
      const e2 = err as Error & { response?: { data?: { message?: string } } }
      toast.error(e2?.response?.data?.message || t('nodes.enroll.issueFailed', '签发失败'))
    }
  }

  return (
    <Dialog open={open} onOpenChange={(v: boolean) => { if (!v) handleClose() }}>
      <DialogContent className="max-w-2xl">
        {issued === null ? (
          <form onSubmit={onSubmit}>
            <DialogHeader>
              <DialogTitle>{t('nodes.enroll.addTitle', '添加节点')}</DialogTitle>
              <DialogDescription>
                {t('nodes.enroll.addDesc', '签发一次性安装凭据，在目标机器粘贴执行一键命令即可自动注册上线。')}
              </DialogDescription>
            </DialogHeader>
            <div className="space-y-3 py-2">
              <div className="space-y-1">
                <label className="text-sm font-medium">{t('nodes.enroll.nodeName', '节点名（可选）')}</label>
                <Input
                  value={nodeName}
                  onChange={(e) => setNodeName(e.target.value)}
                  placeholder={t('nodes.enroll.nodeNamePlaceholder', '留空则由 Worker 自动上报')}
                />
              </div>
              <div className="space-y-1">
                <label className="text-sm font-medium">{t('nodes.enroll.ttl', '有效期（分钟）')}</label>
                <Input
                  type="number"
                  min={1}
                  max={1440}
                  value={ttlMinutes}
                  onChange={(e) => setTtlMinutes(Number(e.target.value) || 30)}
                />
              </div>
            </div>
            <DialogFooter>
              <Button type="button" variant="outline" onClick={handleClose}>
                {t('common.cancel', '取消')}
              </Button>
              <Button type="submit" disabled={issue.isPending}>
                {t('nodes.enroll.generate', '生成一键命令')}
              </Button>
            </DialogFooter>
          </form>
        ) : (
          <>
            <DialogHeader>
              <DialogTitle>{t('nodes.enroll.resultTitle', '一键安装命令（仅此一次）')}</DialogTitle>
              <DialogDescription>
                {t('nodes.enroll.resultDesc', '请立即复制保存。凭据一次性且限时，关闭后无法再次查看完整命令。')}
              </DialogDescription>
            </DialogHeader>
            <div className="space-y-3 py-1">
              <div className="text-xs text-muted-foreground">
                {t('nodes.enroll.expiresAt', '过期时间')}: {new Date(issued.expiresAt).toLocaleString()}
                {issued.nodeName && <> · {t('nodes.enroll.nodeName', '节点名')}: {issued.nodeName}</>}
              </div>
              <CommandBlock title={t('nodes.enroll.linux', 'Linux / macOS')} command={issued.installCommandLinux} />
              <CommandBlock title={t('nodes.enroll.windows', 'Windows (PowerShell)')} command={issued.installCommandWindows} />
              <div className="rounded-md border border-status-warning/40 bg-status-warning/10 p-2 text-xs text-muted-foreground">
                {t('nodes.enroll.hint', '公网下载源未配置时，请先把 Worker 二进制拷到目标机器，并在命令末尾追加 --binary <路径>（Windows 用 -Binary <路径>）。')}
              </div>
            </div>
            <DialogFooter>
              <Button onClick={handleClose}>{t('common.close', '关闭')}</Button>
            </DialogFooter>
          </>
        )}
      </DialogContent>
    </Dialog>
  )
}

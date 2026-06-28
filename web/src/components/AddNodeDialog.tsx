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
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { scrollableDialogContentClass, ScrollableDialogBody } from '@/components/ui/scrollable-dialog'
import { copyToClipboard } from '@/lib/clipboard'
import { useIssueEnrollToken, type IssuedEnrollToken } from '@/api/nodes'

/** 「添加节点」向导对话框：签发一次性 enrollment token，提供「自动安装 / 手动连接」两条上线路径（FR-080 / FR-189）。 */
interface AddNodeDialogProps {
  /** 是否打开。 */
  open: boolean
  /** 关闭回调。 */
  onClose: () => void
}

/** 复制按钮：写剪贴板（兼容 HTTP 非安全上下文）+ toast 反馈（FR-189）。 */
function CopyButton({ text, label }: { text: string; label: string }) {
  const { t } = useTranslation()
  const copy = async () => {
    const ok = await copyToClipboard(text)
    if (ok) {
      toast.success(t('nodes.enroll.copied', '已复制到剪贴板'))
    } else {
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

/** 单条手动步骤：序号说明 + 等宽命令 + 复制按钮（分步兜底，便于先审脚本再执行 / 管道被拦时用）。 */
function ManualStep({ label, command }: { label: string; command: string }) {
  const { t } = useTranslation()
  return (
    <div className="space-y-1">
      <div className="text-xs text-muted-foreground">{label}</div>
      <div className="flex items-start gap-2 rounded-md border bg-muted/40 p-2">
        <code className="flex-1 break-all font-mono text-xs leading-relaxed">{command}</code>
        <CopyButton text={command} label={t('nodes.enroll.copy', '复制')} />
      </div>
    </div>
  )
}

/**
 * 手动安装步骤段：把一键命令拆成「下载脚本 → 执行（带参数）」分步兜底（FR-080）。
 * 适用 `curl … | sh` 管道被策略拦截、或运维想先审脚本再执行的场景。
 */
function ManualInstallSection({ issued }: { issued: IssuedEnrollToken }) {
  const { t } = useTranslation()
  const base = issued.scriptBaseUrl
  const grpc = issued.controlPlaneGrpc
  const token = issued.token
  const name = issued.nodeName

  const linuxDownload = `curl -fsSL ${base}/install-worker.sh -o install-worker.sh`
  const linuxRun = `sh install-worker.sh --control-plane ${grpc} --token ${token}${name ? ` --name ${name}` : ''}`
  const winDownload = `iwr ${base}/install-worker.ps1 -UseBasicParsing -OutFile install-worker.ps1`
  const winRun = `.\\install-worker.ps1 -ControlPlane ${grpc} -Token ${token}${name ? ` -Name ${name}` : ''}`

  return (
    <details className="rounded-md border bg-muted/20 p-2">
      <summary className="cursor-pointer text-xs font-medium text-muted-foreground">
        {t('nodes.enroll.manualTitle', '手动安装步骤（分步兜底 / 先审脚本）')}
      </summary>
      <div className="space-y-3 pt-2">
        <div className="space-y-2">
          <div className="text-xs font-medium">{t('nodes.enroll.linux', 'Linux / macOS')}</div>
          <ManualStep label={t('nodes.enroll.manualStep1', '① 下载脚本')} command={linuxDownload} />
          <ManualStep label={t('nodes.enroll.manualStep2', '② 执行（带参数）')} command={linuxRun} />
        </div>
        <div className="space-y-2">
          <div className="text-xs font-medium">{t('nodes.enroll.windows', 'Windows (PowerShell)')}</div>
          <ManualStep label={t('nodes.enroll.manualStep1', '① 下载脚本')} command={winDownload} />
          <ManualStep label={t('nodes.enroll.manualStep2', '② 执行（带参数）')} command={winRun} />
        </div>
      </div>
    </details>
  )
}

/** 自动安装 Tab：一键脚本（Linux/Windows）+ 手动分步兜底 + 公网源未配置提示。 */
function AutoInstallTab({ issued }: { issued: IssuedEnrollToken }) {
  const { t } = useTranslation()
  return (
    <div className="space-y-3">
      <p className="text-xs text-muted-foreground">{t('nodes.enroll.tabAutoDesc')}</p>
      <CommandBlock title={t('nodes.enroll.linux', 'Linux / macOS')} command={issued.installCommandLinux} />
      <CommandBlock title={t('nodes.enroll.windows', 'Windows (PowerShell)')} command={issued.installCommandWindows} />
      <ManualInstallSection issued={issued} />
      <div className="rounded-md border border-status-warning/40 bg-status-warning/10 p-2 text-xs text-muted-foreground">
        {t('nodes.enroll.hint', '公网下载源未配置时，请先把 Worker 二进制拷到目标机器，并在命令末尾追加 --binary <路径>（Windows 用 -Binary <路径>）。')}
      </div>
    </div>
  )
}

/**
 * 手动连接 Tab：面向「Worker 二进制已自行部署」的场景（FR-189）。
 * 展示 CP 地址 + 一次性 token + 启动命令，复用同一签发结果，不走安装脚本。
 */
function ManualConnectTab({ issued }: { issued: IssuedEnrollToken }) {
  const { t } = useTranslation()
  const grpc = issued.controlPlaneGrpc
  const token = issued.token
  const name = issued.nodeName

  // 与 install 脚本最终调起的 worker 参数一致：直接以 CP 地址 + token 启动即注册上线。
  const linuxRun = `worker --control-plane ${grpc} --token ${token}${name ? ` --name ${name}` : ''}`
  const winRun = `.\\worker.exe --control-plane ${grpc} --token ${token}${name ? ` --name ${name}` : ''}`

  return (
    <div className="space-y-3">
      <p className="text-xs text-muted-foreground">{t('nodes.enroll.tabManualDesc')}</p>
      <CommandBlock title={t('nodes.enroll.manualConnectStep1')} command={grpc} />
      <CommandBlock title={t('nodes.enroll.manualConnectStep2')} command={token} />
      <div className="space-y-2">
        <div className="text-xs font-medium text-muted-foreground">{t('nodes.enroll.manualConnectStep3')}</div>
        <CommandBlock title={t('nodes.enroll.linux', 'Linux / macOS')} command={linuxRun} />
        <CommandBlock title={t('nodes.enroll.windows', 'Windows (PowerShell)')} command={winRun} />
      </div>
      <div className="rounded-md border border-status-warning/40 bg-status-warning/10 p-2 text-xs text-muted-foreground">
        {t('nodes.enroll.manualConnectHint')}
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
      <DialogContent className={`${scrollableDialogContentClass} sm:max-w-2xl`}>
        {issued === null ? (
          <form onSubmit={onSubmit} className="flex min-h-0 flex-1 flex-col">
            <DialogHeader>
              <DialogTitle>{t('nodes.enroll.addTitle', '添加节点')}</DialogTitle>
              <DialogDescription>
                {t('nodes.enroll.addDesc', '签发一次性安装凭据，在目标机器粘贴执行一键命令即可自动注册上线。')}
              </DialogDescription>
            </DialogHeader>
            <ScrollableDialogBody className="space-y-3 py-2">
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
            </ScrollableDialogBody>
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
            <ScrollableDialogBody className="space-y-3 py-1">
              <div className="text-xs text-muted-foreground">
                {t('nodes.enroll.expiresAt', '过期时间')}: {new Date(issued.expiresAt).toLocaleString()}
                {issued.nodeName && <> · {t('nodes.enroll.nodeName', '节点名')}: {issued.nodeName}</>}
              </div>
              <Tabs defaultValue="auto" className="gap-3">
                <TabsList className="self-start">
                  <TabsTrigger value="auto">{t('nodes.enroll.tabAuto')}</TabsTrigger>
                  <TabsTrigger value="manual">{t('nodes.enroll.tabManual')}</TabsTrigger>
                </TabsList>
                <TabsContent value="auto">
                  <AutoInstallTab issued={issued} />
                </TabsContent>
                <TabsContent value="manual">
                  <ManualConnectTab issued={issued} />
                </TabsContent>
              </Tabs>
            </ScrollableDialogBody>
            <DialogFooter>
              <Button onClick={handleClose}>{t('common.close', '关闭')}</Button>
            </DialogFooter>
          </>
        )}
      </DialogContent>
    </Dialog>
  )
}

import { useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { CheckCircle2, FolderOpen, Loader2 } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { Badge } from '@jianmanager/ui/components/badge'
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@jianmanager/ui/components/dialog'
import DirectoryPicker from '@/components/nodes/DirectoryPicker'
import { useSubmitGuard } from '@/lib/hooks/use-submit-guard'
import { useProbeRuntime, type RegisterRuntimeBody, type RuntimeProbeResult } from '@/api/runtimes'

/**
 * 「使用本机已有运行时」登记段（JDK / Node.js 共用）。
 *
 * 【为什么需要一个探测步骤】后端的登记接口要求提供 version / majorVersion /
 * vendor / arch。让用户手填这些既容易错、也完全没必要——节点上跑一次
 * `java -XshowSettings:properties -version` 或 `node --version` 就是准确值。
 * 所以流程是：**选路径（或手输）→ 探测 → 回显探测结果 → 确认入库**。
 *
 * 【与「扫描发现」的分工】扫描只在 Worker 硬编码的常见目录里找，适合"我不知道装在哪"。
 * 本段处理另一半情况："我知道在哪，就是不在常见位置"（如 /opt/my-runtime）。
 *
 * 【探测失败不是错误弹窗】valid=false 表示"这个路径不是该运行时"，
 * 是常规输入结果——错误直接显示在输入框下方，不弹 toast，也不清空用户输入。
 */
export interface NodeRegisterSectionProps {
  nodeId: number
  /** jdk 或 nodejs。 */
  type: 'jdk' | 'nodejs'
  /** 节点是否在线（离线时探测与目录浏览都不可用）。 */
  online: boolean
  /** 登记在途（外壳 mutation 的 isPending）。 */
  registering?: boolean
  /** 提交登记；返回是否成功。 */
  onRegister: (body: RegisterRuntimeBody) => Promise<boolean>
}

/** 把探测结果转成登记请求体所需的字段。 */
function toRegisterBody(
  type: 'jdk' | 'nodejs',
  c: NonNullable<RuntimeProbeResult['candidate']>,
): RegisterRuntimeBody {
  return {
    type,
    // jdk 的展示名是厂商（Temurin/…）；nodejs 用「Node.js <major>」更可读。
    vendor: type === 'jdk' ? c.vendor : undefined,
    name: type === 'jdk' ? undefined : `Node.js ${c.majorVersion}`,
    majorVersion: c.majorVersion,
    version: c.version,
    arch: c.arch,
    path: c.path,
    // 从"已有路径"登记的运行时不归 Worker 托管根管（managed=false）：
    // 删除时只清记录，不去删用户自己的文件。
    managed: false,
  }
}

export function NodeRegisterSection({
  nodeId,
  type,
  online,
  registering = false,
  onRegister,
}: NodeRegisterSectionProps) {
  const { t } = useTranslation()
  const [path, setPath] = useState('')
  const [pickerOpen, setPickerOpen] = useState(false)
  const [probed, setProbed] = useState<RuntimeProbeResult | null>(null)
  const probe = useProbeRuntime(nodeId)
  const probeSeqRef = useRef(0)

  const label = type === 'jdk' ? 'JDK' : 'Node.js'
  const trimmed = path.trim()

  const runProbe = (p: string) => {
    const seq = ++probeSeqRef.current
    setProbed(null)
    probe.mutate(
      { type, path: p },
      {
        onSuccess: (res) => {
          if (probeSeqRef.current !== seq) return
          setProbed(res)
        },
        // 网络/服务错误才进这里；路径不对是 valid=false 的业务结果，不会抛。
        onError: (err) => {
          if (probeSeqRef.current !== seq) return
          const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
          setProbed({ valid: false, error: msg || t('nodes.register.probeFailed', '探测失败：无法连接节点') })
        },
      },
    )
  }

  const submit = async () => {
    const c = probed?.candidate
    if (!probed?.valid || !c) return
    const ok = await onRegister(toRegisterBody(type, c))
    if (ok) {
      probeSeqRef.current++
      setPath('')
      setProbed(null)
    }
  }

  /*
    同步闸门：登记虽然不像下载那样"起一个长任务"，但重复提交会产生重复登记记录
    （后端按路径查重，仍可能两次都通过）。ref 挡同 tick 连点，state 管渲染期禁用。
  */
  const guard = useSubmitGuard(submit)
  const canSubmit = online && probed?.valid === true && probed.candidate !== undefined
    && !probed.candidate.alreadyRegistered && !registering && !guard.pending

  return (
    <div className="space-y-3" data-testid={`node-register-${type}`}>
      <p className="text-xs text-muted-foreground">
        {type === 'jdk'
          ? t(
              'nodes.register.jdkHint',
              '如果这台节点上已经有 JDK（比如手工装过），填它的路径即可登记，不必重新下载。',
            )
          : t(
              'nodes.register.nodeHint',
              '如果这台节点上已经有 Node.js，填它的路径即可登记。依赖 Node 的插件与 Bot 工具链需要它；Java 服不受影响。',
            )}
      </p>

      <div className="space-y-1.5">
        <label className="text-xs font-medium" htmlFor={`register-path-${type}`}>
          {t('nodes.register.path', '{{name}} 路径', { name: label })}
        </label>
        <div className="flex items-center gap-1.5">
          <Input
            id={`register-path-${type}`}
            value={path}
            onChange={(e) => {
              probeSeqRef.current++
              setPath(e.target.value)
              // 改了路径，之前的探测结果与在途探测都过期了——留着会让人以为新路径已验证。
              setProbed(null)
            }}
            placeholder={
              type === 'jdk'
                ? t('nodes.register.jdkPlaceholder', '如 /opt/jdk-21 或 /usr/lib/jvm/temurin-21')
                : t('nodes.register.nodePlaceholder', '如 /opt/node/bin/node 或 /opt/node-v22')
            }
            className="h-8 flex-1 font-mono text-xs"
            disabled={!online}
            data-testid={`node-register-path-${type}`}
          />
          <Button
            variant="outline"
            size="sm"
            onClick={() => setPickerOpen(true)}
            disabled={!online}
            data-testid={`node-register-browse-${type}`}
            title={t('nodes.register.browse', '浏览节点目录')}
          >
            <FolderOpen className="size-3.5" />
          </Button>
          <Button
            size="sm"
            onClick={() => runProbe(trimmed)}
            disabled={!online || !trimmed || probe.isPending}
            data-testid={`node-register-probe-${type}`}
          >
            {probe.isPending ? <Loader2 className="mr-1 size-3.5 animate-spin" /> : null}
            {probe.isPending ? t('nodes.register.probing', '探测中…') : t('nodes.register.probe', '探测')}
          </Button>
        </div>
        {!online && (
          <p className="text-[11px] text-status-warning" role="alert">
            {t('nodes.register.offline', '节点离线：探测与目录浏览需要节点在线。')}
          </p>
        )}
      </div>

      {/* 探测结果回显：成功=可入库的完整信息；失败=内联错误，不弹窗、不清空输入。 */}
      {probed && probed.valid && probed.candidate && (
        <div
          className="rounded-md border border-status-success/40 bg-status-success/10 px-2.5 py-2"
          data-testid={`node-register-result-${type}`}
        >
          <div className="flex items-center gap-1.5 text-xs font-medium text-status-success">
            <CheckCircle2 className="size-3.5" />
            {t('nodes.register.found', '探测到 {{name}}', { name: label })}
            {probed.candidate.alreadyRegistered && (
              <Badge variant="secondary" className="ml-1" data-testid={`node-register-already-${type}`}>
                {t('nodes.register.alreadyRegistered', '已登记')}
              </Badge>
            )}
          </div>
          <dl className="mt-1.5 grid grid-cols-2 gap-x-3 gap-y-1 text-[11px]">
            <div className="flex gap-1">
              <dt className="text-muted-foreground">{t('nodes.register.vendor', '厂商')}</dt>
              <dd className="truncate font-medium">{probed.candidate.vendor || '—'}</dd>
            </div>
            <div className="flex gap-1">
              <dt className="text-muted-foreground">{t('nodes.register.version', '版本')}</dt>
              <dd className="truncate font-medium">
                {probed.candidate.version}（{t('nodes.register.major', '大版本')} {probed.candidate.majorVersion}）
              </dd>
            </div>
            <div className="flex gap-1">
              <dt className="text-muted-foreground">{t('nodes.register.arch', '架构')}</dt>
              <dd className="truncate font-medium">{probed.candidate.arch || '—'}</dd>
            </div>
            <div className="col-span-2 flex gap-1">
              <dt className="shrink-0 text-muted-foreground">{t('nodes.register.pathLabel', '路径')}</dt>
              <dd className="truncate font-mono">{probed.candidate.path}</dd>
            </div>
          </dl>
        </div>
      )}

      {probed && !probed.valid && (
        <p className="text-[11px] text-status-danger" role="alert" data-testid={`node-register-error-${type}`}>
          {probed.error || t('nodes.register.invalidPath', '这个路径上没找到 {{name}}', { name: label })}
        </p>
      )}

      <div className="flex items-center gap-2">
        <Button
          size="sm"
          onClick={() => void guard.run()}
          disabled={!canSubmit}
          data-testid={`node-register-submit-${type}`}
        >
          {guard.pending || registering ? <Loader2 className="mr-1 size-3.5 animate-spin" /> : null}
          {guard.pending || registering
            ? t('nodes.register.submitting', '登记中…')
            : t('nodes.register.submit', '确认登记')}
        </Button>
        {probed?.candidate?.alreadyRegistered && (
          <span className="text-[11px] text-muted-foreground">
            {t('nodes.register.alreadyHint', '这条路径已在清单里，无需重复登记。')}
          </span>
        )}
      </div>

      {/* 目录浏览：模态承载（不内联动布局），选完自动跑一次探测。 */}
      <Dialog open={pickerOpen} onOpenChange={setPickerOpen}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>
              {t('nodes.register.selectDir', '选择 {{name}} 目录', { name: label })}
            </DialogTitle>
          </DialogHeader>
          <DirectoryPicker
            nodeId={nodeId}
            onPick={(p) => {
              setPickerOpen(false)
              setPath(p)
              // 选完立即探测：用户是从列表里点出来的路径，几乎总是想马上确认。
              runProbe(p)
            }}
            onCancel={() => setPickerOpen(false)}
          />
        </DialogContent>
      </Dialog>
    </div>
  )
}

export default NodeRegisterSection

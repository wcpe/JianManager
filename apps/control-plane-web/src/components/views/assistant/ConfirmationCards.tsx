import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ShieldAlert } from 'lucide-react'
import { Alert, AlertDescription, Button } from '@jianmanager/ui'
import { toast } from 'sonner'
import {
  useDecideConfirmation,
  useRunConfirmations,
  type AssistantConfirmation,
} from '@/api/assistant-chat'

/**
 * 逐调用写入确认卡（FR-516 §7，FR-519 §5.5）。
 *
 * 三条必须守住的行为：
 *  1. **只发 approve / reject**：后端明确不接受新参数或新目标，前端也不构造其它字段
 *     （多传会掩盖「确认即改参」这个被禁止的意图）。
 *  2. **参数原样展示**：核对 UI 以票据里冻结的参数为权威来源，前端不得改写或美化。
 *  3. **过期即不可操作**：票据 10 分钟有效，过期后按钮禁用并说明原因，
 *     而不是点了才报错（那会让用户以为是网络问题）。
 *
 * 为什么单独成组件而不塞进消息流：确认卡是**用户决策对象**，
 * 刷新后仍须可见（用户可能过一会儿才回来确认），而消息流是会重建的。
 */

export interface ConfirmationCardsProps {
  runId: number | null
  /** 决策成功后的回调（容器据此继续观察 run 或刷新历史）。 */
  onDecided?: (id: number, decision: 'approve' | 'reject') => void
}

export function ConfirmationCards({ runId, onDecided }: ConfirmationCardsProps) {
  const { t } = useTranslation()
  const { data: items = [] } = useRunConfirmations(runId)
  const decide = useDecideConfirmation()

  /**
   * 「现在」不能直接读 `Date.now()`：渲染期调用非纯函数会让同一份 props
   * 每次渲染算出不同结果（React 19 的 react-hooks/purity 会直接报错），
   * 也让「过期」在重渲染之间闪变。
   *
   * 做法：挂载时取一次快照，再每秒推进——过期与否**随时间自然翻面**，
   * 而不是靠重渲染碰运气。确认有效期只有 10 分钟，秒级精度足够。
   */
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(timer)
  }, [])

  if (items.length === 0) return null

  const handle = async (item: AssistantConfirmation, decision: 'approve' | 'reject') => {
    try {
      await decide.mutateAsync({ id: item.id, decision })
      onDecided?.(item.id, decision)
    } catch (err) {
      // 错误码映射成人话：过期/已执行/已失效都是**事务性**结果，不是网络故障。
      const body = (err as { response?: { data?: { error?: string; message?: string } } })?.response?.data
      const map: Record<string, string> = {
        CONFIRMATION_EXPIRED: t('assistant.confirmExpired', '该确认已过期，请重新发起操作。'),
        CONFIRMATION_CONSUMED: t('assistant.confirmConsumed', '该确认已被执行过，不会重复执行。'),
        CONFIRMATION_STALE: t('assistant.confirmStale', '参数或权限已变更，请重新发起操作。'),
        FORBIDDEN: t('assistant.confirmForbidden', '只能处理自己的确认。'),
      }
      toast.error(body?.error ? (map[body.error] ?? body.message ?? body.error) : String(err))
    }
  }

  return (
    <div className="flex flex-col gap-2 px-1 pb-2" data-slot="confirmation-cards">
      {items.map((item) => {
        const expired = Date.parse(item.expiresAt) <= now
        const busy = decide.isPending
        return (
          <Alert key={item.id} variant="warning" data-confirmation-id={item.id}>
            <ShieldAlert className="size-4" />
            <AlertDescription className="text-xs">
              <p className="font-medium">
                {t('assistant.confirmTitle', '需要你确认后才会执行')}
                {'：'}
                <code className="rounded bg-muted px-1">{item.toolName}</code>
              </p>
              {item.targetType ? (
                <p className="mt-0.5 text-muted-foreground">
                  {t('assistant.confirmTarget', '目标')}：{item.targetType}:{item.targetId}
                </p>
              ) : null}
              {/* 参数原样展示（JSON 字符串），不改写、不美化：用户核对的就是将要执行的内容。 */}
              <pre className="mt-1 max-h-32 overflow-auto whitespace-pre-wrap break-all rounded bg-muted/60 p-1.5 text-[11px]">
                {JSON.stringify(item.args, null, 2)}
              </pre>
              <div className="mt-1.5 flex items-center gap-2">
                <Button
                  type="button"
                  size="sm"
                  variant={item.risk === 'destructive' ? 'destructive' : 'default'}
                  className="h-7 text-xs"
                  disabled={expired || busy}
                  onClick={() => void handle(item, 'approve')}
                >
                  {t('assistant.confirmApprove', '批准并执行')}
                </Button>
                <Button
                  type="button"
                  size="sm"
                  variant="ghost"
                  className="h-7 text-xs"
                  // 与「批准」保持一致：过期后整张卡片不可操作。
                  // 只禁用「批准」会让用户以为「拒绝」还能用，点下去只会拿到
                  // CONFIRMATION_EXPIRED——多一次无意义请求，且体验上是误导。
                  disabled={expired || busy}
                  onClick={() => void handle(item, 'reject')}
                >
                  {t('assistant.confirmReject', '拒绝')}
                </Button>
                {expired ? (
                  <span className="text-[11px] text-muted-foreground">
                    {t('assistant.confirmExpired', '该确认已过期，请重新发起操作。')}
                  </span>
                ) : null}
              </div>
            </AlertDescription>
          </Alert>
        )
      })}
    </div>
  )
}

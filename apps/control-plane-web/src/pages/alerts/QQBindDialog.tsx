import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { RefreshCw } from 'lucide-react'
import {
  QQ_BIND_POLL_MS,
  fetchQQBindResult,
  isBindTaskExpiredError,
  useCreateQQBindTask,
  type QQBindTaskInfo,
} from '@/api/alerts'
import { Button } from '@jianmanager/ui/components/button'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import {
  ScrollableDialogBody,
  scrollableDialogContentClass,
} from '@jianmanager/ui/components/scrollable-dialog'
import { QQBindQrCode } from './qq-badge'
import type { QQBindFill } from '@jianmanager/ui/lib/alert-contracts'

// 实现已迁至 @jianmanager/ui（ADR-097），此处保留 re-export 维持既有导入路径。
export type { QQBindFill }


/** 弹窗当前所处的阶段（同一时刻只展示一条状态文案）。 */
type BindPhase =
  /** 正在申请绑定任务 / 正在重申请。 */
  | 'creating'
  /** 二维码已就绪，等待用户扫码。 */
  | 'waiting'
  /** 申请绑定任务失败（后端 502 BIND_TASK_FAILED / 网络）。 */
  | 'create-failed'
  /** 查询绑定结果失败（非过期类错误）。 */
  | 'poll-failed'
  /** 自动重申请次数用尽。 */
  | 'exhausted'
  /** completed 但三要素不全（半截响应），无法自动填表。 */
  | 'incomplete'

interface QQBindDialogProps {
  /** 扫码成功且三要素齐全时回调（父表单据此回填并关闭本弹窗）。 */
  onBound: (fill: QQBindFill) => void
  onClose: () => void
}

/**
 * 二维码过期后自动重新申请的次数上限。
 *
 * 平台对绑定/分享类接口有频率限制（实测约 9 次即被限流），无上限地重申请会把额度打光、
 * 反而让用户彻底扫不了码。到顶后停下来给「重新生成」按钮，把重试时机交还给用户。
 */
export const MAX_QQ_BIND_AUTO_REFRESH = 3

/**
 * QQ 扫码绑定弹窗（FR-494 接入体验改造）：把「手工抄 appId / appSecret / openid 三个值」
 * 变成扫一次码。
 *
 * 交互要点：
 * - 打开即申请绑定任务并渲染二维码，随后每 {@link QQ_BIND_POLL_MS} 轮询一次结果（节流）。
 * - `expired`（含本地密钥已过期的 404 BIND_TASK_EXPIRED）自动重新申请并提示二维码已刷新，
 *   次数见 {@link MAX_QQ_BIND_AUTO_REFRESH}。
 * - `completed` 时把 appId / secretEnv / userOpenid 交给父表单，明文 appSecret 全程不经过前端。
 * - 弹窗卸载（关闭）即 `clearTimeout` 并置取消位，绝不再打接口——点了取消还继续轮询会白白
 *   消耗平台的频率额度。
 * - 任何一步失败都保留手工填写路径，文案给「怎么办」而不是裸错误码。
 */
export function QQBindDialog({ onBound, onClose }: QQBindDialogProps) {
  const { t } = useTranslation()
  const create = useCreateQQBindTask()
  const [task, setTask] = useState<QQBindTaskInfo | null>(null)
  const [phase, setPhase] = useState<BindPhase>('creating')
  /** 至少自动重申请过一次（提示用户用最新二维码重扫）。 */
  const [refreshed, setRefreshed] = useState(false)
  /** 「重新生成」手动重启轮询的代号；变化即重跑轮询 effect。 */
  const [attempt, setAttempt] = useState(0)
  /** 始终可用的最新 onBound：写 ref 不触发渲染，也不会让轮询循环随父级重渲染重启。 */
  const onBoundRef = useRef(onBound)

  useEffect(() => {
    onBoundRef.current = onBound
  }, [onBound])

  useEffect(() => {
    let cancelled = false
    let timer: ReturnType<typeof setTimeout> | undefined
    /** 轮询节拍等待；句柄留在 timer 里供卸载清理。 */
    const sleep = (ms: number) =>
      new Promise<void>((resolve) => {
        timer = setTimeout(resolve, ms)
      })

    const run = async () => {
      let autoRefreshes = 0
      for (;;) {
        let created: QQBindTaskInfo
        try {
          created = await create.mutateAsync()
        } catch {
          if (cancelled) return
          setTask(null)
          setPhase('create-failed')
          return
        }
        if (cancelled) return
        setTask(created)
        setPhase('waiting')

        // 轮询该任务；内层循环只有「过期 → 跳出重申请」或「completed / 出错 → 直接 return」两条出路。
        for (;;) {
          await sleep(QQ_BIND_POLL_MS)
          if (cancelled) return
          let result: Awaited<ReturnType<typeof fetchQQBindResult>>
          try {
            result = await fetchQQBindResult(created.taskId)
          } catch (err) {
            if (cancelled) return
            // 本地密钥已过期时后端回 404 BIND_TASK_EXPIRED，语义等同 expired：重新申请即可。
            if (isBindTaskExpiredError(err)) break
            setPhase('poll-failed')
            return
          }
          if (cancelled) return
          if (result.status === 'completed') {
            // 三要素缺一即不填：宁可让用户手工来，也不填半截值骗过表单校验。
            if (!result.appId || !result.userOpenid || !result.secretEnv) {
              setPhase('incomplete')
              return
            }
            onBoundRef.current({
              appId: result.appId,
              secretEnv: result.secretEnv,
              userOpenid: result.userOpenid,
            })
            return
          }
          if (result.status === 'expired') break
          // none / pending → 继续下一拍。
        }

        // 能走到这里只有一种可能：二维码（或本地任务密钥）已过期，重新申请一张。
        if (autoRefreshes >= MAX_QQ_BIND_AUTO_REFRESH) {
          setPhase('exhausted')
          return
        }
        autoRefreshes += 1
        setRefreshed(true)
      }
    }

    run()
    return () => {
      cancelled = true
      if (timer !== undefined) clearTimeout(timer)
    }
    // 只在挂载与手动「重新生成」时重启循环；create 的 mutateAsync 引用稳定，不需要进依赖。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [attempt])

  /** 手动重新生成：清掉失败态并重启轮询循环。 */
  const regenerate = () => {
    setTask(null)
    setPhase('creating')
    setRefreshed(false)
    setAttempt((n) => n + 1)
  }

  return (
    <Dialog open onOpenChange={(next) => { if (!next) onClose() }}>
      <DialogContent className={`${scrollableDialogContentClass} sm:max-w-sm`}>
        <DialogHeader>
          <DialogTitle>{t('alerts.qqBindTitle')}</DialogTitle>
        </DialogHeader>

        <ScrollableDialogBody className="space-y-3">
          {!task ? (
            phase === 'create-failed' ? (
              <p className="text-sm text-destructive" role="alert">
                {t('alerts.qqBindFailed')}
              </p>
            ) : (
              <p className="text-sm text-muted-foreground">{t('common.loading')}</p>
            )
          ) : (
            <>
              <div className="flex justify-center">
                <QQBindQrCode url={task.qrUrl} />
              </div>
              <p className="text-xs text-muted-foreground">{t('alerts.qqBindQrHint')}</p>

              {/* 状态行：异常态用 alert，等待态用 status（读屏可达）。 */}
              {phase === 'exhausted' ? (
                <p className="text-xs text-destructive" role="alert">
                  {t('alerts.qqBindExpired')}
                </p>
              ) : phase === 'poll-failed' ? (
                <p className="text-xs text-destructive" role="alert">
                  {t('alerts.qqBindPollFailed')}
                </p>
              ) : phase === 'incomplete' ? (
                <p className="text-xs text-destructive" role="alert">
                  {t('alerts.qqBindIncomplete')}
                </p>
              ) : (
                <p className="text-xs text-muted-foreground" role="status">
                  {refreshed ? t('alerts.qqBindRefreshed') : t('alerts.qqBindWaiting')}
                </p>
              )}

              {/* 链接兜底：二维码扫不出来时可在手机 QQ 里直接打开。 */}
              <div className="space-y-1">
                <p className="text-xs text-muted-foreground">{t('alerts.qqBindLinkHint')}</p>
                <p
                  className="break-all font-mono text-[11px] text-muted-foreground"
                  data-testid="qq-bind-url"
                  title={task.qrUrl}
                >
                  {task.qrUrl}
                </p>
              </div>
            </>
          )}
        </ScrollableDialogBody>

        <DialogFooter>
          <Button type="button" variant="outline" onClick={onClose}>
            {t('alerts.qqBindManual')}
          </Button>
          <Button type="button" variant="secondary" disabled={create.isPending} onClick={regenerate}>
            <RefreshCw className="size-3.5" />
            {phase === 'exhausted' ? t('alerts.qqBindRegenerate') : t('alerts.qqBindRefresh')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

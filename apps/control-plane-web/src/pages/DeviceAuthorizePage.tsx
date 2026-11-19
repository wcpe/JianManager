import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { useSearchParams } from 'react-router'
import { toast } from 'sonner'
import { CheckCircle2, XCircle } from 'lucide-react'
import { useAuthorizeDevice, errMsg } from '@/api/userTokens'
import { Panel } from '@jianmanager/ui/components/panel'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { Label } from '@jianmanager/ui/components/label'

/**
 * 设备授权确认页（FR-562 / RFC 8628 / ADR-112）。
 *
 * 场景：用户在终端运行 `jm auth login`，CLI 显示 user_code 与 URL；
 * 用户在本页输入 user_code 并确认，CLI 那边轮询到令牌完成登录。
 *
 * 页面本身不预取设备码信息（后端无只读查询端点），
 * 由提交结果反馈「不存在 / 已过期 / 已使用」，这是刻意的信息收敛。
 */
export default function DeviceAuthorizePage() {
  const { t } = useTranslation()
  const [params] = useSearchParams()
  const authorizeM = useAuthorizeDevice()

  // 支持 ?user_code=XXXX-XXXX 预填（CLI 可把链接直接给出）。
  const [code, setCode] = useState(params.get('user_code') ?? '')
  const [outcome, setOutcome] = useState<'approved' | 'denied' | ''>('')

  function submit(approve: boolean) {
    const trimmed = code.trim()
    if (!trimmed) {
      toast.error(t('deviceAuth.codeRequired'))
      return
    }
    authorizeM.mutate(
      { userCode: trimmed, approve },
      {
        onSuccess: () => {
          setOutcome(approve ? 'approved' : 'denied')
        },
        onError: (e) => {
          toast.error(errMsg(e, t('deviceAuth.failed')))
        },
      },
    )
  }

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    submit(true)
  }

  // 完成后展示结果，不再接受重复操作。
  if (outcome) {
    const approved = outcome === 'approved'
    return (
      <div className="grid min-h-screen place-items-center p-6">
        <Panel className="w-full max-w-md">
          <div className="space-y-4 py-6 text-center">
            {approved ? (
              <CheckCircle2 className="mx-auto size-12 text-emerald-500" />
            ) : (
              <XCircle className="mx-auto size-12 text-muted-foreground" />
            )}
            <h1 className="text-lg font-semibold">
              {t(approved ? 'deviceAuth.approvedTitle' : 'deviceAuth.deniedTitle')}
            </h1>
            <p className="text-sm text-muted-foreground">
              {t(approved ? 'deviceAuth.approvedBody' : 'deviceAuth.deniedBody')}
            </p>
          </div>
        </Panel>
      </div>
    )
  }

  return (
    <div className="grid min-h-screen place-items-center p-6">
      <Panel className="w-full max-w-md">
        <form onSubmit={handleSubmit} className="space-y-4 py-4">
          <div className="space-y-1">
            <h1 className="text-lg font-semibold">{t('deviceAuth.title')}</h1>
            <p className="text-sm text-muted-foreground">{t('deviceAuth.subtitle')}</p>
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="device-code">{t('deviceAuth.codeLabel')}</Label>
            <Input
              id="device-code"
              value={code}
              onChange={(e) => setCode(e.target.value.toUpperCase())}
              placeholder="XXXX-XXXX"
              autoComplete="off"
              autoFocus
              maxLength={16}
              className="font-mono tracking-widest"
            />
            <p className="text-xs text-muted-foreground">{t('deviceAuth.codeHint')}</p>
          </div>

          <div className="rounded-md border bg-muted/40 p-3">
            <p className="text-xs text-muted-foreground">{t('deviceAuth.warning')}</p>
          </div>

          <div className="flex gap-2">
            <Button
              type="button"
              variant="outline"
              className="flex-1"
              disabled={authorizeM.isPending}
              onClick={() => submit(false)}
            >
              {t('deviceAuth.deny')}
            </Button>
            <Button type="submit" className="flex-1" disabled={authorizeM.isPending}>
              {authorizeM.isPending ? t('common.saving') : t('deviceAuth.approve')}
            </Button>
          </div>
        </form>
      </Panel>
    </div>
  )
}

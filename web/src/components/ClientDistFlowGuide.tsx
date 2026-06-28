import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  AlertTriangle,
  ChevronDown,
  KeyRound,
  PackageCheck,
  RefreshCw,
  Rocket,
  Route,
} from 'lucide-react'
import { cn } from '@/lib/utils'

/**
 * 客户端分发端到端流程图（FR-194，纯前端，增强 FR-187）。
 * 渲染在客户端分发列表页标题下方，用运维向大白话讲清「首次发布（做一遍）」与
 * 「日常更新（反复）」两段流程 + 三条关键提醒，降低首次上手门槛。默认收起避免占太多空间。
 * 纯展示组件，不含任何接口调用；i18n（FR-016）+ 暗/亮 + 双主题随既有体系（全程主题 token）。
 */
export default function ClientDistFlowGuide() {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)

  return (
    <div className="rounded-xl border bg-card/40">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        className="flex w-full items-center justify-between gap-3 rounded-xl px-4 py-3 text-left transition-colors hover:bg-accent/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <span className="flex items-center gap-2">
          <Route className="size-4 text-primary" />
          <span className="text-sm font-semibold">
            {t('clientDistFlow.title', '分发是怎么跑起来的？')}
          </span>
          <span className="hidden text-xs text-muted-foreground sm:inline">
            {t('clientDistFlow.subtitle', '一图看懂首次发布与日常更新')}
          </span>
        </span>
        <ChevronDown className={cn('size-4 shrink-0 text-muted-foreground transition-transform', open && 'rotate-180')} />
      </button>

      {open && (
        <div className="space-y-4 border-t px-4 pb-4 pt-4">
          {/* ① 首次发布（做一遍） */}
          <FlowSection
            icon={<Rocket className="size-4" />}
            tone="primary"
            badge={t('clientDistFlow.onceBadge', '做一遍')}
            title={t('clientDistFlow.firstTitle', '① 首次发布')}
            steps={[
              t('clientDistFlow.first1', '建一个分发频道（每服一个）。'),
              t('clientDistFlow.first2', '建一把拉取密钥——玩家更新器的「门禁卡」，务必存好、可随时查看；丢了玩家就断更。'),
              t('clientDistFlow.first3', '发布第一版：传文件或 zip 整合包 → 编排目录 → 发布，系统自动签名。'),
              t('clientDistFlow.first4', '打包给玩家：把 楔子.jar + 更新核心.jar + 配置（频道 + 密钥）一起塞进整合包。'),
              t('clientDistFlow.first5', '一次性分发整合包给玩家——之后再也不用重发整合包。'),
            ]}
          />

          {/* ② 日常更新（反复） */}
          <FlowSection
            icon={<RefreshCw className="size-4" />}
            tone="emerald"
            badge={t('clientDistFlow.loopBadge', '反复做')}
            title={t('clientDistFlow.dailyTitle', '② 日常更新')}
            steps={[
              t('clientDistFlow.daily1', '你在面板发一个新版本。'),
              t('clientDistFlow.daily2', '玩家一开游戏自动更新：楔子 → 加载更新核心 → 凭密钥拉签名清单 → 验签 → 只下变化的文件 → 进游戏。'),
              t('clientDistFlow.daily3', '万一出问题，自动退回上一个好用的版本，玩家照样进得去。'),
            ]}
          />

          {/* 关键提醒 */}
          <div className="rounded-lg border border-amber-500/30 bg-amber-500/5 p-3">
            <div className="flex items-center gap-2 text-sm font-medium text-amber-700 dark:text-amber-500">
              <AlertTriangle className="size-4" />
              {t('clientDistFlow.tipsTitle', '三条要记牢')}
            </div>
            <ul className="mt-2 space-y-1.5 text-xs text-muted-foreground">
              <li className="flex items-start gap-2">
                <PackageCheck className="mt-0.5 size-3.5 shrink-0 text-muted-foreground/70" />
                {t('clientDistFlow.tipPackage', '整合包只发一次，往后全靠这套系统自动更新，不用再发整合包。')}
              </li>
              <li className="flex items-start gap-2">
                <KeyRound className="mt-0.5 size-3.5 shrink-0 text-muted-foreground/70" />
                {t('clientDistFlow.tipKey', '密钥不能丢——它是玩家更新的门禁卡（本平台可随时查看明文）；真丢了得轮换并重发配置。')}
              </li>
              <li className="flex items-start gap-2">
                <Rocket className="mt-0.5 size-3.5 shrink-0 text-muted-foreground/70" />
                {t('clientDistFlow.tipWedge', '楔子固定不变，平时不用动；要升级/回退的只是「更新核心」。')}
              </li>
            </ul>
          </div>
        </div>
      )}
    </div>
  )
}

/** 流程段落：左侧色调图标 + 标题徽标 + 编号圆点步骤列表。 */
function FlowSection({
  icon,
  tone,
  badge,
  title,
  steps,
}: {
  icon: React.ReactNode
  tone: 'primary' | 'emerald'
  badge: string
  title: string
  steps: string[]
}) {
  const toneIcon =
    tone === 'primary'
      ? 'bg-primary/10 text-primary'
      : 'bg-emerald-500/15 text-emerald-600 dark:text-emerald-500'
  const toneDot =
    tone === 'primary'
      ? 'bg-primary/10 text-primary'
      : 'bg-emerald-500/15 text-emerald-600 dark:text-emerald-500'

  return (
    <div className="rounded-lg border bg-background/40 p-3">
      <div className="mb-3 flex items-center gap-2">
        <span className={cn('grid size-7 shrink-0 place-items-center rounded-lg', toneIcon)}>{icon}</span>
        <h3 className="text-sm font-semibold">{title}</h3>
        <span className="rounded-full bg-muted px-2 py-0.5 text-[11px] text-muted-foreground">{badge}</span>
      </div>
      <ol className="space-y-2">
        {steps.map((step, i) => (
          <li key={i} className="flex items-start gap-2.5 text-sm">
            <span
              className={cn(
                'mt-0.5 grid size-5 shrink-0 place-items-center rounded-full text-[11px] font-medium',
                toneDot,
              )}
            >
              {i + 1}
            </span>
            <span className="text-muted-foreground">{step}</span>
          </li>
        ))}
      </ol>
    </div>
  )
}

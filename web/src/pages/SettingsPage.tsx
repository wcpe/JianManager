import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import { toast } from 'sonner'
import { Palette, ScrollText, Cpu, Archive, Lock, ShieldAlert, type LucideIcon } from 'lucide-react'
import { useThemeStore } from '@/stores/theme'
import { useAuthStore } from '@/stores/auth'
import { changeLanguage } from '@/i18n'
import { cn } from '@/lib/utils'
import { useSettings, useUpdateSettings, type SettingItem } from '@/api/settings'
import { diffSettings, hasUnsavedChanges } from './settings-form'
import { Panel } from '@/components/ui/panel'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

type Theme = 'light' | 'dark' | 'system'

/** 平台管理员角色值（与后端 model.RolePlatformAdmin 对齐）。 */
const ROLE_PLATFORM_ADMIN = 10

/** 日志级别可选值，用于可编辑项的下拉。 */
const LOG_LEVELS = ['debug', 'info', 'warn', 'error'] as const

/** 设置分类（FR-063）：内部侧边栏据此分组。appearance 为客户端偏好，其余为平台配置。 */
type SettingCategory = 'appearance' | 'logging' | 'runtime' | 'backup' | 'security'

/** 把平台配置键映射到分类：可编辑项落 logging/runtime/backup，只读项落 security。 */
function keyCategory(key: string): SettingCategory {
  if (key.startsWith('log.')) return 'logging'
  if (key.startsWith('jdk.') || key.startsWith('graceful_stop.')) return 'runtime'
  if (key.startsWith('backup.')) return 'backup'
  return 'security'
}

const CATEGORY_ICON: Record<SettingCategory, LucideIcon> = {
  appearance: Palette,
  logging: ScrollText,
  runtime: Cpu,
  backup: Archive,
  security: Lock,
}

/** 取配置项的本地化标签，缺省回退键名本身。 */
function settingLabel(t: TFunction, key: string): string {
  return t(`settings.keys.${key}`, key)
}

/**
 * 系统设置页（FR-037 + FR-063 + FR-158）：内部侧边栏分类 + 右侧分类面板，套「设置表单」范式。
 * 外观/语言为客户端偏好（localStorage）；其余为服务端平台配置（DB 覆盖层）。
 * FR-158：切分类时若有未保存草稿则拦截确认；非即时项标注生效时机；security 项视觉隔离。
 * 草稿状态上提到本组件（按键全局唯一），使切分类拦截可跨分类工作；平台配置分类仅平台管理员可见。
 */
export default function SettingsPage() {
  const { t } = useTranslation()
  const role = useAuthStore((s) => s.role)
  const isPlatformAdmin = role === ROLE_PLATFORM_ADMIN
  const [cat, setCat] = useState<SettingCategory>('appearance')

  const { data, isLoading, isError } = useSettings()
  // 可编辑项的本地草稿：仅存用户改动；展示值回退到后端当前生效值（draft[key] ?? it.value）。
  const [draft, setDraft] = useState<Record<string, string>>({})
  // 待确认切换的目标分类（非空时弹未保存拦截对话框）。
  const [pendingCat, setPendingCat] = useState<SettingCategory | null>(null)

  const categories: SettingCategory[] = isPlatformAdmin
    ? ['appearance', 'logging', 'runtime', 'backup', 'security']
    : ['appearance']

  // 当前分类的可编辑项（appearance 无平台项）。
  const currentEditable = (data?.editable ?? []).filter((it) => keyCategory(it.key) === cat)
  const currentDirty = hasUnsavedChanges(currentEditable, draft)

  /** 切分类：当前分类有未保存草稿时先拦截，否则直接切。 */
  const requestSwitch = (next: SettingCategory) => {
    if (next === cat) return
    if (currentDirty) {
      setPendingCat(next)
      return
    }
    setCat(next)
  }

  /** 确认放弃改动并切换：清掉当前分类草稿键，切到目标分类。 */
  const confirmSwitch = () => {
    if (pendingCat === null) return
    setDraft((d) => {
      const next = { ...d }
      for (const it of currentEditable) delete next[it.key]
      return next
    })
    setCat(pendingCat)
    setPendingCat(null)
  }

  return (
    <div className="space-y-4">
      <div>
        <h1 className="text-xl font-bold">{t('settings.title')}</h1>
        <p className="text-xs text-muted-foreground">{t('settings.subtitle')}</p>
      </div>

      <div className="flex gap-6">
        {/* 内部侧边栏：分类导航 */}
        <aside className="w-44 shrink-0">
          <nav className="space-y-0.5">
            {categories.map((c) => {
              const Icon = CATEGORY_ICON[c]
              const dirtyHere = c === cat && currentDirty
              return (
                <button
                  key={c}
                  type="button"
                  onClick={() => requestSwitch(c)}
                  className={cn(
                    'flex w-full items-center gap-2 rounded-md px-2.5 py-1.5 text-[13px] transition-colors',
                    cat === c
                      ? 'bg-primary/10 font-medium text-primary'
                      : 'text-foreground/80 hover:bg-accent/60',
                  )}
                >
                  <Icon className="size-4 shrink-0" />
                  <span className="truncate">{t(`settings.cat.${c}`)}</span>
                  {/* 未保存指示点：当前分类有草稿时显示 */}
                  {dirtyHere && (
                    <span
                      className="ml-auto size-1.5 shrink-0 rounded-full bg-status-warning"
                      title={t('settings.unsavedDot')}
                    />
                  )}
                </button>
              )
            })}
          </nav>
        </aside>

        {/* 右侧：当前分类面板 */}
        <div className="min-w-0 max-w-2xl flex-1">
          {cat === 'appearance' ? (
            <AppearanceSettings />
          ) : (
            <PlatformCategory
              category={cat}
              data={data}
              isLoading={isLoading}
              isError={isError}
              draft={draft}
              setDraft={setDraft}
            />
          )}
        </div>
      </div>

      {/* FR-158：切分类未保存拦截确认 */}
      <Dialog open={pendingCat !== null} onOpenChange={(o) => !o && setPendingCat(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('settings.unsavedTitle')}</DialogTitle>
            <DialogDescription>{t('settings.unsavedDesc')}</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" size="sm" onClick={() => setPendingCat(null)}>
              {t('settings.stay')}
            </Button>
            <Button variant="destructive" size="sm" onClick={confirmSwitch}>
              {t('settings.discardAndSwitch')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

/** 外观：主题模式 + 语言（客户端偏好）。主题色切换在侧栏（FR-164），此处不重复。 */
function AppearanceSettings() {
  const { t, i18n } = useTranslation()
  const { theme, setTheme } = useThemeStore()
  const currentLang = i18n.language as 'zh' | 'en'
  return (
    <Panel bodyClassName="space-y-4 p-4">
      <div>
        <h2 className="text-sm font-semibold">{t('settings.appearance')}</h2>
        <p className="text-xs text-muted-foreground">{t('settings.appearanceDesc')}</p>
      </div>
      <div className="flex items-center justify-between gap-4">
        <div>
          <p className="text-sm font-medium">{t('settings.theme')}</p>
          <p className="text-xs text-muted-foreground">{t('settings.themeDesc')}</p>
        </div>
        <Select value={theme} onValueChange={(v: string) => setTheme(v as Theme)}>
          <SelectTrigger size="sm" className="w-36">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="light">{t('theme.light')}</SelectItem>
            <SelectItem value="dark">{t('theme.dark')}</SelectItem>
            <SelectItem value="system">{t('theme.system')}</SelectItem>
          </SelectContent>
        </Select>
      </div>

      <div className="flex items-center justify-between gap-4">
        <div>
          <p className="text-sm font-medium">{t('settings.language')}</p>
          <p className="text-xs text-muted-foreground">{t('settings.languageDesc')}</p>
        </div>
        <Select value={currentLang} onValueChange={(v: string) => changeLanguage(v as 'zh' | 'en')}>
          <SelectTrigger size="sm" className="w-36">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="zh">{t('language.zh')}</SelectItem>
            <SelectItem value="en">{t('language.en')}</SelectItem>
          </SelectContent>
        </Select>
      </div>
    </Panel>
  )
}

/**
 * 平台配置分类面板：security 展示只读项（视觉隔离），其余（logging/runtime/backup）展示可编辑项 + 保存。
 * 数据/草稿由父组件提供，切分类拦截在父层处理。
 */
function PlatformCategory({
  category,
  data,
  isLoading,
  isError,
  draft,
  setDraft,
}: {
  category: SettingCategory
  data: import('@/api/settings').SettingsView | undefined
  isLoading: boolean
  isError: boolean
  draft: Record<string, string>
  setDraft: React.Dispatch<React.SetStateAction<Record<string, string>>>
}) {
  const { t } = useTranslation()
  const update = useUpdateSettings()

  const editable = (data?.editable ?? []).filter((it) => keyCategory(it.key) === category)
  const readOnly = (data?.readOnly ?? []).filter((it) => keyCategory(it.key) === category)

  const changed = diffSettings(editable, draft)
  const hasChanges = Object.keys(changed).length > 0
  const isSecurity = category === 'security'

  const save = async () => {
    if (!hasChanges) return
    try {
      await update.mutateAsync({ values: changed })
      // 保存成功后清掉已落库的草稿键（回到「无改动」基线）。
      setDraft((d) => {
        const next = { ...d }
        for (const k of Object.keys(changed)) delete next[k]
        return next
      })
      toast.success(t('settings.saved', '已保存'))
    } catch (err: unknown) {
      const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
      toast.error(msg || t('settings.saveFailed', '保存失败'))
    }
  }

  return (
    <Panel
      // security 项视觉隔离：琥珀左描边 + 警示头底色。
      className={cn(isSecurity && 'border-status-warning/40')}
      bodyClassName="space-y-4 p-4"
    >
      <div className="flex items-start gap-2">
        {isSecurity && (
          <span className="mt-0.5 flex size-6 shrink-0 items-center justify-center rounded-md bg-status-warning/12 text-status-warning">
            <ShieldAlert className="size-4" />
          </span>
        )}
        <div>
          <h2 className="text-sm font-semibold">{t(`settings.cat.${category}`)}</h2>
          <p className="text-xs text-muted-foreground">{t(`settings.catDesc.${category}`, '')}</p>
        </div>
      </div>
      {isLoading && <p className="text-sm text-muted-foreground">{t('common.loading', '加载中…')}</p>}
      {isError && <p className="text-sm text-destructive">{t('settings.loadFailed', '加载平台配置失败')}</p>}

      {!isLoading && !isError && isSecurity && (
        <>
          <p className="rounded-md bg-status-warning/10 px-3 py-2 text-xs text-status-warning">
            {t('settings.securityNotice')}
          </p>
          <div className="divide-y rounded-md border">
            {readOnly.length === 0 ? (
              <p className="px-3 py-6 text-center text-sm text-muted-foreground">{t('settings.empty', '暂无配置项')}</p>
            ) : (
              readOnly.map((it) => <ReadOnlyRow key={it.key} item={it} />)
            )}
          </div>
        </>
      )}

      {!isLoading && !isError && !isSecurity && (
        <>
          <p className="text-xs text-muted-foreground">{t('settings.editableHint', '保存后立即覆盖默认值。')}</p>
          <div className="divide-y rounded-md border">
            {editable.length === 0 ? (
              <p className="px-3 py-6 text-center text-sm text-muted-foreground">{t('settings.empty', '暂无配置项')}</p>
            ) : (
              editable.map((it) => (
                <EditableRow
                  key={it.key}
                  item={it}
                  value={draft[it.key] ?? it.value}
                  onChange={(v) => setDraft((d) => ({ ...d, [it.key]: v }))}
                />
              ))
            )}
          </div>
          {editable.length > 0 && (
            <div className="flex justify-end">
              <Button size="sm" onClick={save} disabled={!hasChanges || update.isPending}>
                {update.isPending ? t('common.saving', '保存中…') : t('common.save', '保存')}
              </Button>
            </div>
          )}
        </>
      )}
    </Panel>
  )
}

/** 单个可编辑配置项行：log.level 用下拉，其余用文本框；标注是否即时生效/已覆盖。 */
function EditableRow({
  item,
  value,
  onChange,
}: {
  item: SettingItem
  value: string
  onChange: (v: string) => void
}) {
  const { t } = useTranslation()
  return (
    <div className="flex items-center justify-between gap-4 px-3 py-2">
      <div className="min-w-0">
        <div className="flex items-center gap-2">
          <p className="text-sm font-medium truncate">{settingLabel(t, item.key)}</p>
          {item.overridden && <Badge variant="secondary">{t('settings.overridden', '已覆盖')}</Badge>}
          {!item.effectiveImmediately && (
            // FR-158：非即时项标注生效时机（hover 展开说明）。
            <Badge variant="outline" title={t('settings.effectTimingHint')}>
              {t('settings.workerSide', 'Worker 侧生效')}
            </Badge>
          )}
        </div>
        <p className="text-xs text-muted-foreground font-mono">{item.key}</p>
      </div>
      {item.key === 'log.level' ? (
        <Select value={value} onValueChange={onChange}>
          <SelectTrigger size="sm" className="w-40">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {LOG_LEVELS.map((lv) => (
              <SelectItem key={lv} value={lv}>
                {lv}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      ) : (
        <Input value={value} onChange={(e) => onChange(e.target.value)} className="h-8 w-56" />
      )}
    </div>
  )
}

/** 单个只读配置项行：展示当前生效值；敏感项标注「已脱敏」。 */
function ReadOnlyRow({ item }: { item: SettingItem }) {
  const { t } = useTranslation()
  return (
    <div className="flex items-center justify-between gap-4 px-3 py-2">
      <div className="min-w-0">
        <div className="flex items-center gap-2">
          <p className="text-sm font-medium truncate">{settingLabel(t, item.key)}</p>
          {item.sensitive && <Badge variant="outline">{t('settings.masked', '已脱敏')}</Badge>}
        </div>
        <p className="text-xs text-muted-foreground font-mono">{item.key}</p>
      </div>
      <code className="text-xs text-muted-foreground max-w-[14rem] truncate text-right">{item.value || '—'}</code>
    </div>
  )
}

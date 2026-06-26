import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import { toast } from 'sonner'
import { Palette, ScrollText, Cpu, Archive, Lock, type LucideIcon } from 'lucide-react'
import { useThemeStore } from '@/stores/theme'
import { useAuthStore } from '@/stores/auth'
import { changeLanguage } from '@/i18n'
import { cn } from '@/lib/utils'
import { useSettings, useUpdateSettings, type SettingItem } from '@/api/settings'
import { Panel } from '@/components/ui/panel'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
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
 * 系统设置页（FR-037 + FR-063）：内部侧边栏分类 + 右侧分类面板。
 * 外观/语言为客户端偏好（localStorage）；其余为服务端平台配置（DB 覆盖层），
 * 可编辑项落库即生效（覆盖 env/YAML），只读项展示当前生效值并提示需改配置重启。
 * 平台配置分类仅对平台管理员展示（后端 RBAC 同样收敛）。
 */
export default function SettingsPage() {
  const { t } = useTranslation()
  const role = useAuthStore((s) => s.role)
  const isPlatformAdmin = role === ROLE_PLATFORM_ADMIN
  const [cat, setCat] = useState<SettingCategory>('appearance')

  const categories: SettingCategory[] = isPlatformAdmin
    ? ['appearance', 'logging', 'runtime', 'backup', 'security']
    : ['appearance']

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
              return (
                <button
                  key={c}
                  type="button"
                  onClick={() => setCat(c)}
                  className={cn(
                    'flex w-full items-center gap-2 rounded-md px-2.5 py-1.5 text-[13px] transition-colors',
                    cat === c
                      ? 'bg-primary/10 font-medium text-primary'
                      : 'text-foreground/80 hover:bg-accent/60',
                  )}
                >
                  <Icon className="size-4 shrink-0" />
                  <span className="truncate">{t(`settings.cat.${c}`)}</span>
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
            <PlatformCategory category={cat} />
          )}
        </div>
      </div>
    </div>
  )
}

/** 外观：主题 + 语言（客户端偏好）。 */
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
 * 平台配置分类面板：security 展示只读项，其余（logging/runtime/backup）展示可编辑项 + 保存。
 * 数据经 useSettings（react-query 缓存，切换分类不重拉），按分类过滤后渲染。
 */
function PlatformCategory({ category }: { category: SettingCategory }) {
  const { t } = useTranslation()
  const { data, isLoading, isError } = useSettings()
  const update = useUpdateSettings()

  // 可编辑项的本地草稿：仅存用户改动；展示值回退到后端当前生效值（draft[key] ?? it.value）。
  const [draft, setDraft] = useState<Record<string, string>>({})

  const editable = (data?.editable ?? []).filter((it) => keyCategory(it.key) === category)
  const readOnly = (data?.readOnly ?? []).filter((it) => keyCategory(it.key) === category)

  const changed: Record<string, string> = {}
  for (const it of editable) {
    if (draft[it.key] !== undefined && draft[it.key] !== it.value) changed[it.key] = draft[it.key]
  }
  const hasChanges = Object.keys(changed).length > 0

  const save = async () => {
    if (!hasChanges) return
    try {
      await update.mutateAsync({ values: changed })
      toast.success(t('settings.saved', '已保存'))
    } catch (err: unknown) {
      const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
      toast.error(msg || t('settings.saveFailed', '保存失败'))
    }
  }

  return (
    <Panel bodyClassName="space-y-4 p-4">
      <div>
        <h2 className="text-sm font-semibold">{t(`settings.cat.${category}`)}</h2>
        <p className="text-xs text-muted-foreground">{t(`settings.catDesc.${category}`, '')}</p>
      </div>
      {isLoading && <p className="text-sm text-muted-foreground">{t('common.loading', '加载中…')}</p>}
        {isError && <p className="text-sm text-destructive">{t('settings.loadFailed', '加载平台配置失败')}</p>}

        {!isLoading && !isError && category === 'security' && (
          <div className="divide-y rounded-md border">
            {readOnly.length === 0 ? (
              <p className="px-3 py-6 text-center text-sm text-muted-foreground">{t('settings.empty', '暂无配置项')}</p>
            ) : (
              readOnly.map((it) => <ReadOnlyRow key={it.key} item={it} />)
            )}
          </div>
        )}

        {!isLoading && !isError && category !== 'security' && (
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
            <Badge variant="outline">{t('settings.workerSide', 'Worker 侧生效')}</Badge>
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
        <Input
          value={value}
          onChange={(e) => onChange(e.target.value)}
          className="h-8 w-56"
        />
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

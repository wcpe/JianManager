/** 
 * @file SettingsPageView：系统设置页的受控视图（分类导航 + 分类面板 + 切分类未保存拦截），平台配置取数/保存与客户端偏好落盘由应用容器负责。
 * @input lib/settings-form（keyCategory 分类映射、diffSettings/hasUnsavedChanges 脏比对、validateSettingDraft/hasInvalidDraft 草稿校验）、
 *        Panel/PageShell/PageHeader/SettingsLayout/Dialog/Select/Input/Badge/FieldError 等原语、翻译上下文
 * @output SettingsPageView、SettingsPageViewProps、SettingsThemeMode、SettingsItemView、SettingsDataView、
 *          SettingsOutboundTestArgs
 * @sync apps/control-plane-web/src/pages/SettingsPage.tsx、apps/control-plane-web/src/pages/SettingsPage.dom.test.tsx
 * @since（组件受控化迁包；原页系统设置平台配置覆盖层未保存拦截与生效时机标注 +
 *调试模式开关出站连通性测试入口GitHub 令牌快捷创建）
 */
import { useState, type Dispatch, type ReactNode, type SetStateAction } from 'react'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import {
  Archive,
  Cpu,
  ExternalLink,
  FileCog,
  HeartPulse,
  Lock,
  Mail,
  Network,
  Palette,
  ScrollText,
  ShieldAlert,
  ShieldCheck,
  Gauge,
  type LucideIcon,
} from 'lucide-react'
import { cardSurface, cardSurfaceTone, cn } from '@jianmanager/ui'
import { Button } from '@jianmanager/ui/components/button'
import { Badge } from '@jianmanager/ui/components/badge'
import { FieldLabel } from '@jianmanager/ui/components/field-label'
import { Panel } from '@jianmanager/ui/components/panel'
import { PageHeader, PageShell, SettingsLayout } from '@jianmanager/ui/components/layout'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@jianmanager/ui/components/select'
import { keyCategory } from '@/lib/settings/settings-form'
import { SettingsFormView } from './SettingsFormView'
import {
  clearSubmitted,
  defaultSerializeKv,
  emptyDraft,
  hasChanges,
  type SettingItem as DraftSettingItem,
  type SettingsDraft,
} from '@/lib/settings/settings-draft'
import type { SettingCategory } from '@/lib/settings/settings-form'

/** 明暗偏好三态（与主题 store 的 ThemeMode 同形；组件库不 import 应用 store，故此处自持同名常量类型）。 */
export type SettingsThemeMode = 'light' | 'dark' | 'system'

/** 
 * GitHub 新建 fine-grained 个人访问令牌页：官方 URL 模板预填表单——
 * 名字、描述、永不过期（expires_in=none，不传则默认 30 天过期，届时 CP 同步会再次报错）；
 * 权限参数刻意留空即最小权限——fine-grained 令牌内建对所有公开仓库的只读访问，
 * 而本令牌唯一用途就是读公开仓库 releases 提升 API 限额。
 */
const GITHUB_TOKEN_NEW_URL = `https://github.com/settings/personal-access-tokens/new?name=JianManager&description=${encodeURIComponent(
  'JianManager 控制台专用：读取公开仓库 Releases 提升 GitHub API 限额（零权限即可）',
)}&expires_in=none`

const CATEGORY_ICON: Record<SettingCategory, LucideIcon> = {
  appearance: Palette,
  logging: ScrollText,
  logAcquisition: FileCog,
  runtime: Cpu,
  policy: HeartPulse,
  network: Network,
  traffic: Gauge,
  backup: Archive,
  email: Mail,
  security: Lock,
}

/** 取配置项的本地化标签，缺省回退键名本身。 */
function settingLabel(t: TFunction, key: string): string {
  return t(`settings.keys.${key}`, key)
}

/**
 * 把服务端快照映射成通用表单组件要的草稿模型条目。
 *
 * 【为什么 value 的落位要看 overridden】草稿模型的展示值是
 * `overridden ? override : platform`。服务端只给一个 `value`，
 * 所以"已覆盖"的项必须放进 override，否则 shownText 会返回空串。
 */
function toDraftItem(it: SettingsItemView): DraftSettingItem {
  return it.overridden
    ? { key: it.key, platform: '', override: it.value, overridden: true }
    : { key: it.key, platform: it.value, override: '', overridden: false }
}

/**
 * 从服务端快照里取某一项的权威生效时机。
 *
 * 【为什么必须从这里取而不是靠声明表猜】声明表（`FIELD_SPECS`）只登记了几个键，
 * 而 `/settings` 的每一项都带 `effectiveImmediately`——后端逐项给，是唯一权威来源。
 * 拿不到（分类外的键）时返回 undefined，由组件回落到声明表。
 */
function findEffectiveImmediately(data: SettingsDataView | undefined, key: string): boolean | undefined {
  const all = [...(data?.editable ?? []), ...(data?.readOnly ?? [])]
  return all.find((it) => it.key === key)?.effectiveImmediately
}

/** 
 * 单个平台配置项（本视图渲染所需的最小字段集）。
 *
 * 刻意只声明用到的字段，而非照搬应用侧 `@/api/settings` 的 `SettingItem`：容器直接传 API 返回的
 * 完整对象也结构兼容，无需把该 API 类型迁进包（同 `BackupStorageRow`、`ScheduleLogRow` 的取舍）。
 *
 * `value` 对敏感项**已由后端脱敏**（如 `invite.smtp.password` 的 `(已配置)`）——本视图任何位置都
 * 不得展示或回显明文；密钥类输入框仅以密码框 + `${ENV_VAR}` 占位提示收引用串。
 */
export interface SettingsItemView {
  /** 配置键，如 log.level、graceful_stop.timeout。 */
  key: string
  /** 当前生效值（DB 覆盖 > env > YAML）；敏感项已脱敏。 */
  value: string
  /** 是否可经 PUT /settings 运行时修改。 */
  editable: boolean
  /** 是否敏感项（值已脱敏）。 */
  sensitive: boolean
  /** 该项当前是否被 DB 覆盖（仅可编辑项有意义）。 */
  overridden: boolean
  /** 运行时修改是否在 Control Plane 内即时生效（否则需改配置/重启或在 Worker 侧生效）。 */
  effectiveImmediately: boolean
}

/** GET /settings 的视图数据（可编辑项 + 只读项分区）。 */
export interface SettingsDataView {
  /** 可运行时修改的项（落 logging/runtime/network/backup/email 分类）。 */
  editable: SettingsItemView[]
  /** 只读项（落 security 分类，视觉隔离）。 */
  readOnly: SettingsItemView[]
}

/** 
 * 出站连通性测试入口的插槽参数（该按钮自带 mutation，实现由外壳注入）。
 *
 * 本视图只报「在哪个分类里要测」：网络分类测出站代理（目标可自定义）、
 * 运行时分类测 JDK 下载源可达。测试目标与 mutation 是外壳策略，故视图不 import 该实现。
 */
export interface SettingsOutboundTestArgs {
  /** 请求测试入口的分类：network（出站代理）/ runtime（JDK 下载源）。 */
  category: 'network' | 'runtime'
}

/** 
 * 受控边界：**不取数、不发请求、不弹 toast**。
 * - 平台配置数据与加载/错误态经 props 注入（容器调 `useSettings()`）；
 * - 保存以 `onSave(changed)` 回调上报，由容器执行 mutation 并决定成功/失败文案：返回是否成功，
 *   视图据返回值决定是否清掉已落库的草稿键（失败时保留草稿便于修正重试）；
 * - **归容器**的受控状态：当前分类 `category`——它是「面板在看哪一组配置」这一筛选语义，
 *   即使切换后仍由同一份 `useSettings()` 数据驱动，其取值归属也留在外壳（同 `ScheduleFilter`）；
 * - **留本组件**的纯 UI 状态：各键的编辑草稿、待确认切换的目标分类（弹窗）、分类导航的未保存指示；
 *   草稿是「用户还没提交的输入」，不随应用运行时变化；
 * - 客户端偏好（主题 mode / 界面语言）的值与变更都经 props：`theme`/`onThemeChange` 由外壳接主题 store，
 *   `language`/`onLanguageChange` 由外壳接 i18n 落盘——组件库不持 store，也不自行切语言；
 * - 出站连通性测试按钮走 `renderOutboundTest` 插槽（自带取数与 mutation，包内不得 import）。
 */
export interface SettingsPageViewProps {
  /** 是否平台管理员：决定分类导航是否含平台配置分类（组件库不持鉴权状态，由外壳读角色后注入）。 */
  isPlatformAdmin?: boolean
  /** 当前分类（筛选语义，归容器持有）。 */
  category: SettingCategory
  /** 分类切换上报；当前分类有未保存草稿时本视图先拦截确认，确认放弃后才上报。 */
  onCategoryChange: (category: SettingCategory) => void
  /** 平台配置数据；未取到（加载中/出错）时为 undefined。 */
  data?: SettingsDataView
  /** 平台配置加载态。 */
  isLoading?: boolean
  /** 平台配置取数失败态（渲染错误提示，不渲染表单）。 */
  isError?: boolean
  /** 保存在途：禁用保存按钮并显示保存中文案。 */
  saving?: boolean
  /** 保存当前分类的有效改动集（仅含真正不同的键）；返回是否成功——成功才清掉对应草稿键。 */
  onSave: (changed: Record<string, string>) => Promise<boolean>
  /** 当前明暗偏好（外壳从主题 store 注入）。 */
  theme: SettingsThemeMode
  /** 明暗偏好变更上报（外壳落盘并套用）。 */
  onThemeChange: (theme: SettingsThemeMode) => void
  /** 当前界面语言（外壳从 i18n 注入）。 */
  language: 'zh' | 'en'
  /** 语言变更上报（外壳落盘并切换）。 */
  onLanguageChange: (language: 'zh' | 'en') => void
  /** 出站连通性测试入口插槽（网络/运行时分类各一处）；缺省则该分类不渲染测试入口。 */
  renderOutboundTest?: (args: SettingsOutboundTestArgs) => ReactNode
  /** 取数失败时的重试（外壳注入）。 */
  onRetry?: () => void
  /** 
   * AI 助手配置入口插槽。
   *
   * 走插槽而非在此处 import 路由：视图不认识 react-router，
   * 且该入口只有平台管理员可见——角色与路由都由外壳决定。
   */
  assistantEntry?: ReactNode
}

/** 
 * 系统设置页：内部侧边栏分类 + 右侧分类面板，套「设置表单」范式。
 * 外观/语言为客户端偏好（localStorage）；其余为服务端平台配置（DB 覆盖层）。
 *：切分类时若有未保存草稿则拦截确认；非即时项标注生效时机；security 项视觉隔离。
 * 草稿状态留在本组件（按键全局唯一），使切分类拦截可跨分类工作；平台配置分类仅平台管理员可见。
 */
export function SettingsPageView({
  isPlatformAdmin = false,
  category,
  onCategoryChange,
  data,
  isLoading = false,
  isError = false,
  saving = false,
  onSave,
  theme,
  onThemeChange,
  language,
  onLanguageChange,
  renderOutboundTest,
  assistantEntry,
  onRetry,
}: SettingsPageViewProps) {
  const { t } = useTranslation()
  /*
    可编辑项的本地草稿：仅存用户改动；展示值回退到后端当前生效值。

    【为什么是 SettingsDraft 而不是扁平的 Record<string,string>】通用表单组件把
    字符串字段与结构化字段（kv-pairs）分两条通道存：半填的成对行序列化后是空串，
    若和字符串挤在一张表里，就会被当成"无覆盖"，用户刚加的行会闪一下消失。
    设置页的「日志采集」分类里就有 `log.acq.path_overrides` 这种结构化字段，
    所以这里必须用两通道模型，不能图省事退回扁平表。
  */
  const [draft, setDraft] = useState<SettingsDraft>(emptyDraft())
  // 待确认切换的目标分类（非空时弹未保存拦截对话框）。
  const [pendingCat, setPendingCat] = useState<SettingCategory | null>(null)

  const categories: SettingCategory[] = isPlatformAdmin
    ? ['appearance', 'logging', 'logAcquisition', 'runtime', 'policy', 'network', 'traffic', 'backup', 'email', 'security']
    : ['appearance']

  // 当前分类的可编辑项（appearance 无平台项）。
  const currentEditable = (data?.editable ?? []).filter((it) => keyCategory(it.key) === category)
  const currentDirty = hasChanges(currentEditable.map(toDraftItem), draft, defaultSerializeKv)

  /** 切分类：当前分类有未保存草稿时先拦截，否则直接上报外壳。 */
  const requestSwitch = (next: SettingCategory) => {
    if (next === category) return
    if (currentDirty) {
      setPendingCat(next)
      return
    }
    onCategoryChange(next)
  }

  /** 确认放弃改动并切换：清掉当前分类草稿键，再上报外壳切到目标分类。 */
  const confirmSwitch = () => {
    if (pendingCat === null) return
    setDraft((d) => {
      const next: SettingsDraft = { text: { ...d.text }, kv: { ...d.kv } }
      for (const it of currentEditable) {
        delete next.text[it.key]
        delete next.kv[it.key]
      }
      return next
    })
    onCategoryChange(pendingCat)
    setPendingCat(null)
  }

  return (
    // 全量对齐：外壳与页头改用布局层原语。data-page 由 PageShell spread 透传，保持原值。
    // 双栏（分类导航 + 内容）改用 SettingsLayout：原手写 flex + w-44(176px) 与原型
    // 的 170px 不一致，且缺窄屏单列降级，本原语把这两项一并固定。
    // 壳态取 fixed：ARCHITECTURE 与原型 settingsPage() 的 .page fixed 都要求设置页
    // 固定视口、由内部区域自行滚动，使分类导航常驻、只有内容区滚动。
    <PageShell variant="fixed" data-page="settings">
      <PageHeader title={t('settings.title')} description={t('settings.subtitle')} />

      <SettingsLayout>
        {/* 内部侧边栏：分类导航。
            该元素是导航陆标（<aside> → complementary），换成 Card 渲染的 div 会丢掉语义，
            故保留元素、改取 Card 同源的表面常量：圆角/描边/底色/阴影由 packages/ui 一处决定
            （原 bg-card/80 → subtle 档，嵌套在设置页壳内的次级容器）。 */}
        <aside className={cn(cardSurface, cardSurfaceTone.subtle, 'p-2')}>
          <nav className="space-y-0.5">
            {categories.map((c) => {
              const Icon = CATEGORY_ICON[c]
              const dirtyHere = c === category && currentDirty
              return (
                // 分类导航项：整行可点、选中态靠淡染底色，视觉自绘，故取 invisible + auto。
                <Button
                  key={c}
                  type="button"
                  variant="invisible"
                  size="auto"
                  onClick={() => requestSwitch(c)}
                  className={cn(
                    'flex w-full items-center justify-start gap-2 rounded-md px-2.5 py-1.5 text-[13px] font-normal transition-colors',
                    category === c
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
                </Button>
              )
            })}
          </nav>
          {/* AI 助手配置单独成页（它管的是多实体 CRUD，不是键值设置），
              故在分类导航之外单列一个入口，避免把两种形态混进同一套表单。 */}
          {assistantEntry && <div className="mt-2 border-t border-border/60 pt-2">{assistantEntry}</div>}
        </aside>

        {/* 右侧：当前分类面板。fixed 壳态下外壳不再滚动，故内容区自行溢出滚动
            （安全/系统等分类的表单比一屏高）。 */}
        <div className="min-w-0 max-w-3xl flex-1 overflow-auto">
          {category === 'appearance' ? (
            <AppearanceSettings
              theme={theme}
              onThemeChange={onThemeChange}
              language={language}
              onLanguageChange={onLanguageChange}
            />
          ) : (
            <PlatformCategory
              category={category}
              data={data}
              isLoading={isLoading}
              isError={isError}
              draft={draft}
              setDraft={setDraft}
              saving={saving}
              onSave={onSave}
              renderOutboundTest={renderOutboundTest}
              {...(onRetry ? { onRetry } : {})}
            />
          )}
        </div>
      </SettingsLayout>

      {/*切分类未保存拦截确认 */}
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
    </PageShell>
  )
}

/** 外观分区：主题模式 + 语言。两者都为客户端偏好，值与变更全经 props（本组件不接 store / 不切语言）。 */
interface AppearanceSettingsProps {
  /** 当前明暗偏好。 */
  theme: SettingsThemeMode
  /** 明暗偏好变更上报。 */
  onThemeChange: (theme: SettingsThemeMode) => void
  /** 当前界面语言。 */
  language: 'zh' | 'en'
  /** 语言变更上报。 */
  onLanguageChange: (language: 'zh' | 'en') => void
}

/** 外观：主题模式 + 语言（客户端偏好）。主题色切换在侧栏，此处不重复。 */
function AppearanceSettings({ theme, onThemeChange, language, onLanguageChange }: AppearanceSettingsProps) {
  const { t } = useTranslation()
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
        {/* 可见标签是同级 <p>，故可访问名由触发器自身的 aria-label 承担（落在可聚焦元素上）。 */}
        <Select value={theme} onValueChange={(v: string) => onThemeChange(v as SettingsThemeMode)}>
          <SelectTrigger size="sm" className="w-36" aria-label={t('settings.theme')}>
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
        <Select value={language} onValueChange={(v: string) => onLanguageChange(v as 'zh' | 'en')}>
          <SelectTrigger size="sm" className="w-36" aria-label={t('settings.language')}>
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

/** 平台配置分类面板的受控契约：数据与草稿由页面视图提供，保存动作经 `onSave` 上报外壳。 */
interface PlatformCategoryProps {
  /** 要展示的分类（非 appearance）。 */
  category: SettingCategory
  /** 平台配置数据（外壳注入）。 */
  data?: SettingsDataView
  /** 加载态。 */
  isLoading: boolean
  /** 取数失败态。 */
  isError: boolean
  /** 全量草稿（按键唯一，跨分类共用一份；两通道：文本 + 结构化）。 */
  draft: SettingsDraft
  /** 草稿写入。 */
  setDraft: Dispatch<SetStateAction<SettingsDraft>>
  /** 保存在途。 */
  saving: boolean
  /** 保存改动集；返回是否成功。 */
  onSave: (changed: Record<string, string>) => Promise<boolean>
  /** 连通性测试入口插槽（仅网络/运行时分类使用）。 */
  renderOutboundTest?: (args: SettingsOutboundTestArgs) => ReactNode
  /** 取数失败时的重试（外壳注入）。 */
  onRetry?: () => void
}

/** 
 * 平台配置分类面板：security 展示只读项（视觉隔离），其余分类展示可编辑项 + 保存。
 * 数据/草稿由页面视图提供，切分类拦截在页面视图处理。
 */
function PlatformCategory({
  category,
  data,
  isLoading,
  isError,
  draft,
  setDraft,
  saving,
  onSave,
  renderOutboundTest,
  onRetry,
}: PlatformCategoryProps) {
  const { t } = useTranslation()

  const editable = (data?.editable ?? []).filter((it) => keyCategory(it.key) === category)
  const readOnly = (data?.readOnly ?? []).filter((it) => keyCategory(it.key) === category)

  const isSecurity = category === 'security'

  /*
    保存成功后清草稿：**只清仍是本次提交值的键**。
    通用表单组件只负责"提交什么"，清草稿是容器的职责（见 settings-draft 的 clearSubmitted）。
    整表清空会在 PUT 在途期间抹掉用户的新输入——输入框在保存时并不禁用。
  */
  const saveAndClear = async (values: Record<string, string>) => {
    const ok = await onSave(values)
    if (ok) {
      setDraft((d) => clearSubmitted(d, values, editable.map(toDraftItem), defaultSerializeKv))
    }
    return ok
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
      {isError && (
        <div className="flex items-center gap-2" data-testid="settings-load-error">
          <p className="text-sm text-destructive">{t('settings.loadFailed', '加载平台配置失败')}</p>
          {onRetry && (
            <Button variant="outline" size="sm" onClick={onRetry}>
              {t('common.retry')}
            </Button>
          )}
        </div>
      )}

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
          {category === 'network' ? (
            <div className="space-y-2">
              <p className="rounded-md bg-primary/10 px-3 py-2 text-xs text-muted-foreground">
                {t('settings.networkNotice')}
              </p>
              {/*
                这条是「敢不敢改」的关键：运维最怕配错代理把节点弄失联。
                事实是代理只作用于下载类 HTTP client，Worker↔CP 走 gRPC 不经过它。
              */}
              <p className="flex items-start gap-1.5 rounded-md border border-status-success/30 bg-status-success/5 px-3 py-2 text-xs text-muted-foreground">
                <ShieldCheck className="mt-0.5 size-3.5 shrink-0 text-status-success" aria-hidden />
                {t('settings.proxySafety')}
              </p>
            </div>
          ) : category === 'email' ? (
            <p className="rounded-md bg-primary/10 px-3 py-2 text-xs text-muted-foreground">
              {t('settings.emailNotice')}
            </p>
          ) : (
            <p className="text-xs text-muted-foreground">{t('settings.editableHint', '保存后立即覆盖默认值。')}</p>
          )}
          {/*
            字段渲染与保存统一走通用表单组件 SettingsFormView。
            设置页此前自带一套 EditableRow（switch/select/text 三个 if 分支 + 独立保存按钮），
            节点采集面板又有一套——同一件事三处实现，口径必然漂移。
            现在只有一套：字段类型由 `lib/settings/field-types` 声明表分派，
            校验只走 `validateSettingDraft`，保存按钮由组件自己出。
          */}
          <SettingsFormView
            items={editable.map(toDraftItem)}
            draft={draft}
            onDraftChange={setDraft}
            saving={saving}
            onSave={saveAndClear}
            labelOf={(key) => settingLabel(t, key)}
            effectiveImmediatelyOf={(key) => findEffectiveImmediately(data, key)}
            actionOf={(key) =>
              /*
                github.token 的「去 GitHub 创建」快捷按钮。
                从 EditableRow 迁到这里：通用组件不认识外部 URL，所以由本页注入。
                不预填 expires_in=none 的话令牌默认 30 天过期，届期 CP 同步会再次失败——
                这个细节是当初加按钮的原因，不能随重构丢掉。
              */
              key === 'github.token' ? (
                <Button
                  size="sm"
                  variant="outline"
                  type="button"
                  title={t('settings.githubTokenHint')}
                  onClick={() => window.open(GITHUB_TOKEN_NEW_URL, '_blank', 'noopener,noreferrer')}
                >
                  <ExternalLink className="mr-1 size-3.5" />
                  {t('settings.githubTokenCreate')}
                </Button>
              ) : null
            }
          />
          {/* 连通性测试：代理在网络分类测出站（目标可自定义）、JDK 下载源在运行时分类测可达。
              按钮本体自带取数与 mutation，故经插槽注入（组件库不 import 应用侧实现）。 */}
          {category === 'network' && (
            <div className="space-y-2 border-t pt-3">
              <p className="text-xs text-muted-foreground">
                {t('diagnostics.proxyTrafficHint', '经此出站代理的流量：JDK 一键下载、Worker 二进制/自更新拉取、探针依赖下载、客户端分发外呼与更新源检查等 CP/节点的对外下载与请求。')}
              </p>
              <p className="text-xs text-muted-foreground">{t('diagnostics.proxyTestHint', '填任意地址测试 CP 能否经当前出站代理访问外网（默认 https://www.google.com）。')}</p>
              {renderOutboundTest?.({ category: 'network' })}
            </div>
          )}
          {category === 'runtime' && (
            <div className="space-y-2 border-t pt-3">
              <p className="text-xs text-muted-foreground">{t('diagnostics.jdkSourceTestHint', '测试 JDK 下载源（foojay）是否可达。')}</p>
              {renderOutboundTest?.({ category: 'runtime' })}
            </div>
          )}
        </>
      )}
    </Panel>
  )
}

/** 单个只读配置项行：展示当前生效值；敏感项标注「已脱敏」。 */
function ReadOnlyRow({ item }: { item: SettingsItemView }) {
  const { t } = useTranslation()
  return (
    <div className="flex items-center justify-between gap-4 px-3 py-2">
      <div className="min-w-0">
        <div className="flex items-center gap-2">
          {/* 用 FieldLabel 而不是 <p>：它是真正的 label 元素，屏幕阅读器可关联到控件；
              设置项的控件在同行右侧，故用 aria-labelledby 关联而不是 htmlFor（无 id 可用）。 */}
          <FieldLabel id={`setting-label-${item.key}`} className="truncate">
            {settingLabel(t, item.key)}
          </FieldLabel>
          {item.sensitive && <Badge variant="outline">{t('settings.masked', '已脱敏')}</Badge>}
        </div>
        <p className="text-xs text-muted-foreground font-mono">{item.key}</p>
      </div>
      <code className="text-xs text-muted-foreground max-w-[14rem] truncate text-right">{item.value || '—'}</code>
    </div>
  )
}

export default SettingsPageView

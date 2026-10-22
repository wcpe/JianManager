import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Search } from 'lucide-react'
import { cardSurfaceTone, cn } from '@jianmanager/ui'
import { Tooltip, TooltipContent, TooltipTrigger } from '@jianmanager/ui/components/tooltip'
import { Kbd } from '@jianmanager/ui/components/kbd'
import { RefreshButton } from '@jianmanager/ui/components/refresh-button'
import { searchBoxClass } from '@/lib/shared/header-layout'

/**
 * 全局顶栏的三个展示部件（FR-502 阶段 6 补丁 / FR-179 / FR-232）。
 * 从应用侧 `ConsoleHeader` 拆出：store 与 react-query 依赖经 props 注入，此处只做渲染。
 */

/**
 * 顶栏品牌区（方案 C，见 ADR-071）：Logo + 折叠开关，整体作为折叠触发器复用 `toggleSidebar`
 * （展开态点击=收起、折叠态=展开，接管原侧栏 logo 的 FR-181 行为）。
 *
 * 本组件只排两个按钮，**列宽/内边距/右缘描边由 ui 包 `TopNav` 的品牌槽位与外壳 CSS 提供**：
 * 宽度经 CSS 绑定 `--sidebar-expanded-width`/`--sidebar-collapsed-width` 随侧栏同步收放
 * （ADR-071 的「左列一条连续竖线」），因此这里不能再自己写宽度类，否则会与同步规则打架。
 * 窄屏（<sm）侧栏隐藏，品牌列随之隐藏，顶栏回落为「工作区切换 + 操作区」满宽。
 */
export function ConsoleBrandSegment({
  collapsed,
  onNavigateHome,
}: {
  collapsed: boolean
  /** 点击品牌图标+文字跳转首页。 */
  onNavigateHome: () => void
}) {
  const { t } = useTranslation()

  return (
    <>
      {/* 品牌图标+文字现在是**首页入口**，不再兼任折叠开关（折叠由右侧独立按钮承担）。 */}
      <button
        type="button"
        onClick={onNavigateHome}
        aria-label={t('nav.home')}
        title={t('nav.home')}
        /*
          【为什么是 `w-fit` + 不做 `flex-1`/`justify-center`】这决定了收缩时图标会不会横移。

          旧实现：展开态 `flex-1`（吃掉整条品牌列宽）、折叠态 `justify-center`，标题用
          `{!collapsed && …}` 在点击瞬间从 DOM 移除。于是收缩那一刻 `flex-1` 的按钮突然失去内容、
          在 `min-w-0` 下被撑满，图标被甩到品牌列右侧（实测 left 16 → **109**）；等外层宽度收到位，
          它又被拉回左缘（109 → **13**），中间单帧跳 96px —— 这就是肉眼看到的「从右边闪现到左边」。

          改法：按钮宽度**由内容决定**（`w-fit`），图标永远贴左，位置与侧栏宽度无关；
          折叠态图标左缘必然会从 16 变到 13（品牌列 `px-4` 收成 `px-3` 的结果），
          但那是 3px 的内边距差、且随外层宽度连续变化，不再是 96px 的瞬跳。
          标题仍按状态渲染（折叠态本就不该保留文字节点）。

          注意：`py-1` 必须两种状态共有。它曾只写在展开分支里，折叠时高度从 36px 瞬跳到 28px，
          图标周边空间会「抽」一下 —— 同一个族系的毛病，这里一并钉死。
        */
        className={cn(
          'flex w-fit min-w-0 items-center rounded-md py-1 transition-colors hover:bg-accent/60',
          collapsed ? 'gap-0' : 'gap-2',
        )}
      >
        {/* logo 外框 28 → 24、图标 24 → 20：与 44px 顶栏等比收缩（上下各留 10px）。 */}
        <span className="grid size-6 shrink-0 place-items-center rounded-md border border-primary/15 bg-card shadow-soft">
          <img src="/brand/jianmanager-mark.svg" alt="" aria-hidden="true" className="size-5" />
        </span>
        {!collapsed && (
          <h2 className="min-w-0 flex-1 truncate text-left text-[13px] font-bold leading-[18px] tracking-tight text-foreground">JianManager</h2>
        )}
      </button>
      {/*
        【顶栏不再放折叠按钮】用户指定：折叠入口只留侧栏底部那一个。
        此前这里是 `{!collapsed && …}`——展开态有按钮、折叠态整块卸载，
        导致「缩回去就无法展开」；底部常驻开关补上后，顶栏这一处成了重复入口，故删除。
        连带影响：`onToggleSidebar` 在本组件已无用，但保留在 props 里会误导下一个人，
        因此一并从签名中移除（调用方 `ConsoleHeader` 同步调整）。
      */}
    </>
  )
}

/**
 * 靠右常驻搜索入口（FR-179 重排 + FR-241）：点击或 Ctrl/⌘+K 打开全局命令面板（`CommandPalette`），
 * 检索实例/节点/页面/操作并跳转。本身不再是输入框，仅作开面板的按钮（Ctrl+K 由面板全局监听）。
 * 由 FR-162 的居中铺满改为靠右固定上限宽度（`header-layout.searchBoxClass`），紧贴右侧操作图标；
 * 窄屏（<md）隐藏不挤垮工作区。
 */
export function ConsoleSearchBox({ onOpenPalette }: { onOpenPalette: () => void }) {
  const { t } = useTranslation()

  return (
    // `mr-[6px]` 照原型 `.global-search{margin-right:6px}`：操作区槽位间距是 5px，
    // 搜索入口与图标组之间要更松一档，否则两者会贴成一个整块。
    // `data-slot` 供外壳 CSS 在窄视口压缩本槽位（顶栏挤不下时先牺牲它，见 index.css）。
    <div data-slot="console-header-search" className={cn(searchBoxClass(), 'mr-[6px]')}>
      <button
        type="button"
        onClick={onOpenPalette}
        aria-label={t('header.searchPlaceholder')}
        // 这是输入样式的 <button> 控件（h-8 定高、圆角与阴影跟随控件规范），不是承载内容的卡片，
        // 因此不套 Card（其 flex-col/gap-4/py-4 与 rounded-xl 都要逐项覆盖，只会更脆）。
        // 但底色属于「卡片白盒」的同一语义轴，仍收敛到三档常量（原 bg-card/90 → subtle 档），
        // 消除 bg-card/90 这类任意透明度。
        className={cn(
          cardSurfaceTone.subtle,
          'flex h-8 w-full items-center gap-2 rounded-md border pl-2.5 pr-2 text-xs text-muted-foreground shadow-soft transition-colors hover:bg-muted/55',
        )}
      >
        <Search className="size-4 shrink-0" />
        <span className="min-w-0 flex-1 truncate text-left">{t('header.searchPlaceholder')}</span>
        {/* 键帽统一走 Kbd：`border` 是此处特有的描边诉求（原实现有），其余外观由组件提供。 */}
        <Kbd className="hidden border xl:inline-flex">Ctrl K</Kbd>
      </button>
    </div>
  )
}

/**
 * 全局刷新（FR-232）：重拉当前页所有活跃查询（invalidateQueries），转动图标给反馈。
 * 解决「页面无刷新入口」——不整页 reload，仅失效并重取数据。
 * 重取动作由应用侧注入（`queryClient.invalidateQueries()`），转动态与反馈延时留在本组件。
 */
export function ConsoleRefreshButton({ onRefresh }: { onRefresh: () => void | Promise<void> }) {
  const { t } = useTranslation()
  const [spinning, setSpinning] = useState(false)
  const refresh = () => {
    setSpinning(true)
    void Promise.resolve(onRefresh()).finally(() => {
      setTimeout(() => setSpinning(false), 500)
    })
  }
  return (
    // 纯文字提示的图标按钮 → 组件库 Tooltip（视觉一致、延迟可控）。
    // 原生 title 仍是 aria-label 之外的冗余，去掉不影响可访问名；Tooltip 的 Content
    // 会经 aria-describedby 关联到触发元素，读屏同样能拿到这句提示。
    <Tooltip>
      <TooltipTrigger asChild>
        {/*
          【为什么换成 RefreshButton】此处原本是包内 `RefreshButton` 的手写翻版
          （同样的图标 + 旋转 + 禁用 + 提示），两份实现各自演化必然分叉。
          改用它之后，旋转时长、禁用口径、在途语义都由组件库一处决定。
          `variant="ghost"` + `size="icon-sm"` 保持原先「无边框图标按钮」的观感。
        */}
        <span>
          <RefreshButton
            variant="ghost"
            pending={spinning}
            onClick={refresh}
            aria-label={t('header.refresh')}
            iconSize="size-4"
            className="text-muted-foreground hover:bg-accent/60 hover:text-foreground"
          />
        </span>
      </TooltipTrigger>
      <TooltipContent>{t('header.refresh')}</TooltipContent>
    </Tooltip>
  )
}

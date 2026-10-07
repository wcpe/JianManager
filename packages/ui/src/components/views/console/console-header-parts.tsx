import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { PanelLeftClose, RotateCw, Search } from 'lucide-react'
import { cn } from '@jianmanager/ui'
import { logoToggleLabelKey } from '../../../lib/sidebar-logo'
import { searchBoxClass } from '../../../lib/header-layout'

/**
 * 全局顶栏的三个展示部件（FR-496 阶段 6 补丁 / FR-179 / FR-232）。
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
  onToggleSidebar,
}: {
  collapsed: boolean
  onToggleSidebar: () => void
}) {
  const { t } = useTranslation()

  return (
    <>
      <button
        type="button"
        onClick={onToggleSidebar}
        aria-label={t(logoToggleLabelKey(collapsed))}
        title={t(logoToggleLabelKey(collapsed))}
        className={cn(
          // `py-1` 必须属于**两种状态共有**：先前它只写在展开分支里，折叠时随 `flex-1 gap-2` 一起消失，
          // 按钮高度随即从 36px 瞬跳到 28px（`transition-colors` 只过渡颜色、不过渡尺寸），
          // 于是点 logo 时图标周边空间会「抽」一下——这正是被反复报的「logo 点击缩放」。
          // 提到公共段后高度恒为 36px（28px 图标 + 上下各 4px），图标在两种状态下位置不动。
          'flex min-w-0 items-center rounded-md py-1 transition-colors hover:bg-accent/60',
          collapsed ? 'justify-center' : 'flex-1 gap-2',
        )}
      >
        <span className="grid size-7 shrink-0 place-items-center rounded-md border border-primary/15 bg-card shadow-soft">
          <img src="/brand/jianmanager-mark.svg" alt="" aria-hidden="true" className="size-6" />
        </span>
        {!collapsed && (
          <h2 className="min-w-0 flex-1 truncate text-left text-sm font-bold tracking-tight text-foreground">JianManager</h2>
        )}
      </button>
      {!collapsed && (
        <button
          type="button"
          onClick={onToggleSidebar}
          aria-label={t('nav.collapseSidebar')}
          title={t('nav.collapseSidebar')}
          // `ml-auto` 照原型 `.brand .iconbtn{margin-left:auto}`：收起按钮贴品牌列右缘。
          className="ml-auto grid size-7 shrink-0 place-items-center rounded-md text-muted-foreground transition-colors hover:bg-accent/60 hover:text-foreground"
        >
          <PanelLeftClose className="size-4" />
        </button>
      )}
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
        className="flex h-8 w-full items-center gap-2 rounded-md border bg-card/90 pl-2.5 pr-2 text-xs text-muted-foreground shadow-soft transition-colors hover:bg-muted/55"
      >
        <Search className="size-4 shrink-0" />
        <span className="min-w-0 flex-1 truncate text-left">{t('header.searchPlaceholder')}</span>
        <kbd className="hidden shrink-0 rounded border bg-background px-1.5 py-0.5 text-[10px] font-medium xl:inline-block">
          Ctrl K
        </kbd>
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
    <button
      type="button"
      onClick={refresh}
      disabled={spinning}
      aria-label={t('header.refresh')}
      title={t('header.refresh')}
      className="rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-accent/60 hover:text-foreground disabled:pointer-events-none disabled:opacity-60"
    >
      <RotateCw className={cn('size-4', spinning && 'animate-spin')} />
    </button>
  )
}

import * as React from 'react'

import { cn } from '../../lib/utils'

/**
 * 资源侧栏（FR-496 阶段 4）：把「资源导航」从路由入口里拆出来的固定侧栏。
 *
 * 原型的分工是本组件存在的理由：侧栏只做两件事——**定位资源**（节点/群组/收藏，
 * 见 `ResourceTree`）与**放少数几个高频入口**（集群总览、全部实例、节点）。
 * 其余功能入口全部收进顶栏的「全部功能」，因此这里绝不能再长出几十条路由——
 * 那正是原型要修的「侧栏被路由占满」。
 *
 * 四段（原型 `.sidebar` 的 flex 列）：
 *   `SideNavHead`      工作区名 + 状态 pill，回答「我现在在哪个工作区」
 *   `SideNavShortcuts` 固定入口，不参与滚动——切页时它不该跟着滚走
 *   `SideNavResource`  资源区（唯一可伸缩段）
 *   `SideNavBottom`    底部常驻入口，不参与滚动
 *
 * 折叠态由 `SideNav` 通过 context 下发，而不是让每个子组件自己接一个 `collapsed` 属性：
 * 折叠是整栏的状态，逐个透传既啰嗦又容易出现「父折叠了、子还展开着」的不一致。
 */
const SideNavCollapsedContext = React.createContext(false)

export function SideNav({
  collapsed = false,
  className,
  children,
  ...props
}: React.ComponentProps<'aside'> & {
  /** 折叠为图标栏（246px → 54px）。 */
  collapsed?: boolean
}) {
  return (
    <SideNavCollapsedContext.Provider value={collapsed}>
      <aside
        data-slot="side-nav"
        data-collapsed={collapsed || undefined}
        className={cn(
          'flex min-h-0 flex-col overflow-hidden border-r border-border bg-card',
          collapsed ? 'w-[54px]' : 'w-[246px]',
          className,
        )}
        {...props}
      >
        {children}
      </aside>
    </SideNavCollapsedContext.Provider>
  )
}

/**
 * 侧栏首段：工作区名 + 右侧 pill 徽章（原型 `.sidebar-head` / `.workspace-label`）。
 * 折叠态整段隐藏——54px 装不下文字，留着头也会被压成一团。
 *
 * 【为什么提供 interactive】本段此前是纯 `<div>` 标签，但它位于侧栏顶部、由「图标 + 文字 + 徽章」
 * 构成，形状与位置都在说「这是导航元素」——尤其 pill 写着「资源视图」，"视图"二字直接暗示
 * 可切换。用户于是会反复去点它，而它毫无反应（典型的可供性错配）。
 * 与其削弱视觉去对抗这个直觉，不如给它一个符合预期的行为：点击回到当前工作区落地页
 * （「标题回首页」是跨产品通用惯例）。
 *
 * 实现上**不用 `asChild`**：本组件的 `children` 语义是「标题文字」（被放进 `<span className="truncate">`），
 * 而不是「整个内容」，Slot 只会把 props 合并到它的直接子元素（内层布局 div）上，
 * 包不出 `<a>`。改为由调用方在外层包 `<Link className="group block">`，本组件只声明
 * 「我处在可点容器内」——`interactive` 负责补悬停反馈与手型光标，并让内层文字响应
 * 外层容器的 `group-hover`（内层自己设了 `text-muted-foreground`，不靠 group 传不进来）。
 */
export function SideNavHead({
  icon,
  pill,
  interactive = false,
  className,
  children,
  ...props
}: React.ComponentProps<'div'> & {
  /** 工作区语义图标。 */
  icon?: React.ReactNode
  /** 右端 pill 徽章（如「资源视图」）。 */
  pill?: React.ReactNode
  /** 处于可点容器（如外层 `<Link className="group">`）内时置 true。 */
  interactive?: boolean
}) {
  const collapsed = React.useContext(SideNavCollapsedContext)
  return (
    <div
      data-slot="side-nav-head"
      // `inert` 与视觉收起同步：grid 折叠只让高度归零，元素仍在 DOM 里，
      // 读屏与 Tab 仍会走到它（收起的内容不该被辅助技术读到，也不该可聚焦）。
      inert={collapsed ? true : undefined}
      className={cn(
        // 折叠态不再用 `hidden`（`display:none` 是瞬跳：侧栏还在慢慢收，标题已经"啪"地没了）。
        // 改用 `grid-template-rows: 1fr → 0fr` 收高度 + 同步淡出，与侧栏宽度过渡同一节拍（slow）。
        // 内层必须再包一个 `overflow-hidden`：grid 折叠靠子项裁切，否则内容会溢出到下面去。
        'grid transition-[grid-template-rows,opacity] duration-[var(--motion-duration-slow)] ease-ios',
        collapsed ? 'grid-rows-[0fr] opacity-0' : 'grid-rows-[1fr] opacity-100',
        interactive && 'cursor-pointer',
        className,
      )}
      {...props}
    >
      <div className="overflow-hidden">
        <div
          className={cn(
            'flex items-center gap-2 px-[15px] pb-[9px] pt-[18px] text-[11px] tracking-[.5px] text-muted-foreground',
            interactive && 'transition-colors duration-[var(--motion-duration-fast)] ease-ios group-hover:text-foreground',
          )}
        >
          {icon && <span className="flex size-[17px] shrink-0 items-center justify-center">{icon}</span>}
          <span className="truncate">{children}</span>
          {pill && (
            <span
              data-slot="side-nav-pill"
              // letter-spacing 在 pill 上归零：11px 的 +.5px 会给 9px 小徽章带来明显字距。
              className="ml-auto shrink-0 rounded-[3px] bg-muted px-[5px] py-px text-[9px] tracking-normal"
            >
              {pill}
            </span>
          )}
        </div>
      </div>
    </div>
  )
}

/**
 * 固定入口段（原型 `.main-shortcuts`）：集群总览 / 全部实例 / 节点。
 * 不参与滚动，因此这一段的 `nav-row` 数量应保持个位数。
 */
export function SideNavShortcuts({
  className,
  children,
  ...props
}: React.ComponentProps<'nav'>) {
  const collapsed = React.useContext(SideNavCollapsedContext)
  return (
    <nav
      data-slot="side-nav-shortcuts"
      className={cn(
        'border-b border-border/60',
        // 【padding 与 flex 基准都要参与过渡】折叠时这一段会从「固定高度的快捷入口段」
        // 变成「撑满剩余高度的图标段」（`shrink-0` → `flex-1`），同时 padding 由
        // `px-[10px] pb-[13px] pt-[3px]` 变为 `px-[6px] py-[10px]`。原先两者都是瞬跳的：
        // 侧栏宽度还在收窄，这一段已经先跳了一次高度，叠加起来就是"二次 resize"。
        // `flex-grow`/`flex-shrink`/`flex-basis` 都是可过渡属性。
        'transition-[padding,flex] duration-[var(--motion-duration-slow)] ease-ios',
        collapsed ? 'flex-1 px-[6px] py-[10px]' : 'shrink-0 px-[10px] pb-[13px] pt-[3px]',
        className,
      )}
      {...props}
    >
      {children}
    </nav>
  )
}

/**
 * 资源区（原型 `.resource-scroll` 所在段）：侧栏唯一可伸缩的一段。
 *
 * 这里只做「撑满剩余高度 + overflow-hidden」，真正的滚动容器在 `ResourceTree` 内部：
 * 原型的搜索框位于滚动区**之外**（`.resource-search{margin:0 14px 9px}`），
 * 因此滚动必须发生在树内部的列表上，否则搜索框会跟着列表一起滚走。
 */
export function SideNavResource({
  className,
  children,
  ...props
}: React.ComponentProps<'div'>) {
  const collapsed = React.useContext(SideNavCollapsedContext)
  return (
    <div
      data-slot="side-nav-resource"
      // 同头段：视觉收起（grid-rows 0fr）后元素仍在 DOM，需 `inert` 让它不可交互、
      // 也不被读屏读到。
      inert={collapsed ? true : undefined}
      className={cn(
        // 【为什么不用 `hidden`】`display: none` 不可过渡，折叠瞬间资源区整块消失，
        // 剩下几段要立刻重新分配高度——这是"二次 resize"里最明显的一次跳动。
        // 改用 grid-rows 0fr 收起高度 + 淡出，与侧栏宽度同节拍，高度是连续让出去的。
        'grid min-h-0 flex-1 transition-[grid-template-rows,opacity] duration-[var(--motion-duration-slow)] ease-ios',
        collapsed ? 'grid-rows-[0fr] opacity-0' : 'grid-rows-[1fr] opacity-100',
        className,
      )}
      {...props}
    >
      {/* grid 折叠靠子项裁切：没有这层 `overflow-hidden`，内容会溢出到下面的段落上。 */}
      <div className="flex min-h-0 flex-col overflow-hidden">{children}</div>
    </div>
  )
}

/**
 * 底部常驻入口段（原型 `.sidebar-bottom`）：群组与拓扑、多服工作台等。
 * 内层导航行比标准行矮一档（33px / 11px 字），避免底部区抢走资源区高度。
 */
export function SideNavBottom({
  className,
  children,
  ...props
}: React.ComponentProps<'div'>) {
  const collapsed = React.useContext(SideNavCollapsedContext)
  return (
    <div
      data-slot="side-nav-bottom"
      className={cn(
        'shrink-0 border-t border-border/60',
        // 【padding 必须参与过渡】它在折叠/展开时由 `p-[10px]` 变为 `p-[8px_6px]`。
        // 原先容器上没有任何 transition，这层 padding 是瞬跳的：底部区域的可用宽度会在
        // 侧栏宽度**还在过渡途中**就"啪"地缩一下（实测出现 132px → 42px 的单帧突跳），
        // 与侧栏本身的平滑收窄叠加起来就是"抖两下"。与侧栏宽度同节拍即可对齐。
        'transition-[padding] duration-[var(--motion-duration-slow)] ease-ios',
        collapsed ? 'p-[8px_6px]' : 'p-[10px]',
        // 底部段的尺寸收敛写在容器上：子行仍是无状态的 SideNavRow，不必知道自己在哪一段。
        '[&_[data-slot=side-nav-row]]:min-h-[33px] [&_[data-slot=side-nav-row]]:text-[11px]',
        className,
      )}
      {...props}
    >
      {children}
    </div>
  )
}

/**
 * 侧栏导航行（原型 `.nav-row`）：唯一一种行样式，固定入口与底部入口共用。
 *
 * 【为什么渲染 `<div>` 而不是 `<button>`】导航行的真实语义是**链接**，但本组件库不依赖路由、
 * 自己产不出 `<a href>`；而 `<a>` 里不允许嵌 `<button>`（interactive content 嵌套非法），
 * 所以只要本组件还渲染 button，调用方就永远包不出真链接——中键、Ctrl+点击、新标签页、
 * 「复制链接地址」全都拿不到，读屏软件也不会把它念成链接。此前正是卡在这一点上。
 *
 * 改为渲染 `<div>`，语义完全交给调用方在外层包的元素：
 *   - 真实导航处包 `<Link to=…>`（`WorkspaceNavRow` 即如此）→ 得到真 `<a href>`；
 *   - 静态展示处（组件博物馆预览）什么都不包。
 * 本组件只负责**行的外观与状态**（图标 / 计数 / 激活态 / 折叠态），因此不再接
 * `type` / `disabled`——那是 button 专属；也不再自己写 `aria-current`，它应落在真正可聚焦的
 * 元素上，由调用方设在它的 `<Link>` 上。`data-active` 保留：只驱动样式，不承载语义。
 *
 * 注意：`<div>` 自身不可聚焦，**键盘可达性由外层链接提供**。调用方若不包链接，
 * 该行就是纯展示、不参与键盘导航——这正是静态预览想要的。
 */
export function SideNavRow({
  icon,
  count,
  active,
  className,
  children,
  ...props
}: React.ComponentProps<'div'> & {
  /** 行首图标。 */
  icon?: React.ReactNode
  /** 行尾计数徽章；`0` 也会渲染（「0 个实例」是有意义的信息），数字按 zh-CN 千分位（与 `PageHeader` 同口径）。 */
  count?: React.ReactNode
  /** 当前所在项（仅驱动样式；`aria-current` 请由调用方设在可聚焦的链接上）。 */
  active?: boolean
}) {
  const collapsed = React.useContext(SideNavCollapsedContext)
  return (
    <div
      data-slot="side-nav-row"
      data-active={active || undefined}
      className={cn(
        'my-[2px] flex min-h-9 w-full items-center rounded-md text-left text-xs text-muted-foreground',
        // 【gap 与 padding 必须一起过渡】此前行上只有 `transition-colors`，`gap`/`padding`
        // 是瞬跳的：折叠瞬间 `px-[10px]→px-0`、`gap-[9px]→gap-0` 直接跳变，视觉上就是
        // 「炸」一下的撕裂感。纳入过渡并与侧栏宽度同节拍（slow + ease-ios），
        // 宽度、间距、内边距三者同步，才像一个整体动画。
        'transition-[background-color,color,gap,padding] duration-[var(--motion-duration-slow)] ease-ios hover:bg-muted hover:text-foreground',
        active && 'bg-accent font-[630] text-accent-foreground',
        // 【折叠态为什么不用 justify-center】`justify-content` **不参与过渡**（瞬跳）。
        // 折叠一开始它就立刻把「图标 + 尚未收窄的文字 + 徽章」当成一个整体居中，图标被推到偏左；
        // 等文字宽度过渡完毕，居中基准才变成「只剩图标」，图标于是"闪"回中间——
        // 这正是「缩放时图标先位移出去、再闪现居中」的来源。
        // 改用**对称内边距**让图标自然落中：行内宽 = 侧栏折叠宽 − nav 横向内边距(12px)，
        // 减掉图标 17px 后对半分即左右各需的 padding。`padding` 是可过渡属性，
        // 因此整个过程是连续滑入而非先错位再跳回；用 calc 而不是写死 12.5px，
        // 是为了让 `--sidebar-collapsed-width` 变化时自动跟随。
        // fallback 取 3.5rem：变量由外壳（`.jm-console-shell`）提供，组件博物馆的静态预览没有它，
        // 少了 fallback 整条 padding 会失效、图标直接贴左。
        collapsed
          ? 'gap-0 px-[calc((var(--sidebar-collapsed-width,3.5rem)-29px)/2)]'
          : 'gap-[9px] px-[10px]',
        className,
      )}
      {...props}
    >
      {icon && (
        <span className="flex size-[17px] shrink-0 items-center justify-center [&_svg]:size-[17px]">
          {icon}
        </span>
      )}
      {/* 文字不用 `hidden`（`display:none` 是瞬跳）：改为 opacity + max-width 同步过渡，
          随侧栏收窄被压掉、展开时反向。`min-w-0` 是 flex 项能收缩到 0 的前提，
          少了它 `max-w-0` 会被 `min-width:auto` 顶回去。 */}
      <span
        className={cn(
          'min-w-0 truncate transition-[opacity,max-width] duration-[var(--motion-duration-slow)] ease-ios',
          collapsed ? 'max-w-0 opacity-0' : 'max-w-full opacity-100',
        )}
      >
        {children}
      </span>
      {count !== undefined && (
        <span
          data-slot="side-nav-count"
          className={cn(
            'shrink-0 rounded-[3px] bg-muted px-[5px] font-mono text-[10px] leading-[18px]',
            // margin 同样纳入过渡：`ml-auto → ml-0` 若瞬跳，折叠瞬间图标会被横向弹一下。
            'transition-[opacity,max-width,padding,margin] duration-[var(--motion-duration-slow)] ease-ios',
            active && 'bg-card text-accent-foreground',
            // 【折叠态必须去掉 ml-auto】`margin-left: auto` 会吃掉行内的全部剩余空间：
            // 徽章宽度虽已收为 0，那 24px 外边距仍把图标顶向左侧（实测图标偏 -12px）。
            // 展开态才需要它把计数推到右端。
            collapsed ? 'ml-0 max-w-0 overflow-hidden px-0 opacity-0' : 'ml-auto max-w-full opacity-100',
          )}
        >
          {typeof count === 'number' ? count.toLocaleString('zh-CN') : count}
        </span>
      )}
    </div>
  )
}

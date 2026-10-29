import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Cpu, MemoryStick, Users, Zap, Play, Square, RotateCw } from 'lucide-react'

import { MiniBar } from '@jianmanager/ui/components/mini-bar'
import { Button } from '@jianmanager/ui/components/button'
import { Card } from '@jianmanager/ui/components/card'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
import { cn } from '@jianmanager/ui'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import { InstanceIdentityBadges } from '@/components/views/instances/InstanceIdentityBadges'
import { instanceStatusLevel, type StatusLevel } from '@jianmanager/ui'



/** 实例展示数据（本组件所需的最小结构；外壳传 InstanceInfo 会结构兼容）。 */
export interface InstanceWorktableCardInstanceView {
  id: number
  name: string
  status: string
  /** 类型标识（如 minecraft_java）：用于推导「插件服 / 代理 / 二进制」类别徽章。 */
  type: string
  /** 角色（backend / proxy / universal）：身份徽章里与类型并列展示，与表格身份列同源。 */
  role: string
  /** 游戏端口；0 表示未分配（不显示端口段）。 */
  serverPort: number
  /** 配置的内存上限（探针与容器上限都取不到时的分母回退）。 */
  memLimitMb?: number
  /** 状态原因（失败原因或搭建中提示，二者共用该字段）。 */
  statusReason?: string
  /** 
   * 大区 / 小区（由外壳从实例标签 `region:` / `zone:` 解析后注入）。
   *
   * 【为什么放在卡片而不是分组头】大区是虚拟分区、没有独立实体，做成折叠层级后
   * 两层组头把真正的信息压到屏幕下方。改为卡面左上角一行文字：既保留归属信息，
   * 又不占额外的行。任一为空时该段退化为另一项，两者都空则不渲染。
   */
  region?: string
  zone?: string
}

/** 实时指标（本组件所需的最小结构）。 */
export interface InstanceWorktableMetricsView {
  cpuPercent?: number
  memoryMb?: number
  /** JVM 堆上限（探针）；> 0 时优先作为内存条分母。 */
  heapMaxMb?: number
  onlinePlayers?: number
  tps?: number
  /** 在线数是否可得（探针/SLP/Query 任一有值）。 */
  playersAvailable?: boolean
  /** TPS 是否可得（仅探针）。 */
  probeAvailable?: boolean
}

/** 运行态漂移（本组件所需的最小结构）。 */
export interface InstanceWorktableDriftView {
  pid: number
  cmdline?: string
}

/** 
 * 卡片内「附加提示区」（失败原因 / 搭建中 / 运行态漂移）的最大高度（px）= 35。
 *
 * 两段提示各按一行（16.5）加段间距（2）计满；再多（如失败原因本身折了两行）就从这里裁掉——
 * 卡面只需把事实说清到两行以内，完整文本仍在各段的 `title` 里。
 *
 * 【为什么必须硬封】卡片实高 = 固定骨架 199.5 + 头部超出基础高（40）的部分，而不封顶时该区
 * 可到 4 行（66），卡片实高 267.5 会顶穿列表行盒（`CARD_ROW_HEIGHT` − 行间距 = 244）
 * 与下一行重叠。封到 35 后卡片实高上限 = 234.5，行盒内留有 9.5px 余量——「声明值 = 真实行盒」
 * 由构造保证，而不是靠「现有数据恰好不长」。
 */
export const CARD_EXTRA_MAX_HEIGHT = 35

/** 
 * 身份徽章槽位高度（px）：两行徽章。
 *
 * 为什么要固定：徽章数量随实例而变（无标签 / 只有类型 / 类型+角色+环境+两个标签），
 * 自然占位会让下面的节点、资源条、底栏逐卡错位，一排卡片看起来是斜的。
 * 固定成槽位后各分区对齐，视觉上等价于表格的「每列对齐」。
 */
export const CARD_IDENTITY_HEIGHT = 40

/** 
 * 实例工作台卡（§4.5 运行实体范式）。
 * 内嵌资源（CPU/内存条 + 玩家/TPS）+ 呼吸灯（运行时脉动）+ 启停/重启按钮；点名进控制台工作区。
 * 仅运行态拉实时指标（原先在卡内惰性 enable），停机卡不轮询、资源显「--」。
 *
 * 受控视图（ADR-103 b 范式）：不取数、不碰路由——三处「谁来做判断」的边界在此划清：
 * - **能力画像**（是否代理）由外壳判定后以 `isProxy` 注入，卡内不写死 `role === 'proxy'`；
 * - **搭建中**（provision 未终态）由外壳判定后注入，卡内据此禁用启动；
 * - **运行态漂移**由外壳判定后注入（卡面只说清事实，接管入口在「⋯」菜单里）；
 * - **路由跳转**由外壳注入 `onOpen`（列表页要自定义跳转，超级工作台则用默认深链）。
 * 状态光晕是纯展示映射（`instanceStatusGlowClass` 已随包），故留在卡内计算。
 */
export interface InstanceWorktableCardProps {
  /** 实例展示数据。 */
  inst: InstanceWorktableCardInstanceView
  /** 所属节点名（由列表统一解析后传入，避免卡内各自查节点表）。 */
  nodeName: string
  /** 是否为代理实例（外壳按能力画像判定）。 */
  isProxy?: boolean
  /** 是否搭建中（provision 未终态）：启动按钮硬性禁用并给提示。 */
  provisioning?: boolean
  /** 运行态漂移；无漂移为 undefined。 */
  drift?: InstanceWorktableDriftView
  /** 实时指标；未运行或未取到为 undefined。 */
  metrics?: InstanceWorktableMetricsView
  /** 启动在途（外壳按实例 id 比对后注入，避免别的卡的提交禁用本卡）。 */
  starting?: boolean
  /** 停止在途。 */
  stopping?: boolean
  /** 重启在途。 */
  restarting?: boolean
  /** 角色徽标元素（统一语义色，由页面渲染）。 */
  roleBadge: ReactNode
  /** 「⋯」次要操作菜单元素（标签/限额/克隆/删除，由页面渲染）。 */
  menu: ReactNode
  /** 打开实例控制台；外壳注入（默认深链或自定义跳转）。 */
  onOpen?: (id: number) => void
  /** 
   * 右键菜单入口：只上报**光标坐标**，不上报实例。
   *
   * 【为什么不传实例】本视图用的是自己的最小结构，而菜单需要完整领域对象；
   * 让视图往外递领域对象会把 api 层模型反向耦合进受控视图。改为接线层用闭包
   * 捕获 `inst`——视图只管「在哪儿右键了」，语义更窄也更难用错。
   */
  onContextMenu?: (x: number, y: number) => void
  onStart: () => void
  /** 停止。 */
  onStop: () => void
  /** 重启。 */
  onRestart: () => void
  /** 环境标签值（与表格身份列同源，用于渲染环境徽章）。 */
  env?: string
  /** 自由标签（与表格身份列同源）。 */
  freeTags?: string[]
  /** 身份徽章的显示开关：与表格列设置对齐（type/role/tags 三个可选列）。 */
  showType?: boolean
  showRole?: boolean
  showTags?: boolean
  /** 是否被批量选中（用户提出：卡片补齐多选）。 */
  selected?: boolean
  /** 
   * 勾选框/卡片被点选。
   *
   * `event` 用于读 Shift / Ctrl / Meta 修饰键——连选与点选的语义由外壳统一实现
   * （见 `applySelectionClick`），本视图只负责把修饰键如实上报，不自己算区间。
   */
  onToggleSelect?: (modifiers: { shiftKey: boolean; ctrlKey: boolean; metaKey: boolean }) => void
}

export function InstanceWorktableCard({
  inst,
  nodeName,
  isProxy = false,
  provisioning = false,
  drift,
  metrics,
  starting = false,
  stopping = false,
  restarting = false,
  roleBadge,
  menu,
  onOpen,
  onContextMenu,
  onStart,
  onStop,
  onRestart,
  env,
  freeTags = [],
  showType = true,
  showRole = true,
  showTags = true,
  selected = false,
  onToggleSelect,
}: InstanceWorktableCardProps) {
  const { t } = useTranslation()

  const running = inst.status === 'RUNNING'
  const stopped = inst.status === 'STOPPED' || inst.status === 'CRASHED'
  const level: StatusLevel = instanceStatusLevel(inst.status)

  const statusLabel = t(`instances.${inst.status.toLowerCase()}`, inst.status)
  const cpuPct = running ? (metrics?.cpuPercent ?? 0) : 0
  // 内存条分母：优先 JVM 堆上限（探针），其次容器内存上限（docker，经 heapMaxMb 承载），
  // 再次实例配置限额；均无则为 0（仅以绝对值标签呈现，不画占比）。
  const memMax = running && metrics ? (metrics.heapMaxMb && metrics.heapMaxMb > 0 ? metrics.heapMaxMb : (inst.memLimitMb ?? 0)) : 0
  const memMb = running && metrics ? (metrics.memoryMb ?? 0) : 0
  const memPct = memMax > 0 ? Math.min(100, (memMb / memMax) * 100) : 0
  // 标签用绝对值（docker/无探针实例 heapMaxMb 为 0 时，百分比恒 0 看不出占用，故统一显绝对内存）。
  const cpuLabel = running ? `${cpuPct.toFixed(cpuPct > 0 && cpuPct < 10 ? 1 : 0)}%` : '--'
  const memLabel = running && memMb > 0 ? fmtMem(memMb) : '--'
  // 447 可用性位：运行态缺测显示「不可用」，不再把 0 当「0 人在线」/0.0 TPS。
  // 停机态保留「--」（是明确的「不适用」，非缺测）。在线数任一来源（探针/SLP/Query）有值即显值。
  const unavailable = t('metrics.unavailable')
  const playersAvailable = running && (metrics?.playersAvailable ?? false)
  const playersLabel = !running ? '--' : playersAvailable ? String(metrics?.onlinePlayers ?? 0) : unavailable
  const tpsAvailable = running && (metrics?.probeAvailable ?? false)
  const tpsLabel = !running ? '--' : tpsAvailable ? (metrics?.tps ?? 0).toFixed(1) : unavailable

  // 大区 · 小区：两者都有时用「·」分隔；只有一个时直接显示那个；都没有则不渲染整行。
  const region = inst.region?.trim() ?? ''
  const zone = inst.zone?.trim() ?? ''
  const regionZoneLabel = region && zone ? `${region} · ${zone}` : region || zone

  return (
    /* 
      整块可点击（进入工作区）→ 保留原 onClick 语义与 interactive 的 hover 抬升；
      业务特性全部留在 className/children 里：
      - 状态光晕 `instanceStatusGlowClass`（伪元素画外圈，依赖容器自身圆角继承）；
      - `hover:shadow-lift` 悬停反馈；
      - 卡内各段自带 `mt-3` 节奏，故 gap-0 保持原纵向间距。
      注：`@/test/ui-details` 的守护测试断言本文件保留 hover:shadow-lift，勿删。
    */
    <Card
      interactive
      data-testid="instance-worktable-card"
      data-selected={selected ? 'true' : undefined}
      className={cn(
        /* 
          【必须固定高度】虚拟化按 `CARD_ROW_HEIGHT` 常量给每行定位，而卡片内容是自适应的：
          实测同一页里卡片实高在 169~277px 之间浮动（标签多、名称换行、状态原因多行的更高）。
          卡片若被内容撑高，就会盖住下一行——用户看到的「卡片重叠」就是这个。
          `h-full` 让卡片填满包裹层给定的行盒高度，`overflow-hidden` 裁掉超出部分：
          行盒由常量决定、卡片不再反向影响布局，结构上不可能重叠。
        */
        'group relative flex h-full shrink-0 cursor-pointer flex-col gap-0 overflow-hidden p-3',
        // 选中态用描边：卡片没有行高可挂，靠边框表达最省空间也最不易误读。
        selected && 'ring-2 ring-primary/60 ring-offset-1',
      )}
      onClick={(event) => {
        /* 
          修饰键语义交给外壳（applySelectionClick）：Ctrl/Cmd 切换单项、Shift 连选、
          无修饰键是「替换为单项」而不是「打开控制台」。
          为什么无修饰键不打开：卡片一旦支持多选，单击的首要含义就是选它（与表格行一致——
          表格里单击行也是选中，打开靠名称链接/双击）。保持两种视图手感一致。
        */
        if (selected || event.ctrlKey || event.metaKey || event.shiftKey) {
          onToggleSelect?.({
            shiftKey: event.shiftKey,
            ctrlKey: event.ctrlKey,
            metaKey: event.metaKey,
          })
          return
        }
        if (onToggleSelect) {
          // 支持选择时，单击 = 选中（与表格行一致），打开走「⋯」或双击。
          onToggleSelect({ shiftKey: false, ctrlKey: false, metaKey: false })
          return
        }
        onOpen?.(inst.id)
      }}
      onDoubleClick={() => onOpen?.(inst.id)}
      onContextMenu={
        onContextMenu
          ? (event) => {
              // 阻止浏览器原生菜单，避免与本菜单叠出两套（与列表行同款处理）。
              event.preventDefault()
              onContextMenu(event.clientX, event.clientY)
            }
          : undefined
      }
    >
      {/*
        勾选框：卡片视图补齐多选（用户提出）。放右上角且 `stopPropagation`——
        否则点勾选会同时触发卡片的 onClick，变成「勾上又立刻按修饰键逻辑取消」。
      */}
      {onToggleSelect && (
        <div className="absolute right-2 top-2 z-10" onClick={(e) => e.stopPropagation()}>
          <Checkbox
            checked={selected}
            onCheckedChange={() => onToggleSelect({ shiftKey: false, ctrlKey: false, metaKey: false })}
            aria-label={t('instanceBatch.selectOne', { name: inst.name, defaultValue: '选择 {{name}}' })}
          />
        </div>
      )}
      {/* 左上角：大区 · 小区。大区是虚拟分区，不做折叠层级，只以一行小字交代归属；
          两者都空时不占位（不留空行，卡片高度由构造不变）。 */}
      {regionZoneLabel && (
        <div
          data-testid="instance-card-region-zone"
          className="mb-2 truncate text-micro text-muted-foreground"
          title={regionZoneLabel}
        >
          {regionZoneLabel}
        </div>
      )}
      {/* 头部：名称 + 状态徽章 + 菜单。去掉 40px 图标块——
          卡片每行放 4 张后，图标块占掉近 1/6 宽度却不携带信息（状态已由徽章表达）。 */}
      <div className="flex items-center gap-2">
        <div className="min-w-0 flex-1">
          {/* 卡片标题即进详情入口：外观是纯标题文字，取 invisible + auto。
              onClick 内部 stopPropagation 阻止冒泡到卡片自身的选中逻辑，原样保留。 */}
          <Button
            type="button"
            variant="invisible"
            size="auto"
            className="block max-w-full truncate text-left text-sm font-semibold hover:text-primary"
            onClick={(e) => {
              e.stopPropagation()
              onOpen?.(inst.id)
            }}
            title={inst.name}
          >
            {inst.name}
          </Button>
          <div className="mt-0.5 flex items-center gap-1.5">
            <StatusBadge
              level={level}
              label={statusLabel}
              pulse={inst.status === 'STARTING' || inst.status === 'STOPPING'}
              className="bg-transparent px-0 py-0"
            />
          </div>
          {/* 附加提示区（失败原因 / 搭建中 / 运行态漂移）——整区高度硬封在 CARD_EXTRA_MAX_HEIGHT
              以内：两段 `line-clamp-2` 同时出现时，不封顶的卡片实高会顶穿行盒、与下一行重叠
              （行高口径见 InstanceCardViews 的 CARD_ROW_HEIGHT 说明）。
              失败原因非空即显、不看 status：Worker 心跳会把 CRASHED 冲回 STOPPED，
              以 CRASHED 为前置条件时原因随之不可见；搭建中的 reason 是进行时状态而非失败，
              故只走琥珀行、不落红。运行态漂移只说清事实，接管入口在「⋯」菜单。 */}
          {/*
            【固定高度槽位，不再条件渲染】
            原来整块用 `(statusReason || drift) && ...` 包着：无提示时这块**完全不占高**，
            有提示时占 1~2 行。结果提示区下面的「身份徽章」上下浮动——同一排卡片里，
            有错误原因的那张（如 creative-1）徽章被顶到下一行，与其它卡对不齐。
            表格模式天然对齐是因为每列独立；卡片只能靠**固定槽位**换来同款整齐。

            现在无论有没有提示都占 `CARD_EXTRA_MAX_HEIGHT`：
            有内容时最多两行、超出裁剪；无内容时留空。徽章因此恒在同一水平线。
          */}
          <div
            className="mt-0.5 shrink-0 overflow-hidden"
            style={{ height: CARD_EXTRA_MAX_HEIGHT }}
            data-testid="instance-card-extra"
          >
            {inst.statusReason && (
              <p
                className={cn('line-clamp-2 text-meta', provisioning ? 'text-status-warning' : 'text-status-danger')}
                title={inst.statusReason}
              >
                {inst.statusReason}
              </p>
            )}
            {drift && (
              <p className="mt-0.5 line-clamp-2 text-meta text-status-warning" title={drift.cmdline}>
                {t('serverConsole.runtimeDriftDesc', { pid: drift.pid })}
              </p>
            )}
          </div>
        </div>
        {roleBadge}
        {/* 次要操作菜单（⋯）不冒泡到卡片，避免点菜单误开工作区（FIX-9）。 */}
        <span onClick={(e) => e.stopPropagation()}>{menu}</span>
      </div>

      {/*
        【身份徽章与表格对齐】原来这里显示 `inst.type` 的原始值（minecraft_java 之类），
        而表格的身份列显示「本地化类型徽章 + 角色 + 环境 + 标签」——同一台实例两处对不上。
        现在共用 `InstanceIdentityBadges`，文案、配色、标签截断规则完全一致。
      */}
      {/*
        身份徽章同样固定槽位：徽章数量因实例而异（有的无标签、有的带环境+标签），
        若让它们自然占位，下面的「节点:端口 / 资源条 / 底栏」会随之上下错位。
        固定两行高度（`CARD_IDENTITY_HEIGHT`）后，一排卡片的所有分区都落在同一水平线，
        达到与表格「每列对齐」相同的整齐度。
      */}
      <div
        className="mt-2 shrink-0 overflow-hidden"
        style={{ height: CARD_IDENTITY_HEIGHT }}
        data-testid="instance-card-identity"
      >
        <InstanceIdentityBadges
          type={inst.type}
          role={inst.role}
          env={env}
          freeTags={freeTags}
          showType={showType}
          showRole={showRole}
          showTags={showTags}
          layout="wrap"
        />
      </div>
      {/* 节点:端口单独一行（表格里它是独立一列，卡片没有列，靠这行承载） */}
      <div className="mt-1 truncate text-xs text-muted-foreground" title={`${nodeName}${inst.serverPort > 0 ? `:${inst.serverPort}` : ''}`}>
        {nodeName}
        {inst.serverPort > 0 && <span className="tabular-nums">:{inst.serverPort}</span>}
      </div>

      {/* 内嵌资源条：CPU / 内存（仅运行态有值，否则空轨） */}
      <div className="mt-2 shrink-0 space-y-1">
        <ResourceLine icon={<Cpu className="size-3" />} label={t('nodes.cpu')} pct={cpuPct} active={running} valueLabel={cpuLabel} />
        <ResourceLine icon={<MemoryStick className="size-3" />} label={t('nodes.memory')} pct={memPct} active={running} valueLabel={memLabel} />
      </div>

      {/* 玩家 / TPS + 启停按钮 */}
      <div className="mt-2 flex items-center gap-2 border-t pt-2">
        <span className="inline-flex items-center gap-1 text-sm font-semibold text-primary">
          <Users className="size-3.5" />
          <span className={cn('tabular-nums', running && !playersAvailable && 'text-xs font-normal text-muted-foreground')}>
            {playersLabel}
          </span>
        </span>
        {!isProxy && (
          <span className="inline-flex items-center gap-1 text-sm text-muted-foreground">
            <Zap className="size-3.5" />
            <span className={cn('tabular-nums', running && !tpsAvailable && 'text-xs')}>
              {tpsLabel}
            </span>
          </span>
        )}
        <div className="ml-auto flex items-center gap-1" onClick={(e) => e.stopPropagation()}>
          {stopped && (
            // 禁用按钮带 disabled:pointer-events-none，tooltip 由外层 span 承载。
            <span title={provisioning ? t('instances.provisioningBlocked') : undefined}>
              <Button
                variant="ghost"
                size="icon-xs"
                disabled={provisioning || starting}
                onClick={(e) => {
                  // 阻止冒泡：卡片根 onClick 是「打开实例控制台」，动作按钮不该顺带跳页。
                  e.stopPropagation()
                  onStart()
                }}
                aria-label={t('instances.start')}
                title={provisioning ? t('instances.provisioningBlocked') : t('instances.start')}
                className="text-status-success hover:text-status-success"
              >
                <Play className="size-3.5" />
              </Button>
            </span>
          )}
          {running && (
            <>
              <Button
                variant="ghost"
                size="icon-xs"
                disabled={restarting}
                onClick={(e) => {
                  e.stopPropagation()
                  onRestart()
                }}
                aria-label={t('instances.restart')}
                title={t('instances.restart')}
                className="text-status-info hover:text-status-info"
              >
                <RotateCw className="size-3.5" />
              </Button>
              <Button
                variant="ghost"
                size="icon-xs"
                disabled={stopping}
                onClick={(e) => {
                  e.stopPropagation()
                  onStop()
                }}
                aria-label={t('instances.stop')}
                title={t('instances.stop')}
                className="text-status-warning hover:text-status-warning"
              >
                <Square className="size-3.5" />
              </Button>
            </>
          )}
        </div>
      </div>
    </Card>
  )
}

/** 卡内单条资源行：图标 + 标签 + MiniBar（停机时空轨 + 「--」）。
 * valueLabel 覆盖右侧数值文案（如内存显绝对值、CPU 小数）；缺省回退百分比。 */
function ResourceLine({
  icon,
  label,
  pct,
  active,
  valueLabel,
}: {
  icon: ReactNode
  label: string
  pct: number
  active: boolean
  valueLabel?: string
}) {
  return (
    <div className="flex items-center gap-2">
      <span className="flex w-12 shrink-0 items-center gap-1 text-micro text-muted-foreground">
        {icon}
        {label}
      </span>
      <MiniBar value={active ? pct : 0} className="flex-1" />
      <span className="w-12 shrink-0 text-right text-micro tabular-nums text-muted-foreground">
        {active ? (valueLabel ?? `${pct.toFixed(0)}%`) : '--'}
      </span>
    </div>
  )
}

/** 内存 MiB → 人类可读（≥1024 显 GB 一位小数，否则整 MB）。 */
function fmtMem(mb: number): string {
  if (mb >= 1024) return `${(mb / 1024).toFixed(1)}G`
  return `${Math.round(mb)}M`
}

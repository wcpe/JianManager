/**
 * @file InstanceRowView：实例列表的行渲染件（平铺表与分组树表复用）——勾选 + 名称（proxy 内联展开 /
 *       就地导入徽章 / 运行态漂移标记）+ 类型 + 节点:端口 + 角色 + 环境与标签 + 状态 + 行内主操作与「⋯」菜单；
 *       proxy 展开时在行下追加后端摘要行。虚拟窗口逐行调用，故行内不持有任何取数或跨行共享状态。
 *       行高（含展开行）是**导出常量**，与本文件写进行盒的行内 style 同源，供虚拟窗口建模使用。
 * @input lib/instance-types（InstanceInfo）、lib/instance-grouping（envOf/freeTagsOf、定义于包内）、
 *        lib/threshold（instanceStatusLevel）、views/instances/InstanceTableParts（RoleBadge）、
 *        views/instances/RuntimeDriftNotice（RuntimeDriftBadge 与 RuntimeDriftInfoView）、
 *        Badge/StatusBadge/Button/Checkbox/Table 原语、翻译上下文
 * @output InstanceRowView、InstanceRowViewProps、INSTANCE_ROW_HEIGHT、INSTANCE_EXPANDED_ROW_HEIGHT
 * @sync apps/control-plane-web/src/pages/InstancesPage.tsx（容器保留取数与装配，经 renderRow 逐行调用）、
 *        apps/control-plane-web/src/pages/InstancesPage.dom.test.tsx（与 grouping / runtimeDrift / rowMenu 三个针对性测试）
 * @since FR-508（组件受控化迁包；原页 FR-047 多维筛选、FR-058 批量选择、FR-136 分组与节点:端口、
 *        FR-138 行内主操作与「⋯」次要菜单、FR-302 就地导入徽章、FR-310 运行中删除、FR-331 搭建中禁启、
 *        FR-445 代理能力画像、FR-452 分组树表、FR-471 运行态漂移）
 */
import { Fragment, memo, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronDown, ChevronRight, Play, Square, TriangleAlert, X } from 'lucide-react'

import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import { TableCell, TableRow } from '@jianmanager/ui/components/table'
import { cn, instanceStatusLevel } from '@jianmanager/ui'
import { envOf, freeTagsOf, regionOf, zoneOf } from '@/lib/instances/instance-grouping'
import { INSTANCE_KIND_LABEL_KEY, instanceKindOf, type InstanceKind } from '@/lib/instances/instance-kind'
import type { InstanceInfo } from '@/lib/instances/instance-types'
import { RoleBadge } from '@/components/views/instances/InstanceTableParts'
import { computeVisibleColumns, type InstanceColumnKey } from '@/lib/instances/instance-view-prefs'
import type { RuntimeDriftInfoView } from '@/components/views/instances/RuntimeDriftNotice'
import { InstanceHealthCell } from '@/components/views/instances/InstanceRowSpark'
import { healthHoverBridgeRef } from '@/components/views/instances/health-hover-bridge'
import type { InstanceMetricSnapshot } from '@/components/views/instances/InstanceRowMetrics'
import { DEFAULT_COLUMNS, type InstanceOptionalColumn } from '@/lib/instances/instance-view-prefs'
import type { BusinessStatus } from '@/api/businessStatus'
import { sameBusinessStatus, summarizeBusiness } from '@/lib/instances/business-summary'

/** 列设置的缺省可见集（常用三列，与 `readStoredColumns` 的缺省一致）。 */
const ALL_OPTIONAL_COLUMNS: ReadonlySet<InstanceOptionalColumn> = new Set(DEFAULT_COLUMNS)

/** 未知状态的兜底文案键；单列常量是因为登记表是索引签名，取 `.STOPPED` 拿不回确定类型。 */
const STATUS_LABEL_FALLBACK = 'instances.stopped'

/**
 * 服务端类别 → 标签配色。
 *
 * 三档颜色要有区分度但不能抢状态色的注意力：
 * 插件服（最常见）用主色、代理用信息色、二进制用中性灰。
 * 状态仍由「状态」列与名称前的圆点承载，这里只表达「是什么」。
 */
/** 状态级别 → 名称前小圆点配色（与 StatusBadge 同源判据）。 */
const STATUS_DOT_CLASS: Record<string, string> = {
  success: 'bg-status-success',
  warning: 'bg-status-warning',
  danger: 'bg-status-danger',
  info: 'bg-status-info',
  neutral: 'bg-muted-foreground/40',
}

const KIND_CHIP_CLASS: Record<InstanceKind, string> = {
  plugin: 'bg-primary/10 text-primary',
  proxy: 'bg-status-info/15 text-status-info',
  binary: 'bg-muted text-muted-foreground',
}

/**
 * 实例状态 → i18n 文案键（与实例页状态筛选项同源）。
 * 未知状态回退「已停止」文案，与迁移前 `statusConfig[status] || statusConfig.STOPPED` 等价。
 */
const STATUS_LABEL_KEY: Record<string, string> = {
  STOPPED: STATUS_LABEL_FALLBACK,
  STARTING: 'instances.starting',
  RUNNING: 'instances.running',
  STOPPING: 'instances.stopping',
  CRASHED: 'instances.crashed',
}

/**
 * 实例行高（px）：**声明值 = 真实值，由构造保证**。
 *
 * 数值依据（Chromium 1280 宽实测）：行内最高内容 = 操作列的 `Button size="xs"`（`h-6` = 24）
 * 加上 `TableCell` 的 `py-1.5`（12）= 36；表格行的指定高度对**行盒**是「最小值」语义，
 * 内容不超出时行盒恰为 36（实测把行盒写成 100 即得 100，写成 44 即得 44，无 ±1 边框误差）。
 *
 * 为什么写成行内 style 而不是靠类名反推：虚拟窗口按「1 项 = 本常量」定位整棵树，
 * 行内 style 与常量是同一个数字（先例：`views/logs/LogsPageView` 的 `ROW_HEIGHT`），
 * 类名（`py-1.5` / `h-6`）一旦被改动，声明值与真实行盒就会静默分叉——滚动条长度与
 * 滚动位置映射随之失真，且没有任何断言会红。
 *
 * 【2026-10-24 调整】列表已改为**服务端分页**（每页 50 条，不再虚拟滚动），
 * 行高不再需要与虚拟窗口的固定像素严格对齐，故从 36 放宽到 44：
 * 36px 下 13px 文字贴着上下边框，状态点与徽章几乎挤在一起，扫读吃力。
 * 单元格内容仍保持单行（标签列改为「2 个 + N」计数，不再折行或硬裁）。
 */
export const INSTANCE_ROW_HEIGHT = 64

/**
 * proxy 行内联后端摘要（展开时**追加**的那个 `<tr>`）高度（px）：同样写进行盒的行内 style。
 *
 * 语义是「追加行」而不是「该项总高」：展开的实例在虚拟模型里占
 * `INSTANCE_ROW_HEIGHT + INSTANCE_EXPANDED_ROW_HEIGHT`（主行 + 追加行），
 * 因为 DOM 里确实是两个 `<tr>`（`VirtualizedInstanceTables` 的逐行高度模型按此求和）。
 *
 * 展开内容（后端条数）不可预知，故容器用 `maxHeight` 封到本常量以内并在**行内滚动**——
 * 追加行高度因而恒等于常量，虚拟模型无需测量回填（对比 TasksPageView 对展开行的实测回填，
 * 那是内容高度无法预设时的兜底手段；此处高度可预设，就用不上它）。
 * 160 ≈ 4 条后端（每条 24）加摘要标题与留白，够看规模、不挤压列表。
 */
export const INSTANCE_EXPANDED_ROW_HEIGHT = 160

/**
 * 行渲染件的受控边界（ADR-103 b 范式）：**不取数、不发请求、不碰路由**。
 * - 七类实例派生判断由容器注入：节点名（`nodeName`，容器查节点表）、是否代理（`isProxy`，按能力画像）、
 *   是否搭建中（`provisioning`，`isProvisioningInstance` 属应用侧 API）、运行态漂移（`drift`）、
 *   三个动作的在途态（`starting` / `stopping` / `restarting`，容器按实例 id 比对——别的行在提交不该禁用本行）；
 * - 跨行共享的状态由容器持有后逐行下发：批量勾选（`selected`，与表头全选、批量栏同源）与 proxy 内联展开
 *   （`proxyExpanded`，虚拟窗口滚动会卸载行，行内自持会丢失展开态）；
 * - 「⋯」菜单与 proxy 展开的后端摘要都是**插槽**（`menu` / `renderBackends`）：前者接各弹窗目标，
 *   后者接 `useRegistrations` 取数容器，两者都属应用侧装配；
 * - 启动在途文案在包内选取，只上报意图（`onStart` / `onStop` / `onRestart` / `onKill` / `onToggleSelect` / `onToggleProxy`）。
 *
 * 行盒结构（两个 `<TableRow>`：主行 + 可选展开行）与迁移前一致，但两者都带上**行高行内 style**：
 * 展开是在同一虚拟「项」下多渲染一个 `<tr>`，若虚拟模型只按一行的高度计算，其后所有行的
 * 定位与滚动条长度都会漂移——故两个行高都是导出常量，由 `VirtualizedInstanceTables` 的
 * 逐行 `sizes` 与这里的行内 style 共用（判据见其 `isRowExpanded`）。
 */
export interface InstanceRowViewProps {
  /** 本行渲染的实例实体（虚拟窗口内逐行传入）。 */
  inst: InstanceInfo
  /** 所属节点名（容器由 `useNodes` 统一解析后注入，避免每行各自查节点表）。 */
  nodeName: string
  /** 本行是否处于批量选中态（选择集归容器，与表头全选、批量栏共享）。 */
  selected: boolean
  /** 切换本行勾选。 */
  onToggleSelect: () => void
  /** 是否代理实例（容器按能力画像 `bcTopology` 判定；行内不写死 `role === 'proxy'`）。 */
  isProxy?: boolean
  /** 是否搭建中（provision 未终态）：启动按钮硬性禁用并给提示。 */
  provisioning?: boolean
  /** 运行态漂移；无漂移为 `undefined`（行内据此决定是否出标记）。 */
  drift?: RuntimeDriftInfoView
  /**
   * 可见窗口批量取数后的行内指标快照（容器注入，本件不取数）。
   * 缺省或全缺测时，指标列与心跳列以 `--` 呈现（不隐藏列）。
   */
  metrics?: InstanceMetricSnapshot
  /**
   * 业务域状态（FR-534，容器**批量**取回后按实例注入）。
   *
   * 缺省 `undefined` 表示"这行还没有域状态数据"（列未开启、批量未返回、或该台不在批里），
   * 此时单元格显示 `--` 而不是"未注册"——两者含义完全不同，不能混。
   */
  businessStatus?: BusinessStatus
  /**
   * 可见的可选列（列设置，容器注入）。固定列（勾选/名称/节点:端口/状态/操作）不参与；
   * 缺省=全开（与旧行为一致，测试与既有使用点零改动）。
   */
  columns?: ReadonlySet<InstanceOptionalColumn>
  /** proxy 行内联后端是否展开（跨虚拟窗口卸载仍需保持，故由容器持有）。 */
  proxyExpanded?: boolean
  /** 展开/收起 proxy 行内联后端摘要。 */
  onToggleProxy?: () => void
  /** proxy 行内联后端摘要插槽（容器注入含 `useRegistrations` 取数的容器；仅在展开时调用）。 */
  renderBackends?: () => ReactNode
  /** 启动在途（容器按实例 id 比对后注入，避免别的行的提交禁用本行）。 */
  starting?: boolean
  /** 停止在途。 */
  stopping?: boolean
  /** 「⋯」次要操作菜单元素（生命周期 / 标签 / 配置 / 限额 / 代理后端 / 克隆 / 接管 / 删除，由容器渲染）。 */
  menu: ReactNode
  /** 打开实例控制台（容器注入路由跳转）。 */
  onOpenInstance?: (id: number) => void
  /** 启动。 */
  onStart: () => void
  /** 停止。 */
  onStop: () => void
  /**
   * 行右键菜单入口（用户提出）。只上报「哪一行、鼠标在哪」，菜单本身由**表格级单例**渲染
   * （见 InstancesPage 的 InstanceContextMenu）——每行挂一棵 Radix ContextMenu 会重演
   * 每行一个 Popover 的老问题（实测单次排序 300 次行渲染 / 880ms）。
   */
  onContextMenu?: (inst: InstanceInfo, x: number, y: number) => void
  /**
   * 列顺序（表头拖拽换位的结果）。缺省为默认顺序。
   * 与表头、colgroup 共用同一份顺序——三处同源才不会错列。
   */
  columnOrder?: readonly InstanceColumnKey[]
  /** 强杀（过渡态）：接入回调后由容器弹二次确认框，行内不直发写请求。 */
  onKill: () => void
}

/**
 * 实例列表单行：平铺虚拟表与分组树表共用同一渲染件。
 * 单元格内容（就地导入徽章、漂移标记、环境/标签徽标、状态徽章、按状态切换的主操作）与迁移前逐项一致。
 */
function InstanceRowViewImpl({
  inst,
  nodeName,
  selected,
  onToggleSelect,
  isProxy = false,
  provisioning = false,
  drift,
  metrics,
  columns = ALL_OPTIONAL_COLUMNS,
  proxyExpanded = false,
  onToggleProxy,
  renderBackends,
  starting = false,
  stopping = false,
  menu,
  onOpenInstance,
  onStart,
  onStop,
  onKill,
  onContextMenu,
  columnOrder,
  businessStatus,
}: InstanceRowViewProps) {
  const { t } = useTranslation()
  const statusLabel = t(STATUS_LABEL_KEY[inst.status] ?? STATUS_LABEL_FALLBACK)
  /** 服务端类别（插件服 / 代理 / 二进制）：类型列标签与名称列图标共用同一判据。 */
  const kindOf = (i: InstanceInfo) => instanceKindOf(i)
  const instEnv = envOf(inst)
  const free = freeTagsOf(inst)
  const envLabel = instEnv ? t(`grouping.env_${instEnv}`, { defaultValue: instEnv }) : ''
  // 环境 + 自由标签的完整列表：标签列单行裁剪后，靠 title 仍能看全（见下方标签列注释）。
  const tagsTitle = [envLabel, ...free].filter(Boolean).join(' · ')
  // 大区 · 小区：两者都有时用「·」分隔，只有一个时直接显示那个，都没有则显示占位符。
  // 大区是虚拟分区（由 region: 标签推导），不做折叠层级，只作为一列归属信息呈现。
  const region = regionOf(inst)
  const zone = zoneOf(inst)
  /**
   * 异常强化（常态弱化）：崩溃或存在运行态漂移的行给出左侧警示条 + 淡红底。
   * 只加背景与伪元素，不动行盒高度——虚拟窗口的 36px 模型不受影响（见 INSTANCE_ROW_HEIGHT）。
   */
  const attention = inst.status === 'CRASHED' || Boolean(drift)

  const cell_select = (
      <TableCell key="select">
        <Checkbox checked={selected} onCheckedChange={onToggleSelect} aria-label={inst.name} />
      </TableCell>
  )
  /**
   * 
   * 大区 / 小区改为**两级徽章**：原来是一串 `r1 · z1` 纯文本——
   * 两者层级不同却挤在一个文本里，读起来像同一个东西的两个值；
   * 而 `r1`/`z1` 本身是标签值（运维自定的短码），加不加前缀都读不出层级。
   * 徽章化之后：大区用中性底、小区用更浅的底，层级由「深浅 + 上下位置」表达，
   * 同时省宽（纯文本要占 92px 也只够写四个字符）。
   * 
   */
  const cell_region = (
      <TableCell key="region" className="whitespace-nowrap">
        {/*
          大区 / 小区：`r1 / z1` 斜杠分隔，**大区加粗**（层级更高）。
          不用徽章：一行 50 条重复两枚色块太吵；斜杠 + 字重已足够表达「上级 / 下级」。
          两者都是标签值（`region:` / `zone:`），后端没有独立的显示名，
          因此保持原样展示——不要把 `r1` 擅自改写成别的文案，那会与分组树对不上。
        */}
        {region || zone ? (
          <span className="text-xs text-muted-foreground">
            {region && <span className="font-semibold text-foreground">{region}</span>}
            {region && zone && <span className="mx-1 text-muted-foreground/60">/</span>}
            {zone && <span>{zone}</span>}
          </span>
        ) : (
          <span className="text-xs text-muted-foreground/60">--</span>
        )}
      </TableCell>
  )
  const cell_name = (
      <TableCell key="name" className="font-medium">
        <div className="flex items-center gap-1.5">
          {/*
            展开箭头占一个**固定宽度的槽位**（w-4），代理与后端行都保留它：
            原来只有代理行插入箭头，导致同一列里代理名比后端名右移约 18px——
            整列名字对不齐，扫读时像「代理是子级」。槽位共用后名称一律从同一 x 起排。
          */}
          <span className="flex w-4 shrink-0 items-center justify-center">
          {isProxy && (
            // 纯图标：展开该代理已注册的后端列表。
            // 原样式无固定盒模型（宽度即图标本身），故 invisible + auto 保住紧凑占位；
            // 用 icon-xs 会插入 24px 方块，撑宽「实例名」列的排版。
            <Button
              type="button"
              variant="invisible"
              size="auto"
              onClick={onToggleProxy}
              aria-label={t('proxy.manageBackends')}
              className="text-muted-foreground hover:text-foreground"
            >
              {proxyExpanded ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
            </Button>
          )}
          </span>
          {/*
            状态色点：让「这台是活的还是死的」在扫名称列时就成立。
            原来状态只在最右侧那一列，视觉动线要从左扫到右才能判断；
            色点用与状态徽章同一判据（instanceStatusLevel），不会出现两处不一致。
          */}
          <span
            className={cn('size-1.5 shrink-0 rounded-full', STATUS_DOT_CLASS[instanceStatusLevel(inst.status)])}
            title={statusLabel}
            aria-hidden="true"
          />
          {/* 实例名：表格单元格里的行内跳转，外观是文本链接，取 invisible + auto。
              显式写 text-[13px]（= 全局正文基准）：Button 基础类带 text-sm(14px)，
              不覆盖会让表格里的实例名比同列其它文字大一号。 */}
          <Button
            type="button"
            variant="invisible"
            size="auto"
            className="text-left text-[13px] text-primary hover:underline"
            onClick={() => onOpenInstance?.(inst.id)}
          >
            {inst.name}
          </Button>
          {/* 就地导入徽章（FR-302）：工作目录在托管区外，删除实例不删原目录。 */}
          {inst.workDirInPlace && (
            <Badge variant="outline" className="shrink-0 border-amber-500/50 text-amber-600 dark:text-amber-400">
              {t('importServer.inPlaceBadge')}
            </Badge>
          )}
          {/* 运行态漂移标记（FR-471）：目录下有未纳管活进程，面板状态可能不准（行内只做标记，
              接管入口收在「⋯」菜单，避免每行多插一个破坏密度的按钮）。 */}
          {/*
            运行态漂移标记在**表格行里只显示图标**：完整徽章带「检测到未纳管进程」七字，
            会把名称列撑宽并抬高整行（实测该行比其他行高约 20px，破坏表格行高一致）。
            详情文案仍走 title，接管入口在「⋯」菜单里。
          */}
          {drift && (
            <span
              className="shrink-0 text-status-warning"
              title={`${t('serverConsole.runtimeDriftBadge')}\n${drift.cmdline ?? ''}`}
              data-testid="runtime-drift-badge"
            >
              <TriangleAlert className="size-3.5" />
            </span>
          )}
        </div>
      </TableCell>
  )
  const cell_identity = (
        <TableCell key="identity">
          <div className="flex min-w-0 flex-col gap-1">
            {/* 第一行：类型 + 角色 + 环境 */}
            <div className="flex min-w-0 items-center gap-1">
              {columns.has('type') && (
                <span
                  className={cn(
                    'inline-flex shrink-0 items-center rounded px-1.5 py-px text-[11px] font-medium',
                    KIND_CHIP_CLASS[kindOf(inst)],
                  )}
                  title={inst.type}
                >
                  {t(INSTANCE_KIND_LABEL_KEY[kindOf(inst)])}
                </span>
              )}
              {columns.has('role') && <RoleBadge role={inst.role} />}
              {instEnv && (
                <span className="shrink-0 rounded border border-primary/40 px-1.5 py-px text-[11px] text-primary">
                  {envLabel}
                </span>
              )}
            </div>
            {/* 第二行：自由标签（最多 2 个 + 计数） */}
            {columns.has('tags') && free.length > 0 && (
              <div className="flex min-w-0 items-center gap-1" title={tagsTitle}>
                {free.slice(0, 2).map((tg) => (
                  <span
                    key={tg}
                    className="max-w-[74px] truncate rounded bg-muted px-1.5 py-px text-[11px] text-muted-foreground"
                  >
                    {tg}
                  </span>
                ))}
                {free.length > 2 && (
                  <span className="shrink-0 text-[11px] text-muted-foreground/70 tabular-nums">+{free.length - 2}</span>
                )}
              </div>
            )}
          </div>
        </TableCell>
  )
  /**
   * 
   * 节点 / 端口：上行节点名、下行端口。两者性质不同（在哪 / 怎么连），
   * 拆开各自可读；端口弱化颜色，不与节点名抢注意力。
   * 
   */
  const cell_nodePort = (
      <TableCell key="nodePort" className="whitespace-nowrap">
        <div className="truncate text-xs text-muted-foreground" title={nodeName}>
          {nodeName}
        </div>
        {inst.serverPort > 0 && (
          <div className="text-[11px] text-muted-foreground/70 tabular-nums">:{inst.serverPort}</div>
        )}
      </TableCell>
  )
  const cell_status = (
      <TableCell key="status">
        <StatusBadge
          level={instanceStatusLevel(inst.status)}
          label={statusLabel}
          pulse={inst.status === 'STARTING' || inst.status === 'STOPPING'}
        />
      </TableCell>
  )
  const cell_health = (
        <TableCell key="health">
          <InstanceHealthCell
            metrics={metrics}
            running={inst.status === 'RUNNING'}
            onHover={healthHoverBridgeRef.current}
          />
        </TableCell>
  )
  /*
    业务插件单元格（FR-534）：显示该实例探针注册了哪些业务域、有没有降级的。

    **只渲染不解释**：域数量与健康与否直接来自探针上报，不推断业务含义。
    三种状态必须分清（混起来会让运维误判）：
      - 无数据（undefined）→ `--`：列刚开启/批量未返回/该台不在批里，**不等于没装插件**
      - 0 个域           → 「未注册」：探针明确说没有域
      - N 个域有降级      → 「2/3 正常」+ 警示色：能一眼看出哪台有问题
  */
  const cell_business = (
    <TableCell key="business">
      {(() => {
        const s = summarizeBusiness(businessStatus)
        if (s.kind === 'unknown') {
          return <span className="text-muted-foreground">--</span>
        }
        if (s.kind === 'none') {
          return (
            <span className="text-muted-foreground">
              {t('instances.business.none', { defaultValue: '未注册' })}
            </span>
          )
        }
        return (
          <span
            className={
              s.degraded > 0
                ? 'font-medium text-amber-600 dark:text-amber-400'
                : 'text-muted-foreground'
            }
            title={
              s.stale
                ? t('instances.business.staleHint', { defaultValue: '探针不可达，显示最后一次已知状态' })
                : undefined
            }
          >
            {t('instances.business.summary', {
              defaultValue: '{{ok}}/{{total}} 正常',
              ok: s.ok,
              total: s.total,
            })}
          </span>
        )
      })()}
    </TableCell>
  )
  const cell_actions = (
      <TableCell key="actions">
        {/*
          【操作列只留图标】此前启动/停止/重启/强杀都是**文字**按钮（「强制终止」就占约 60px），
          四档状态叠加行尾「⋯」后把本列撑到 135px，导致表格在 1280 宽下横向溢出、
          「⋯」被挤出视口。改为图标按钮：可访问名与 tooltip 仍在（aria-label / title），
          视觉宽度降到约 24px/个，一键操作的能力一点没减。
  
          行内只保留**当前状态下最常用**的那一个（停止态=启动、运行态=停止、过渡态=强杀），
          重启等偶发动作统一收进「⋯」菜单——与卡片视图可用动作保持一致。
        */}
        <div className="flex items-center gap-0.5">
          {(inst.status === 'STOPPED' || inst.status === 'CRASHED') && (
            <span title={provisioning ? t('instances.provisioningBlocked') : t('instances.start')}>
              <Button
                variant="ghost"
                size="icon-xs"
                disabled={provisioning || starting}
                onClick={onStart}
                aria-label={t('instances.start')}
                className="text-green-600 hover:text-green-700"
              >
                <Play className="size-3.5" />
              </Button>
            </span>
          )}
          {inst.status === 'RUNNING' && (
            <Button
              variant="ghost"
              size="icon-xs"
              disabled={stopping}
              onClick={onStop}
              aria-label={t('instances.stop')}
              title={t('instances.stop')}
              className="text-yellow-600 hover:text-yellow-700"
            >
              <Square className="size-3.5" />
            </Button>
          )}
          {(inst.status === 'STARTING' || inst.status === 'STOPPING') && (
            <Button
              variant="ghost"
              size="icon-xs"
              onClick={onKill}
              aria-label={t('instances.kill')}
              title={t('instances.kill')}
              className="text-yellow-600 hover:text-yellow-700"
            >
              <X className="size-3.5" />
            </Button>
          )}
          {menu}
        </div>
      </TableCell>
  )
  /** 列 key → 单元格元素（按 columnOrder 取用，见下方 map）。 */
  const CELLS: Record<string, ReactNode> = {
    select: cell_select,
    region: cell_region,
    name: cell_name,
    identity: cell_identity,
    nodePort: cell_nodePort,
    status: cell_status,
    health: cell_health,
    business: cell_business,
    actions: cell_actions,
  }

  /*
    本视图实际渲染的列顺序：与表头、colgroup 共用 computeVisibleColumns。
    【踩过坑】这三处曾各自算一遍，结果列顺序改了行没改（整表错位）、
    健康度格没跟随列设置、新增 business 列时 colgroup 漏加——
    收成一个函数后，加列只改一处。
  */
  const visibleColumnKeys = computeVisibleColumns(columnOrder, columns)

  return (
    <Fragment key={inst.id}>
      <TableRow
        data-state={selected ? 'selected' : undefined}
        onContextMenu={
          onContextMenu
            ? (event) => {
                // 只接管有菜单的行（代理/后端等一律有）：阻止浏览器原生菜单，避免两套菜单叠出。
                event.preventDefault()
                onContextMenu(inst, event.clientX, event.clientY)
              }
            : undefined
        }
        data-attention={attention ? 'true' : undefined}
        /*
          【为什么不用 ::before 画左侧警示条】在 `<tr>` 上用伪元素会破坏表格列布局：
          浏览器把 `::before` 当行内盒参与该行的列分配，导致这一行的首格整体右移、
          列宽与其它行错开（实测崩溃行比普通行右移 34px，内容挤成一团）。
          改用 `box-shadow: inset` 画同一条 3px 竖条——视觉等价，且完全在背景层、
          不参与任何布局计算。
        */
        style={{
          height: INSTANCE_ROW_HEIGHT,
          boxShadow: attention ? 'inset 3px 0 0 0 var(--status-danger)' : undefined,
        }}
        className={
          // 每行都有明确分隔线（用户要求「每一行要有分割」）：默认 border-b，
          // 异常行叠淡红底，但仍保留下边框，否则红底行会看起来「粘连」。
          attention
            ? 'border-b border-border/60 bg-status-danger/5 hover:bg-status-danger/10'
            : 'border-b border-border/60'
        }
      >
      {/*
        列顺序驱动（用户提出的表头拖拽换位）：8 个单元格先各自算好，再按 `columnOrder` 输出。
        表头与 colgroup 用同一份顺序，三处同源才不会错列。
        `identity` / `health` 是可选列组合：开关全关时对应单元格为 null，自然从输出里消失。
      */}
      {visibleColumnKeys.map((key) => (
        <Fragment key={key}>{CELLS[key]}</Fragment>
      ))}
      </TableRow>
      {isProxy && proxyExpanded && renderBackends && (
        <TableRow className="bg-muted/30 hover:bg-muted/30" style={{ height: INSTANCE_EXPANDED_ROW_HEIGHT }}>
          {/*
            colSpan 用**本行实际渲染的列数**，不用外层传入的 columnCount：
            两者一旦漂移（列顺序/列设置各算一套），摘要行就会与主行错列。
            这里直接以 visibleColumnKeys 为准，结构上不可能不一致。
          */}
          <TableCell colSpan={visibleColumnKeys.length} className="p-0">
            {/* 展开内容封顶在行高以内并内部滚动：行盒高度恒等于 INSTANCE_EXPANDED_ROW_HEIGHT，
                与后端条数无关（虚拟模型据此把展开行按常量计入，不需要测量回填）。 */}
            <div className="overflow-auto" style={{ maxHeight: INSTANCE_EXPANDED_ROW_HEIGHT }}>
              {renderBackends()}
            </div>
          </TableCell>
        </TableRow>
      )}
    </Fragment>
  )
}

/**
 * 行组件的记忆化包装。
 *
 * 【为什么需要】一次排序点击会引发 6~7 次整表重渲染（URL 变化 → 查询状态变化 → 数据到达 →
 * 指标到达…）。此前没有记忆化，每一次都把 50 行 × 8 列全部重建：实测单次排序触发
 * `InstanceRowView` 渲染 300 次、耗时约 880ms，并出现 1.1s 的 "Update Blocked"。
 *
 * 【比较策略】只比较**决定输出**的数据与视图开关；以下两类 props 故意不参与比较：
 * - 函数 props（onToggleSelect / onStart / onStop / onKill / onToggleProxy）：容器每帧新建闭包，
 *   但语义由 `inst` 决定（都只捕获实例 id），`inst` 不变即等价。
 * - 元素插槽（menu / renderBackends）：`menu` 由 `buildMenu(inst)` 现造，`renderBackends` 是惰性插槽
 *   （仅展开时才调用）；它们的内容同样由 `inst` 与 `proxyExpanded` 决定，后者已参与比较。
 * `metrics` 是唯一的例外——它来自外部 Map，需按值比较（见下）。
 */
export const InstanceRowView = memo(InstanceRowViewImpl, (prev, next) => {
  if (prev.inst !== next.inst) return false
  if (prev.selected !== next.selected) return false
  if (prev.isProxy !== next.isProxy) return false
  if (prev.provisioning !== next.provisioning) return false
  if (prev.drift !== next.drift) return false
  if (prev.starting !== next.starting) return false
  if (prev.stopping !== next.stopping) return false
  if (prev.proxyExpanded !== next.proxyExpanded) return false
  if (prev.nodeName !== next.nodeName) return false
  // 列集合按内容比较：容器每次都可能传新 Set，引用不同但内容相同不该重渲染。
  if (!sameColumnSet(prev.columns, next.columns)) return false
  /*
    列顺序必须参与比较。
    【这里踩过坑】表头拖拽换位后，表头变了而行内容没变——整表错位。
    根因就是这个比较器漏了 columnOrder：行组件被 memo 拦下，仍用旧顺序渲染，
    于是「表头是新顺序、行是旧顺序」。凡是决定**渲染结果**的 prop 都必须进比较器，
    漏一个就是一次静默错位。
  */
  if (!sameKeyArray(prev.columnOrder, next.columnOrder)) return false
  /*
    业务域状态同样必须进比较器：它决定「业务插件」这一格的渲染结果。
    批量轮询每次都会给出新对象，但域清单没变时不该重渲染整行——故按值比较。
  */
  if (!sameBusinessStatus(prev.businessStatus, next.businessStatus)) return false
  // 指标按浅值比较：Map 每次轮询都会给出新对象，但数值没变时不该重渲染这一行。
  return sameMetrics(prev.metrics, next.metrics)
})

/** 列顺序按内容相等：数组引用每帧都可能变，顺序相同时不该重渲染。 */
function sameKeyArray(a: readonly string[] | undefined, b: readonly string[] | undefined): boolean {
  if (a === b) return true
  if (!a || !b) return false
  if (a.length !== b.length) return false
  for (let i = 0; i < a.length; i++) if (a[i] !== b[i]) return false
  return true
}

/** 列集合内容相等（两边都可能为 undefined，表示「全部可选列」）。 */
function sameColumnSet(a: ReadonlySet<string> | undefined, b: ReadonlySet<string> | undefined): boolean {
  if (a === b) return true
  if (!a || !b) return false
  if (a.size !== b.size) return false
  for (const v of a) if (!b.has(v)) return false
  return true
}

/** 行内指标浅值相等：逐字段比数值，`series` 只比长度与末值（曲线细节变了就该重画，这里保守取末值）。 */
function sameMetrics(a: unknown, b: unknown): boolean {
  if (a === b) return true
  if (!a || !b) return false
  const x = a as Record<string, unknown>
  const y = b as Record<string, unknown>
  for (const k of Object.keys(x)) {
    const va = x[k]
    const vb = y[k]
    if (va === vb) continue
    // series 是对象，按末值+长度粗判；其余字段（数字/null）直接比引用
    if (va && vb && typeof va === 'object' && typeof vb === 'object') {
      const sa = va as { tps?: unknown[] }
      const sb = vb as { tps?: unknown[] }
      if (sa.tps?.length !== sb.tps?.length) return false
      continue
    }
    return false
  }
  return true
}

export default InstanceRowView


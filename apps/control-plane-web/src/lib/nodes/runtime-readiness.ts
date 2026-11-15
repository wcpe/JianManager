/**
 * 运行时就绪判定（节点详情「运行时」页签的结论层）。
 *
 * 【为什么需要它】改造前那个页签是三块独立面板竖排：JDK 面板说「有 3 个 JDK」、
 * 运行时面板说「Node.js 22 已登记」、日志面板说「HOT 未就绪」——每句都是原子事实，
 * 但没有一句回答「**这台节点现在能不能干活、缺什么**」。用户实际问的是后者。
 *
 * 本模块把那三份原子事实折算成 4 条**带结论的判定**，页面顶部只需渲染它：
 *   ok       绿 —— 一切就绪
 *   warn     黄 —— 能跑，但有隐患（如日志磁盘将满、无托管运行时）
 *   blocked  红 —— 缺前置，相关能力不可用
 *   unknown  灰 —— 数据没取到（加载中/接口失败），**不得**当成 ok
 *
 * 【为什么是纯函数】判定规则会随业务演进反复调参，而它是全部单元测试的主战场；
 * 不依赖 React、不取数、不发请求，才能把这些规则钉住而不必渲染整页。
 */

/** 判定档位。 */
export type ReadinessLevel = 'ok' | 'warn' | 'blocked' | 'unknown'

/** 单条判定的建议动作（页面据 key 决定跳哪、弹什么）。 */
export type ReadinessActionKey =
  | 'installJDK'
  | 'registerJDK'
  | 'installNode'
  | 'scanRuntimes'
  | 'dispatchLogAsset'
  | 'startLogRuntime'
  | 'openLogStorage'
  | 'openLogAcq'

/** 一条判定。 */
export interface ReadinessItem {
  /** 稳定 key（React 列表与测试定位）。 */
  key: 'java' | 'node' | 'log' | 'packages'
  level: ReadinessLevel
  /** 主数字/主结论（如「3 个可用」「HOT 未启动」）。 */
  headline: string
  /** 一句话解释（为什么是这个档位）。 */
  detail?: string
  /** 主行动（页面渲染成一个按钮）。 */
  action?: { key: ReadinessActionKey; label: string }
}

/** 判定所需的输入（全部来自已有接口，本模块不认识 HTTP）。 */
export interface ReadinessInput {
  nodeOnline: boolean
  /** JDK 行（只需 managed/majorVersion/refCount）。 */
  jdks?: Array<{ managed: boolean; majorVersion: number; refCount: number }>
  /** 非 JDK 运行时（nodejs / python…）。 */
  runtimes?: Array<{ type: string; majorVersion: number }>
  /** 包管理器配置（pm 名即可；未配置为 undefined）。 */
  packages?: { pm: string | null; globalPackageCount?: number }
  /** 日志运行时状态。 */
  logRuntime?: { instances: Array<{ namespace: string; state: string; health_ok?: boolean }> } | null
  /** 日志平台是否已下发审批资产。 */
  logAssetCached?: boolean
  /** 日志存储占用百分比（0~100，算不出来时为 null）。 */
  logDiskPct?: number | null
  /** 日志存储已用字节（与 pct 二选一展示，pct 缺失时兜底）。 */
  logDiskUsedBytes?: number | null
  /** 各数据源是否已加载完（未加载完→ unknown，不误判 blocked）。 */
  loaded: { jdks: boolean; runtimes: boolean; log: boolean }
}

/** 阈值：与全站资源水位口径一致（≥90 危险、≥80 注意）。 */
const DISK_DANGER = 90
const DISK_WARN = 80

/** 把字节数格式化成人读形式（本模块自带，避免依赖 UI 层）。 */
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  let v = bytes
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  // 取整规则：字节档（i=0）与 >=100 的大数取整，其余保留一位小数；
  // 恰好是整数时不留 `.0`（「1 KiB」而不是「1.0 KiB」）。
  const rounded = v >= 100 || i === 0 ? String(Math.round(v)) : Number.isInteger(v) ? String(v) : v.toFixed(1)
  return `${rounded} ${units[i]}`
}

/**
 * Java 就绪：有可用 JDK 即可跑 Java 实例。
 *
 * 【为什么不看「有没有被引用」】零引用的 JDK 也是可用运行时——它只是暂时没实例用，
 * 说成「未就绪」会让刚装完 JDK 还没建实例的用户以为装失败了。
 */
function judgeJava(input: ReadinessInput): ReadinessItem {
  if (!input.loaded.jdks) {
    return { key: 'java', level: 'unknown', headline: '读取中…' }
  }
  const jdks = input.jdks ?? []
  if (jdks.length === 0) {
    return {
      key: 'java',
      level: 'blocked',
      headline: '无可用 JDK',
      detail: 'Java 实例无法启动，需先安装或登记一个 JDK',
      action: { key: 'installJDK', label: '安装 JDK' },
    }
  }
  const managed = jdks.filter((j) => j.managed).length
  const used = jdks.reduce((sum, j) => sum + (j.refCount > 0 ? 1 : 0), 0)
  const majors = Array.from(new Set(jdks.map((j) => j.majorVersion))).sort((a, b) => b - a)
  return {
    key: 'java',
    level: 'ok',
    headline: `${jdks.length} 个可用`,
    detail: `Java ${majors.join(' / ')} · 托管 ${managed} · 被引用 ${used}`,
  }
}

/**
 * Node.js / 包管理就绪：插件部署与 Bot 依赖这些，但缺了不影响 Java 服跑起来，
 * 所以缺失是 warn 而不是 blocked。
 */
function judgeNode(input: ReadinessInput): ReadinessItem {
  if (!input.loaded.runtimes) {
    return { key: 'node', level: 'unknown', headline: '读取中…' }
  }
  const nodeRuntimes = (input.runtimes ?? []).filter((r) => r.type === 'nodejs')
  const pm = input.packages?.pm ?? null
  if (nodeRuntimes.length === 0) {
    return {
      key: 'node',
      level: 'warn',
      headline: '未安装 Node.js',
      detail: '依赖 Node 的插件与 Bot 工具链不可用；Java 服不受影响',
      action: { key: 'installNode', label: '安装 Node.js' },
    }
  }
  const majors = nodeRuntimes.map((r) => r.majorVersion).sort((a, b) => b - a)
  const pkgCount = input.packages?.globalPackageCount
  return {
    key: 'node',
    level: 'ok',
    headline: `Node.js ${majors[0]}`,
    detail: [pm ? `包管理器 ${pm}` : '包管理器未配置', typeof pkgCount === 'number' ? `全局包 ${pkgCount}` : null]
      .filter(Boolean)
      .join(' · '),
  }
}

/**
 * 日志平台就绪：三态判定——资产未下发 → 未启动 → 运行中（再看磁盘水位）。
 *
 * 【为什么资产缺失是 blocked 而不是 warn】没有审批资产时，HOT/COLD/Rehydrate
 * 三个分区一个都启动不了（后端会以 LOG_ARCHIVE_MISSING / supervised 未配置拒绝），
 * 那不是「有隐患」，是「整块能力不可用」。
 */
function judgeLog(input: ReadinessInput): ReadinessItem {
  if (!input.loaded.log) {
    return { key: 'log', level: 'unknown', headline: '读取中…' }
  }
  const instances = input.logRuntime?.instances ?? []
  const running = instances.filter((i) => i.state === 'RUNNING')
  const hot = instances.find((i) => i.namespace === 'hot')

  if (!input.logAssetCached && instances.length === 0) {
    return {
      key: 'log',
      level: 'blocked',
      headline: '审批资产未下发',
      detail: '需先上传 VictoriaLogs 审批包并下发到本节点，之后才能启动分区',
      action: { key: 'dispatchLogAsset', label: '下发资产' },
    }
  }
  if (!hot || hot.state !== 'RUNNING') {
    return {
      key: 'log',
      level: 'blocked',
      headline: hot ? `HOT ${hot.state}` : 'HOT 未启动',
      detail: 'HOT 分区未运行时无法写入与查询在线日志',
      action: { key: 'startLogRuntime', label: '启动 HOT' },
    }
  }
  // HOT 已运行：再看存储水位。pct 算不出来时用已用字节说明，不编造百分比。
  const pct = input.logDiskPct
  if (typeof pct === 'number' && pct >= DISK_DANGER) {
    return {
      key: 'log',
      level: 'blocked',
      headline: `磁盘 ${Math.round(pct)}%`,
      detail: '已超危险水位，超保留期的日志将被丢弃，需清理或迁移分区',
      action: { key: 'openLogStorage', label: '查看存储' },
    }
  }
  if (typeof pct === 'number' && pct >= DISK_WARN) {
    return {
      key: 'log',
      level: 'warn',
      headline: `磁盘 ${Math.round(pct)}%`,
      detail: '接近上限，建议迁移旧分区到 COLD',
      action: { key: 'openLogStorage', label: '查看存储' },
    }
  }
  return {
    key: 'log',
    level: 'ok',
    headline: `${running.length} 个分区运行中`,
    detail:
      typeof pct === 'number'
        ? `HOT 磁盘 ${Math.round(pct)}%`
        : input.logDiskUsedBytes
          ? `HOT 占用 ${formatBytes(input.logDiskUsedBytes)}`
          : 'HOT 运行中',
  }
}

/**
 * 托管盘余量：回答「还能装几个运行时/放多少日志」。
 * 与前三项不同，它没有「缺失」态——只在磁盘告急时降级。
 */
function judgePackages(input: ReadinessInput): ReadinessItem {
  const used = input.logDiskUsedBytes ?? null
  const pkgCount = input.packages?.globalPackageCount
  const pm = input.packages?.pm
  if (!pm) {
    return {
      key: 'packages',
      level: 'warn',
      headline: '包管理器未配置',
      detail: '全局包安装需要先选定 npm / pnpm / yarn',
      action: { key: 'installNode', label: '配置包管理器' },
    }
  }
  return {
    key: 'packages',
    level: 'ok',
    headline: pm,
    detail: [typeof pkgCount === 'number' ? `全局包 ${pkgCount}` : null, used ? `日志占用 ${formatBytes(used)}` : null]
      .filter(Boolean)
      .join(' · '),
  }
}

/**
 * 总判定：固定 4 条，顺序即页面渲染顺序（Java → Node → 日志 → 包管理）。
 *
 * 【为什么顺序固定】页签内三块面板的阅读顺序是「JDK → 运行时 → 日志」，
 * 结论带必须与之同序，否则用户要在两处之间来回映射。
 */
export function evaluateReadiness(input: ReadinessInput): ReadinessItem[] {
  const items = [judgeJava(input), judgeNode(input), judgeLog(input), judgePackages(input)]
  // 节点离线时全部能力不可用：把 ok/warn 统一压成 blocked，并说明原因。
  // 【为什么仍保留 unknown】数据没取到是另一回事，不能用「离线」掩盖取数失败。
  if (!input.nodeOnline) {
    return items.map((it) =>
      it.level === 'unknown'
        ? it
        : {
            ...it,
            level: 'blocked' as const,
            detail: '节点离线，运行时状态无法确认；以下判据来自最后一次心跳',
          },
    )
  }
  return items
}

/** 汇总：最高严重度（页面折叠头用它给整条带一个颜色）。 */
export function worstLevel(items: ReadinessItem[]): ReadinessLevel {
  if (items.some((i) => i.level === 'blocked')) return 'blocked'
  if (items.some((i) => i.level === 'warn')) return 'warn'
  if (items.some((i) => i.level === 'unknown')) return 'unknown'
  return 'ok'
}

/** 一句话结论（折叠态展示，避免「收起来就什么都不说」）。 */
export function summarizeReadiness(items: ReadinessItem[]): string {
  const blocked = items.filter((i) => i.level === 'blocked')
  const warn = items.filter((i) => i.level === 'warn')
  if (blocked.length > 0) return `${blocked.map((i) => i.headline).join('、')} — 需处理`
  if (warn.length > 0) return `${warn.map((i) => i.headline).join('、')} — 可用但有隐患`
  if (items.some((i) => i.level === 'unknown')) return '部分数据未取到'
  return '运行时全部就绪'
}

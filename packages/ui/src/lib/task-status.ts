/**
 * 任务状态的纯逻辑与类型（FR-183 / FR-227 / FR-329）。
 *
 * 从应用侧 `api/tasks.ts` 抽出：那里混着 `useTasks` 等取数 hook，包内不该依赖；
 * 类型、终态判定、kind 文案键与轮询启停规则都是纯逻辑，单独成模块便于复用与单测。
 * 应用侧 `api/tasks.ts` 原样转出这些导出，调用点零改动。
 */

/** 任务状态。pending/running 为进行中，succeeded/failed/canceled 为终态（canceled=被强制停止，FR-227）。 */
export type TaskState = 'pending' | 'running' | 'succeeded' | 'failed' | 'canceled'

/** 一条长任务。 */
export interface Task {
  id: number
  taskId: string
  nodeId: number
  /** 任务种类，如 jdk_install。 */
  kind: string
  state: TaskState
  /** 0~100。 */
  progress: number
  title: string
  detail: string
  /** 失败原因（仅 failed）。 */
  error: string
  /** 成功结果 JSON（如安装出的 JDK 信息，仅 succeeded）。 */
  result: string
  /** 已请求强制停止但 Worker 尚未确认中断（在线 running 取消时为 true，显「取消中」，FR-227）。 */
  cancelRequested: boolean
  createdBy: number
  createdAt: string
  updatedAt: string
}

/** 任务的一行滚动日志。 */
export interface TaskLog {
  id: number
  taskId: string
  seq: number
  line: string
  ts: string
}

/** 终态集合，便于判断是否仍需轮询。 */
const TERMINAL_STATES: ReadonlySet<TaskState> = new Set<TaskState>(['succeeded', 'failed', 'canceled'])

/** 任务是否处于终态。 */
export function isTerminalTask(t: Pick<Task, 'state'>): boolean {
  return TERMINAL_STATES.has(t.state)
}

/**
 * 任务 kind → i18n 文案键（任务中心筛选与页眉任务下拉共用，FR-327）。
 * 新增 TaskKind 时同步补一行 + zh/en 文案；未知 kind 由消费方回退显示原始值。
 */
export const TASK_KIND_LABEL_KEYS: Record<string, string> = {
  jdk_install: 'tasks.kind.jdkInstall',
  runtime_install: 'tasks.kind.runtimeInstall',
  pkg_install: 'tasks.kind.pkgInstall',
  provision: 'tasks.kind.provision',
  binary_provision: 'tasks.kind.binaryProvision',
  import: 'tasks.kind.import',
  clone: 'tasks.kind.clone',
  backup_create: 'tasks.kind.backupCreate',
  backup_restore: 'tasks.kind.backupRestore',
  artifact_migrate: 'tasks.kind.artifactMigrate',
}

/** 活跃任务轮询间隔（FR-329）：存在非终态任务时 ~2s 自动刷新进度。 */
export const ACTIVE_TASKS_REFETCH_MS = 2000

/**
 * 任务轮询启停判定（FR-329）：任一任务非终态 → 2s 短轮询；全部终态 / 空 / 未加载 → 停。
 * 抽纯函数供任务列表与单任务详情共用，且轮询启停规则可独立单测。
 */
export function tasksRefetchInterval(tasks: readonly Pick<Task, 'state'>[] | undefined): number | false {
  const hasActive = Array.isArray(tasks) && tasks.some((t) => !isTerminalTask(t))
  return hasActive ? ACTIVE_TASKS_REFETCH_MS : false
}

/** GET /tasks 分页信封（FR-337）：total 与筛选（含归属隔离）同口径，limit/offset 回显服务端钳制后生效值。 */
export interface TaskPage {
  items: Task[]
  total: number
  limit: number
  offset: number
}

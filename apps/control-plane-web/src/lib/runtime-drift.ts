/**
 * 运行态漂移（FR-471）：判定与字段归一。
 *
 * 语义：`runtimeDriftPid > 0` 表示该实例**工作目录下存在未被平台纳管的活进程**——
 * 典型场景是运维用 tmux/脚本手工启服，平台 DB 记为 STOPPED 而磁盘实际在跑。
 *
 * 为什么放在 `lib` 而不是 `api/instances`：判定本身是纯函数，而若干既有测试会整模块
 * `vi.mock('@/api/instances')`（工厂只提供被用到的 hooks）。组件若从该模块 import 本函数，
 * 在那些 mock 下会取到 undefined 而直接抛错——判定逻辑与被 mock 的取数层解耦可避免这种脆性。
 */

/** 判定所需的实例侧字段（`InstanceInfo` 的子集，便于列表/详情/卡片复用）。 */
export interface RuntimeDriftFields {
  runtimeDriftPid?: number
  runtimeDriftCmdline?: string
}

/** 漂移进程的展示信息。 */
export interface RuntimeDriftInfo {
  pid: number
  cmdline?: string
}

/**
 * 是否存在运行态漂移：唯一判据是 `runtimeDriftPid > 0`。
 * 后端对无漂移实例写 0，故 0 与缺省同义——用真值判断会把合法 PID 之外的 0 误当「有漂移」，
 * 判定收敛在此函数，禁止在组件里散写 `instance.runtimeDriftPid` 的真值判断。
 */
export function hasRuntimeDrift(inst: RuntimeDriftFields): boolean {
  return (inst.runtimeDriftPid ?? 0) > 0
}

/** 归一为可渲染的漂移信息；无漂移返回 undefined（调用方据此不渲染任何标记）。 */
export function runtimeDriftOf(inst: RuntimeDriftFields): RuntimeDriftInfo | undefined {
  if (!hasRuntimeDrift(inst)) return undefined
  return { pid: inst.runtimeDriftPid as number, cmdline: inst.runtimeDriftCmdline?.trim() || undefined }
}

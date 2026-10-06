/**
 * 实例实体的类型（FR-032 / FR-047 / FR-079 / FR-445 / FR-471）。
 *
 * 从应用侧 `api/instances.ts` 抽出：那里混着取数 hook，而实例实体是纯数据，
 * 视图与纯逻辑侧要复用。应用侧原文件原样转出，调用点零改动。
 */
import type { InstanceCapabilityProfile } from './capabilities'

export interface InstanceInfo {
  id: number
  uuid: string
  nodeId: number
  name: string
  type: string
  /** 群组服角色（FR-032）：proxy / backend / universal。 */
  role: string
  processType: string
  status: string
  /** 当前状态原因，主要用于 CRASHED：异步委托失败的具体错误（如「实例未绑定 JDK…」），供前端显示。 */
  statusReason?: string
  startCommand: string
  /** 绑定的 JDK id（0/缺省=未绑定，用系统 Java）。 */
  jdkId?: number
  workDir: string
  /** 就地导入标记（FR-302）：工作目录为托管区外原目录，删除实例不删原目录。 */
  workDirInPlace?: boolean
  /** docker 模式的容器镜像引用（FR-078，ADR-019）；非 docker 模式为空。 */
  image?: string
  /** docker 模式 CPU 核数上限（FR-079）；0=不限制，仅 docker 模式生效。 */
  cpuLimit?: number
  /** docker 模式内存上限（MiB，FR-079）；0=不限制，仅 docker 模式生效。 */
  memLimitMb?: number
  /** docker 模式磁盘上限（MiB，FR-079）；0=不限制，v1 仅持久化/展示不注入。 */
  diskLimitMb?: number
  /** 系统分配的游戏服监听端口（FR-032），Bot 默认据此连入所属实例。 */
  serverPort: number
  /** MC 查询端口（Query 协议）；0/缺省=未启用。 */
  queryPort?: number
  /** ServerProbe 监控探针 /metrics 端口（系统分配，FR-010）；0=未部署探针。 */
  probePort?: number
  autoStart: boolean
  autoRestart: boolean
  /**
   * 标签集合（FR-047）：环境维度复用 `env:` 前缀（如 `env:prod`），其余为自由标签。
   * 后端以原始 JSON 字符串返回（空为 ""、有值为 `'["env:prod"]'`、清空为 "null"），与
   * envVars/launchSpec 一致；消费前一律经 `parseTags()` 规范化为数组，勿直接当数组用。
   */
  tags: string | string[] | null
  /**
   * 实例能力画像（FR-445，ADR-091）：仅详情/单查响应携带（列表不计），前端据此显隐 Tab。
   * 缺失时前端按 (type, role) 本地兜底（`lib/capabilities.ts`），故为可选。
   */
  capabilities?: InstanceCapabilityProfile | null
  /**
   * 运行态漂移 PID（FR-471）：>0 表示该实例工作目录下存在**未被平台纳管的活进程**——
   * 面板可能显示 STOPPED 而磁盘实际在跑（如 tmux/脚本手工启动）。0/缺省=无漂移。
   * 判定一律经 `hasRuntimeDrift()`，勿直接读该字段做真值判断（0 与缺省同义）。
   */
  runtimeDriftPid?: number
  /** 漂移进程的命令行摘要（已截断，FR-471）；无漂移为空串。 */
  runtimeDriftCmdline?: string
  /** 漂移最近观测时间（FR-471）；无漂移为 null。 */
  runtimeDriftAt?: string | null
  createdAt: string
}

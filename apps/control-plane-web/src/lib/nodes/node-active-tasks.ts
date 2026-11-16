/**
 * 节点「当前在跑什么」的纯逻辑。
 *
 * 【为什么单独一个模块】这段逻辑有两个容易被写错的判断，都值得单测：
 *  1. 「这个安装按钮该不该禁用」——不是"有任务在跑"就禁，而是**同互斥组**有任务才禁
 *     （全局包装着时仍可以装 JDK，它们写不同目录）；
 *  2. 「点下去会不会被复用」——同 kind 且同参数才是复用，不同参数会被拒。
 *     两者对用户的提示完全不同，混成一个就是误导。
 */
import type { Task } from '@/lib/tasks/task-status'

/** 重型任务 kind（与后端 exclusiveGroupOf 同口径）。 */
export type HeavyKind = 'jdk_install' | 'runtime_install' | 'pkg_install'

/** kind → 互斥组名（与后端保持一致：JDK 与 Node 共组，全局包独立）。 */
export function exclusiveGroupOf(kind: string): string {
  switch (kind) {
    case 'jdk_install':
    case 'runtime_install':
      return 'runtime_install'
    case 'pkg_install':
      return 'pkg_install'
    default:
      return ''
  }
}

/** 从任务列表里挑出占着指定互斥组的在途任务（没有则 null）。 */
export function activeInGroup(tasks: readonly Task[], group: string): Task | null {
  if (!group) return null
  for (const t of tasks) {
    if (exclusiveGroupOf(t.kind) === group) return t
  }
  return null
}

/** 从任务列表里挑出**同 kind 且同参数**的在途任务（幂等复用的判定口径）。 */
export function activeSameKind(tasks: readonly Task[], kind: HeavyKind): Task | null {
  for (const t of tasks) {
    if (t.kind === kind) return t
  }
  return null
}

/** 任务的展示用进度：取消中的任务显示「取消中」，其余按百分比。 */
export function activeTaskLabel(t: Task): string {
  if (t.cancelRequested) return '取消中…'
  if (t.state === 'pending') return '排队中'
  return `${t.progress}%`
}

/** 是否可强制停止（pending 也可停：Worker 还没起，CP 直接落 canceled）。 */
export function canForceStop(t: Task): boolean {
  return !t.cancelRequested && (t.state === 'pending' || t.state === 'running')
}

/** 
 * @file 实例「服务端类别」的归一与文案。
 *
 * 三分法（用户确认的粒度）：插件服 / 代理 / 二进制，不细分 Paper/Spigot/Bukkit。
 *
 * 【为什么需要归一】`kind` 是后端新加的列，旧库数据为空；前端不能直接判空后显示空白——
 * 那会让侧栏第二行白占位。这里给出与后端 `KindOrInfer` **同口径**的回退推断，
 * 保证「前端推断」与「后端未回填时的推断」不会给出两个不同答案。
 */
import type { InstanceInfo } from './instance-types'

/** 服务端类别。 */
export type InstanceKind = 'plugin' | 'proxy' | 'binary'

const VALID: readonly InstanceKind[] = ['plugin', 'proxy', 'binary']

/** 是否为合法类别值。 */
export function isValidKind(value: string | undefined | null): value is InstanceKind {
  return value !== undefined && value !== null && (VALID as readonly string[]).includes(value)
}

/** 
 * 取实例类别；`kind` 缺失或非法时按 role + type 回退。
 *
 * 回退顺序与后端 `model.Instance.KindOrInfer` 一致：
 * 1. `role === 'proxy'` → 代理（群组拓扑角色是代理，跑的一定是代理程序）
 * 2. `type === 'generic'` → 二进制（非 MC Java 进程）
 * 3. 其余 → 插件服（MC 服务端里最常见的一类）
 */
export function instanceKindOf(inst: Pick<InstanceInfo, 'kind' | 'role' | 'type'>): InstanceKind {
  if (isValidKind(inst.kind)) return inst.kind
  if (inst.role === 'proxy') return 'proxy'
  if (inst.type === 'generic') return 'binary'
  return 'plugin'
}

/** 类别 → i18n 文案键。 */
export const INSTANCE_KIND_LABEL_KEY: Record<InstanceKind, string> = {
  plugin: 'instanceKind.plugin',
  proxy: 'instanceKind.proxy',
  binary: 'instanceKind.binary',
}

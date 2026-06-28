import { createCollection, type Collection, type Entity } from './collection'

/**
 * 惰性 collection 注册表（FR-197 / ADR-047 决策 5）。
 * 域簇在各自 handler 模块顶层 `const users = db<User>('users', seedUsers)` 声明并读写自己的集合，
 * 不改本中心文件、不改中心类型 —— 12 路并行加文件即生效、零冲突。
 */
const registry = new Map<string, Collection<Entity>>()

/**
 * 取（或惰性建）一个命名集合。
 * 首次传 seedFn 决定种子并立即播种；后续同名调用复用同一集合（seedFn 忽略）。
 * 因此每个集合应在**唯一**一处（其所属域的 handler 模块顶层）带 seedFn 声明。
 */
export function db<T extends Entity>(name: string, seedFn?: () => T[]): Collection<T> {
  let c = registry.get(name)
  if (!c) {
    c = createCollection<T>(seedFn) as unknown as Collection<Entity>
    registry.set(name, c)
  }
  return c as unknown as Collection<T>
}

/** 重置所有集合到种子初值。测试 afterEach 调用，保证用例间隔离（FR-197）。 */
export function resetDb(): void {
  registry.forEach((c) => c.reset())
}

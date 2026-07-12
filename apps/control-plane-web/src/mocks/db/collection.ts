/**
 * 内存假后端的实体集合抽象（FR-197 / ADR-047 决策 2）。
 * 单集合内存 CRUD + 可重播种子；跨实体联动由 handler 读写多个 collection 实现。
 */

/** 假后端实体基类型：每行有唯一 id。 */
export interface Entity {
  id: number | string
}

/** 单个实体集合：内存 CRUD + 种子重置（测试间隔离）。 */
export interface Collection<T extends Entity> {
  /** 回到种子初值（首次建集合时自动调用一次）。 */
  seed(): void
  /** reset = 重新播种，供测试 afterEach 归零。 */
  reset(): void
  list(pred?: (r: T) => boolean): T[]
  find(pred: (r: T) => boolean): T | undefined
  get(id: T['id']): T | undefined
  /** 插入；不传 id 则自增。返回插入后的行（含 id）。 */
  insert(row: Omit<T, 'id'> & Partial<Pick<T, 'id'>>): T
  /** 局部更新；返回更新后的行（不存在返回 undefined）。 */
  update(id: T['id'], patch: Partial<T>): T | undefined
  remove(id: T['id']): void
}

/**
 * 用种子函数建一个集合。seed/reset 都把数据恢复为 seedFn() 的深拷贝，
 * 保证用例间互不污染（reset 后改动不回写种子）。
 */
export function createCollection<T extends Entity>(seedFn: () => T[] = () => []): Collection<T> {
  let rows: T[] = []
  let auto = 1
  const self: Collection<T> = {
    seed() {
      rows = seedFn().map((r) => ({ ...r }))
      auto = rows.length + 1
    },
    reset() {
      self.seed()
    },
    list(pred) {
      return pred ? rows.filter(pred) : [...rows]
    },
    find(pred) {
      return rows.find(pred)
    },
    get(id) {
      return rows.find((r) => String(r.id) === String(id))
    },
    insert(row) {
      const r = { ...(row as T), id: row.id ?? auto++ } as T
      rows.push(r)
      return r
    },
    update(id, patch) {
      const r = self.get(id)
      if (r) Object.assign(r, patch)
      return r
    },
    remove(id) {
      rows = rows.filter((r) => String(r.id) !== String(id))
    },
  }
  self.seed()
  return self
}

import { describe, it, expect } from 'vitest'
import {
  normalizeQQDiscoveredGroups,
  normalizeQQDiscoveredGroupsPage,
  type QQDiscoveredGroup,
  type QQDiscoveredGroupList,
} from './alerts'

/** 
 * QQ 已发现群列表归一化单测。
 *
 * 存在的理由：真实后端 QQGroups 返回分页信封 `{items,total}`
 * （internal/controlplane/router/alert.go；docs/API.md 同），而 devmock 的
 * `mockInject(..., { kind: 'empty' })` 注入与历史实现给的是纯数组。只靠页面级 DOM 用例
 * 覆盖不到「信封形状」这一分支（此前 devmock 返回数组，信封分支从未被真实形状打到），
 * 故对纯函数直接钉住两种形状。
 */
const GROUP: QQDiscoveredGroup = {
  id: 1,
  groupOpenid: 'GROUP_OPENID_A',
  opMemberOpenid: 'OP_MEMBER_A',
  firstSeenAt: '2026-09-01T00:00:00Z',
  lastSeenAt: '2026-09-02T00:00:00Z',
  sourceAppId: '102000001',
}

describe('normalizeQQDiscoveredGroups', () => {
  it('分页信封 {items,total}：取 items（真实后端形状）', () => {
    expect(normalizeQQDiscoveredGroups({ items: [GROUP], total: 1 })).toEqual([GROUP])
  })

  it('纯数组：原样返回（mock 注入 / 历史形状）', () => {
    expect(normalizeQQDiscoveredGroups([GROUP])).toEqual([GROUP])
  })

  it('空输入不崩：undefined / null / 空信封 / 空数组一律 []', () => {
    expect(normalizeQQDiscoveredGroups(undefined)).toEqual([])
    expect(normalizeQQDiscoveredGroups(null)).toEqual([])
    expect(normalizeQQDiscoveredGroups({ items: [], total: 0 })).toEqual([])
    expect(normalizeQQDiscoveredGroups([])).toEqual([])
  })

  it('半截信封（缺 items 字段）→ []，不退化成 undefined', () => {
    const truncated = { total: 3 } as unknown as QQDiscoveredGroupList
    expect(normalizeQQDiscoveredGroups(truncated)).toEqual([])
  })
})

/**
 * 归一化为「一页群 + 总数」（FR-495 只读观测区的计数）。
 *
 * 存在的理由：页面标题写的是「已发现群（N）」。后端默认 pageSize=20，直接用 items.length
 * 当 N 会在群超过 20 个时静默少报——而那正好是运维判断「机器人到底进了多少群」的唯一数字。
 */
describe('normalizeQQDiscoveredGroupsPage', () => {
  it('信封：items 与 total 各自取其值（total 可以大于本页条数）', () => {
    expect(normalizeQQDiscoveredGroupsPage({ items: [GROUP], total: 25 })).toEqual({
      items: [GROUP],
      total: 25,
    })
  })

  it('纯数组（无总数信息）：total 退化为条数，不谎报 0', () => {
    expect(normalizeQQDiscoveredGroupsPage([GROUP, GROUP])).toEqual({
      items: [GROUP, GROUP],
      total: 2,
    })
  })

  it('信封 total 缺失或小于条数：取条数（计数不得低于实际展示到的条数）', () => {
    const noTotal = { items: [GROUP, GROUP] } as unknown as QQDiscoveredGroupList
    expect(normalizeQQDiscoveredGroupsPage(noTotal).total).toBe(2)
    expect(normalizeQQDiscoveredGroupsPage({ items: [GROUP, GROUP], total: 1 }).total).toBe(2)
  })

  it('空输入一律 0 条 0 总数', () => {
    for (const input of [undefined, null, [], { items: [], total: 0 }]) {
      expect(normalizeQQDiscoveredGroupsPage(input as QQDiscoveredGroupList | null | undefined)).toEqual({
        items: [],
        total: 0,
      })
    }
  })
})

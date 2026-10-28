import { describe, expect, it, vi } from 'vitest'
import {
  PAGE_CONTEXT_MAX_ENTITIES,
  PAGE_CONTEXT_MAX_FILTER_LEN,
  PAGE_CONTEXT_SECRET_VALUE_PATTERNS,
  getPageContext,
  pageContextValueLooksSecret,
  serializePageContext,
  setPageContext,
  subscribePageContext,
  useContextChips,
  usePageContext,
} from '@/lib/assistant/page-context'
import type { PageContextInput } from '@/lib/assistant/page-context'
import { render, renderHook, act } from '@testing-library/react'
import { useMemo } from 'react'

/**
 * 上下文快照的**边界语义**测试。
 *
 * 这里守的是三条容易在重构中悄悄破掉的规则：
 *  1. 没内容就回 null（不是 "{}"）——否则每个 run 都会挂一条空快照；
 *  2. 只有 pageKey 而无实体/筛选/页签/时间窗时也回 null（无信息量）；
 *  3. 移除即不再外发（芯片数组与外发内容一致，没有隐藏背景）。
 */
describe('上下文快照序列化', () => {
  it('没有任何上下文时返回 null，而不是空对象字符串', () => {
    expect(serializePageContext(null)).toBeNull()
  })

  it('只剩 pageKey、没有其它信息时返回 null（避免无信息量的快照）', () => {
    expect(serializePageContext({ pageKey: 'instances' })).toBeNull()
  })

  it('只有实体时也带上 v 与 pageKey（后端需要版本号解释字段）', () => {
    const raw = serializePageContext({ pageKey: 'instances', entities: [{ type: 'instance', id: 7 }] })
    expect(raw).not.toBeNull()
    // 实体 id 统一序列化为字符串：后端契约允许 string|number，统一成字符串后
    // 落库形状稳定，前端也不必区分两种类型。
    expect(JSON.parse(raw as string)).toEqual({
      v: 1,
      pageKey: 'instances',
      entities: [{ type: 'instance', id: '7' }],
    })
  })

  it('页签 / 时间窗 / 筛选都按需出现在快照里', () => {
    const raw = serializePageContext({
      pageKey: 'alerts',
      tab: 'open',
      timeRange: '24h',
      filters: { level: 'p1' },
    })
    expect(JSON.parse(raw as string)).toMatchObject({
      v: 1,
      pageKey: 'alerts',
      tab: 'open',
      timeRange: '24h',
      filters: { level: 'p1' },
    })
  })

  it('空 entities 数组 + 有页签 → 仍然外发（页签本身是信息）', () => {
    const raw = serializePageContext({ pageKey: 'alerts', tab: 'open' })
    expect(raw).not.toBeNull()
    expect(JSON.parse(raw as string).entities).toEqual([])
  })
})

describe('页面上下文注册表', () => {
  it('设置后可读取，订阅者会收到通知', () => {
    const spy = vi.fn()
    const unsub = subscribePageContext(spy)
    const ctx: PageContextInput = { pageKey: 'nodes', entities: [{ type: 'node', id: 3 }] }
    setPageContext(ctx)
    expect(getPageContext()).toBe(ctx)
    expect(spy).toHaveBeenCalledTimes(1)
    unsub()
    setPageContext(null)
    expect(spy).toHaveBeenCalledTimes(1)
  })

  it('注册表跨订阅者共享（浮窗与首页拿到同一份）', () => {
    const ctx: PageContextInput = { pageKey: 'tasks' }
    setPageContext(ctx)
    expect(getPageContext()).toEqual(ctx)
    setPageContext(null)
    expect(getPageContext()).toBeNull()
  })

  it('取消订阅后不再被通知（页面卸载即静默）', () => {
    const spy = vi.fn()
    const unsub = subscribePageContext(spy)
    unsub()
    unsub() // 重复取消不抛错（React 严格模式下会调两次）
    setPageContext({ pageKey: 'logs' })
    expect(spy).not.toHaveBeenCalled()
    setPageContext(null)
  })
})

describe('上下文芯片', () => {
  it('每条实体生成独立芯片，标签使用中文实体名而不暴露原始 type', () => {
    const { result } = renderHook(() =>
      useContextChips(
        {
          pageKey: 'instances',
          entities: [
            { type: 'instance', id: 1 },
            { type: 'instance', id: 2 },
          ],
        },
        () => {},
      ),
    )
    const labels = result.current.map((c) => c.label)
    expect(labels).toContain('当前页：实例')
    expect(labels).toContain('实例：instance-1')
    expect(labels).toContain('实例：instance-2')
    expect(result.current).toHaveLength(3)
  })

  it('移除单条实体后回写的剩余项里不再包含它', () => {
    const onChange = vi.fn()
    const value: PageContextInput = {
      pageKey: 'instances',
      entities: [
        { type: 'instance', id: 1 },
        { type: 'instance', id: 2 },
      ],
    }
    const { result } = renderHook(() => useContextChips(value, onChange))
    const chip = result.current.find((c) => c.key === 'ent:instance:1')
    act(() => chip?.onRemove())
    expect(onChange).toHaveBeenCalledWith({
      pageKey: 'instances',
      entities: [{ type: 'instance', id: 2 }],
    })
  })

  it('移除「当前页」等价于整页上下文不再外发（传 null）', () => {
    const onChange = vi.fn()
    const { result } = renderHook(() =>
      useContextChips({ pageKey: 'nodes', entities: [{ type: 'node', id: 9 }] }, onChange),
    )
    act(() => result.current.find((c) => c.key === 'page:nodes')?.onRemove())
    expect(onChange).toHaveBeenCalledWith(null)
  })

  it('没有上下文时没有芯片', () => {
    const { result } = renderHook(() => useContextChips(null, () => {}))
    expect(result.current).toEqual([])
  })
})

/**
 * FR-518 §4.1 / §6 的外发裁剪（前端侧）。
 *
 * 后端会再校验一次，但前端也裁有三条实际理由：
 *  - 用户当场能从芯片上看到「只带前 N 个」，而不是发出后才收到 context_warning；
 *  - 敏感键不该出现在**任何**出站请求体里（哪怕后端会丢）；
 *  - 不为一个必然被裁的字段撑大请求。
 */
describe('外发裁剪（FR-518 §4.1 / §6）', () => {
  it('实体最多外发 8 个', () => {
    const entities = Array.from({ length: 12 }, (_, i) => ({ type: 'instance' as const, id: i + 1 }))
    const raw = serializePageContext({ pageKey: 'instances', entities })
    const parsed = JSON.parse(raw as string)
    expect(parsed.entities).toHaveLength(PAGE_CONTEXT_MAX_ENTITIES)
    // 保留的是前 8 个（顺序稳定，不随机挑）。
    expect(parsed.entities[0].id).toBe('1')
    expect(parsed.entities[7].id).toBe('8')
  })

  it('实体 id 统一序列化为字符串', () => {
    const raw = serializePageContext({
      pageKey: 'instances',
      entities: [{ type: 'instance', id: 42 }, { type: 'node', id: 'abc-uuid' }],
    })
    const parsed = JSON.parse(raw as string)
    expect(parsed.entities).toEqual([
      { type: 'instance', id: '42' },
      { type: 'node', id: 'abc-uuid' },
    ])
  })

  it('敏感筛选键整键丢弃（连键名都不外发）', () => {
    const raw = serializePageContext({
      pageKey: 'instances',
      filters: { status: 'RUNNING', apiKey: 'sk-xxx', access_token: 'abc', userPassword: 'p' },
    })
    const parsed = JSON.parse(raw as string)
    expect(parsed.filters).toEqual({ status: 'RUNNING' })
    expect(raw).not.toContain('sk-xxx')
  })

  it('筛选值截断到 64 字符', () => {
    // 用 'x' 而不是 'a'：全 a 是合法十六进制，会被「值形态判据」认作摘要而丢弃，
    // 那样测到的就不是截断行为，而是脱敏行为（这条用例初版正因为换成 a 而变红）。
    const long = 'x'.repeat(100)
    const raw = serializePageContext({ pageKey: 'instances', filters: { q: long } })
    const parsed = JSON.parse(raw as string)
    expect(parsed.filters.q).toHaveLength(PAGE_CONTEXT_MAX_FILTER_LEN)
  })

  it('player 类型可外发（契约允许，可达性由后端重验）', () => {
    const raw = serializePageContext({
      pageKey: 'monitor',
      entities: [{ type: 'player', id: 'steve' }],
    })
    expect(JSON.parse(raw as string).entities).toEqual([{ type: 'player', id: 'steve' }])
  })

  it('超 4 KiB 不截断，原样交给后端拒收', () => {
    // 前端悄悄裁剪会让用户以为上下文完整送达（spec §6 末条）。
    // 这里用多个长筛选项堆出超限体积。
    const filters: Record<string, string> = {}
    for (let i = 0; i < 80; i++) filters[`k${i}`] = 'v'.repeat(PAGE_CONTEXT_MAX_FILTER_LEN)
    const raw = serializePageContext({ pageKey: 'instances', filters })
    expect(raw).not.toBeNull()
    expect(new TextEncoder().encode(raw as string).length).toBeGreaterThan(4096)
  })
})

describe('敏感筛选值形态判定（FR-518 §8 双重判定）', () => {
  /*
    只有键名黑名单拦不住 `?q=eyJhbGciOi...`：键名 `q` 无害，值是粘来的 JWT。
    这一组同时守两个方向——**该拦的拦住**，**不该拦的别误杀**。
    误杀的代价更隐蔽：用户看到「筛选没生效」却查不出原因。
  */

  it('JWT 形态的值即使键名无害也被丢弃', () => {
    const jwt =
      'eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0In0.dBjftJeZ4CVPmB92K27uhbUJU1p1r_wW1gFWFOEjXk'
    const raw = serializePageContext({ pageKey: 'instances', filters: { q: jwt, status: 'RUNNING' } })
    const parsed = JSON.parse(raw as string)
    expect(parsed.filters, 'JWT 值应被丢弃，只留 status').toEqual({ status: 'RUNNING' })
    expect(raw, 'JWT 原文不得出现在外发载荷里').not.toContain('eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9')
  })

  it('常见厂商密钥前缀被丢弃', () => {
    for (const v of [
      'sk-abcdefghijklmnopqrstuvwxyz123456',
      'ghp_abcdefghijklmnopqrstuvwxyz123456',
      'AKIAIOSFODNN7EXAMPLE',
      'xoxb-123456789012-abcdefghijkl',
    ]) {
      expect(pageContextValueLooksSecret(v), `${v} 应判为凭据`).toBe(true)
      // 只剩被丢弃的 filter 时，整个快照退化为 null（既有设计：无信息量不外发）
      expect(
        serializePageContext({ pageKey: 'instances', filters: { q: v } }),
        `${v} 不应外发（快照应退化为 null）`,
      ).toBeNull()
    }
  })

  it('纯 hex 摘要（md5/sha1/sha256）被丢弃', () => {
    for (const v of ['a'.repeat(32), 'b'.repeat(40), 'c'.repeat(64)]) {
      expect(pageContextValueLooksSecret(v), `${v.length} 位 hex 应判为凭据`).toBe(true)
    }
  })

  it('uuid 不被误杀（实例/节点 id 常是 uuid，误杀会让上下文直接失效）', () => {
    const uuid = '3f2504e0-4f89-11d3-9a0c-0305e82c3301'
    expect(pageContextValueLooksSecret(uuid), 'uuid 含连字符，不匹配 hex 判据').toBe(false)
    const parsed = JSON.parse(
      serializePageContext({
        pageKey: 'instances',
        filters: { instanceId: uuid },
      }) as string,
    )
    expect(parsed.filters.instanceId, 'uuid 应正常外发').toBe(uuid)
  })

  it('普通筛选词不被误杀', () => {
    for (const v of ['RUNNING', 'survival-1', 'beacon-1', '生产环境', 'q=123']) {
      expect(pageContextValueLooksSecret(v), `${v} 不该被判为凭据`).toBe(false)
    }
  })

  it('判定发生在截断之前（JWT 超 64 字符仍能被识别）', () => {
    // 若实现先 slice(0,64) 再判定，三段结构会被截断，正则必然失配——
    // 这是「先裁剪再校验」的经典顺序错误，会让整个判据静默失效。
    const longJwt =
      'eyJhbGciOiJIUzI1NiJ9.' + 'a'.repeat(80) + '.' + 'b'.repeat(40)
    expect(longJwt.length, '构造的 JWT 应超过 64 字符上限').toBeGreaterThan(PAGE_CONTEXT_MAX_FILTER_LEN)
    expect(pageContextValueLooksSecret(longJwt)).toBe(true)
    expect(
      serializePageContext({ pageKey: 'instances', filters: { q: longJwt } }),
      '超长 JWT 仍须被丢弃，不能被截断后当成普通值外发（快照退化为 null）',
    ).toBeNull()
  })

  it('模式表非空（防止有人清空它而测试全绿）', () => {
    expect(PAGE_CONTEXT_SECRET_VALUE_PATTERNS.length).toBeGreaterThan(0)
  })
})

/**
 * 页面卸载后上下文必须**真的清空**（FR-518 §8）。
 *
 * 验收原文后半句「助手不再声称『你正在看 X』」是**观察结论**，不是可直接断言的量——
 * 要判断模型"有没有声称"，得让它生成自然语言，措辞每次不同、判定主观。
 *
 * 但它的**因**是可精确测的：页面卸载后，下一轮外发载荷里不能还带着旧页面的
 * `pageKey` / 实体 / 筛选。只要载荷干净，模型就没有材料说「你正在看 X」。
 * 所以这里断言的是结果链的上游，而不是去猜模型措辞。
 */
describe('页面卸载后上下文清空', () => {
  it('卸载时把注册表置空，随后序列化快照为空', () => {
    // 模拟页面挂载：注册上下文
    setPageContext({
      pageKey: 'instances',
      entities: [{ type: 'instance', id: 'inst-1' }],
      filters: { status: 'RUNNING' },
    })
    const whileMounted = serializePageContext(getPageContext())
    expect(whileMounted, '挂载期间应有快照').not.toBeNull()
    expect(whileMounted).toContain('instances')

    // 模拟页面卸载：usePageContext 的 cleanup 就是 setPageContext(null)
    setPageContext(null)

    expect(getPageContext(), '卸载后注册表应为 null').toBeNull()
    expect(
      serializePageContext(getPageContext()),
      '卸载后序列化快照必须为空 —— 否则下一轮仍会把旧页面外发给模型',
    ).toBeNull()
  })

  it('对照：不卸载时会带着上下文外发（证明上面的空不是测量失灵）', () => {
    setPageContext({ pageKey: 'logs', entities: [{ type: 'instance', id: 'inst-2' }] })
    const snap = serializePageContext(getPageContext())
    expect(snap, '未卸载时快照应非空').not.toBeNull()
    expect(snap).toContain('logs')
    setPageContext(null)
  })
})

/**
 * 走真实组件生命周期：`usePageContext` 的 cleanup 是否真的在卸载时把上下文清掉。
 *
 * 上一条用例直接调 `setPageContext(null)`，只证明「置空后快照为空」——
 * 但没有证明**卸载会触发置空**。这里用一个真实挂载的组件卸载来验证
 * 「页面卸载 → 上下文清空」这条因果链本身。
 */
describe('usePageContext 卸载即清空（真实生命周期）', () => {
  function Probe({ pageKey, entityId }: { pageKey: string; entityId: string }) {
    usePageContext(
      useMemo(
        () => ({ pageKey, entities: [{ type: 'instance' as const, id: entityId }] }),
        [pageKey, entityId],
      ),
    )
    return <div data-testid="probe" />
  }

  it('组件卸载后注册表变 null，快照为空', () => {
    const { unmount, getByTestId } = render(
      <Probe pageKey="instances" entityId="inst-mount" />,
    )
    expect(getByTestId('probe')).toBeTruthy()

    const mounted = serializePageContext(getPageContext())
    expect(mounted, '挂载期间应有上下文').not.toBeNull()
    expect(mounted).toContain('instances')

    unmount() // ← 真实卸载

    expect(getPageContext(), '卸载后注册表应为 null').toBeNull()
    expect(
      serializePageContext(getPageContext()),
      '卸载后外发快照必须为空：模型拿不到旧页面，自然不会再「声称你正在看 X」',
    ).toBeNull()
  })

  it('切换页面（key 变化）时旧上下文被新上下文替换，不残留', () => {
    const { rerender } = render(<Probe pageKey="instances" entityId="a" />)
    expect(serializePageContext(getPageContext())).toContain('instances')

    rerender(<Probe pageKey="logs" entityId="b" />)
    const after = serializePageContext(getPageContext())
    expect(after, '切页后应是新页面上下文').toContain('logs')
    expect(after, '旧页面的实体不应残留').not.toContain('instances')
  })
})

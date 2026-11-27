import { describe, expect, it } from 'vitest'
import {
  evaluateReadiness,
  formatBytes,
  summarizeReadiness,
  worstLevel,
  type ReadinessInput,
} from './runtime-readiness'

/** 就绪的基线输入：各判定默认值都是 ok。 */
function base(over: Partial<ReadinessInput> = {}): ReadinessInput {
  return {
    nodeOnline: true,
    jdks: [{ managed: true, majorVersion: 21, refCount: 2 }],
    runtimes: [{ type: 'nodejs', majorVersion: 22 }],
    packages: { pm: 'pnpm', globalPackageCount: 3 },
    logRuntime: { instances: [{ namespace: 'hot', state: 'RUNNING', health_ok: true }] },
    logAssetCached: true,
    logDiskPct: 42,
    logDiskUsedBytes: 3 * 1024 ** 3,
    loaded: { jdks: true, runtimes: true, log: true },
    ...over,
  }
}

const byKey = (items: ReturnType<typeof evaluateReadiness>, key: string) =>
  items.find((i) => i.key === key)!

describe('evaluateReadiness：Java', () => {
  it('无 JDK → blocked 并给出安装动作（Java 服起不来，不是隐患而是不可用）', () => {
    const item = byKey(evaluateReadiness(base({ jdks: [] })), 'java')
    expect(item.level).toBe('blocked')
    expect(item.action?.key).toBe('installJDK')
  })

  it('有 JDK 但零引用仍是 ok：刚装完还没建实例不代表装失败', () => {
    const item = byKey(
      evaluateReadiness(base({ jdks: [{ managed: true, majorVersion: 21, refCount: 0 }] })),
      'java',
    )
    expect(item.level).toBe('ok')
    expect(item.headline).toBe('1 个可用')
  })

  it('多版本汇总：headline 报总数，detail 列出全部大版本与托管数', () => {
    const item = byKey(
      evaluateReadiness(
        base({
          jdks: [
            { managed: true, majorVersion: 21, refCount: 3 },
            { managed: false, majorVersion: 17, refCount: 0 },
          ],
        }),
      ),
      'java',
    )
    expect(item.headline).toBe('2 个可用')
    expect(item.detail).toContain('Java 21 / 17')
    expect(item.detail).toContain('托管 1')
  })

  it('未加载完 → unknown 而不是 blocked（取数中不能误报缺 JDK）', () => {
    const item = byKey(
      evaluateReadiness(base({ loaded: { jdks: false, runtimes: true, log: true } })),
      'java',
    )
    expect(item.level).toBe('unknown')
  })
})

describe('evaluateReadiness：日志平台', () => {
  it('资产未下发且无实例 → blocked，动作是下发资产', () => {
    const item = byKey(
      evaluateReadiness(base({ logAssetCached: false, logRuntime: { instances: [] } })),
      'log',
    )
    expect(item.level).toBe('blocked')
    expect(item.action?.key).toBe('dispatchLogAsset')
  })

  it('资产已缓存但实例未启动 → blocked，动作是启动运行时', () => {
    const item = byKey(
      evaluateReadiness(
        base({ logRuntime: { instances: [{ namespace: 'hot', state: 'STOPPED' }] } }),
      ),
      'log',
    )
    expect(item.level).toBe('blocked')
    expect(item.action?.key).toBe('startLogRuntime')
  })

  it('实例运行中 + 磁盘 85% → warn（能跑但要调上限/缩保留期）', () => {
    const item = byKey(evaluateReadiness(base({ logDiskPct: 85 })), 'log')
    expect(item.level).toBe('warn')
    expect(item.action?.key).toBe('openLogStorage')
  })

  it('实例运行中 + 磁盘 95% → blocked（超危险水位会丢日志）', () => {
    const item = byKey(evaluateReadiness(base({ logDiskPct: 95 })), 'log')
    expect(item.level).toBe('blocked')
  })

  it('pct 算不出来时回落到已用字节，不编造百分比', () => {
    const item = byKey(
      evaluateReadiness(base({ logDiskPct: null, logDiskUsedBytes: 41 * 1024 ** 3 })),
      'log',
    )
    expect(item.level).toBe('ok')
    expect(item.detail).toContain('41 GiB')
    expect(item.detail).not.toContain('%')
  })
})

describe('evaluateReadiness：Node 与包管理', () => {
  it('未安装 Node.js → warn（不影响 Java 服，但工具链不可用）', () => {
    const item = byKey(evaluateReadiness(base({ runtimes: [] })), 'node')
    expect(item.level).toBe('warn')
    expect(item.action?.key).toBe('installNode')
  })

  it('包管理器未配置 → warn 且指向「配置包管理器」，不是「安装 Node」', () => {
    /*
      【这条断言改过，原因是原实现错了】原先 package 未配置时给的 action 是 `installNode`，
      于是那个写着「配置包管理器」的按钮点下去会跳到装 Node 的分段——文案与落地内容打架。
      现在用独立的 `openPackages`（落到包管理 sub）。
      钉住 key 就够了：它是"按钮带用户去哪"的唯一标识。
    */
    const item = byKey(evaluateReadiness(base({ packages: { pm: null } })), 'packages')
    expect(item.level).toBe('warn')
    expect(item.action?.key).toBe('openPackages')
    expect(item.action?.label).toBe('配置包管理器')
  })
})

describe('evaluateReadiness：离线与汇总', () => {
  it('节点离线时 ok/warn 一律压成 blocked，unknown 保留（取数失败不该被「离线」掩盖）', () => {
    const items = evaluateReadiness(
      base({ nodeOnline: false, loaded: { jdks: false, runtimes: true, log: true } }),
    )
    expect(byKey(items, 'java').level).toBe('unknown')
    expect(byKey(items, 'node').level).toBe('blocked')
    expect(byKey(items, 'node').detail).toContain('节点离线')
  })

  it('worstLevel：有 blocked 就是 blocked，其次 warn，再次 unknown，全 ok 才 ok', () => {
    expect(worstLevel(evaluateReadiness(base()))).toBe('ok')
    expect(worstLevel(evaluateReadiness(base({ runtimes: [] })))).toBe('warn')
    expect(worstLevel(evaluateReadiness(base({ jdks: [] })))).toBe('blocked')
    expect(
      worstLevel(evaluateReadiness(base({ loaded: { jdks: false, runtimes: false, log: false } }))),
    ).toBe('unknown')
  })

  it('summarizeReadiness：折叠态必须给出结论，不能收起来就什么都不说', () => {
    expect(summarizeReadiness(evaluateReadiness(base()))).toBe('运行时全部就绪')
    expect(summarizeReadiness(evaluateReadiness(base({ jdks: [] })))).toContain('需处理')
    expect(summarizeReadiness(evaluateReadiness(base({ runtimes: [] })))).toContain('隐患')
  })

  it('4 条判定的 key 与顺序固定：Java → Node → 日志 → 包管理', () => {
    const keys = evaluateReadiness(base()).map((i) => i.key)
    expect(keys).toEqual(['java', 'node', 'log', 'packages'])
  })

  it('包管理器未加载完或取数报错时返回 unknown，不误报「未配置」', () => {
    const items = evaluateReadiness(
      base({
        packages: { pm: null },
        loaded: { jdks: true, runtimes: true, log: true, packages: false },
      }),
    )
    expect(byKey(items, 'packages').level).toBe('unknown')
  })
})

describe('formatBytes', () => {
  it('小于 1KiB 显示整字节，大数取整、小数留一位', () => {
    expect(formatBytes(512)).toBe('512 B')
    expect(formatBytes(1024)).toBe('1 KiB')
    expect(formatBytes(41 * 1024 ** 3)).toBe('41 GiB')
    expect(formatBytes(1536)).toBe('1.5 KiB')
  })

  it('非法/零值不抛错，回落到 0 B', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(Number.NaN)).toBe('0 B')
    expect(formatBytes(-5)).toBe('0 B')
  })
})

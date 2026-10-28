import { beforeAll, describe, expect, it, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, cleanup } from '@testing-library/react'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ReactNode } from 'react'
import { AssistantChatPanel } from '@/components/views/assistant/AssistantChatPanel'

/**
 * 聊天面板 Markdown 渲染的**安全断言**（FR-519 §7 后三条）。
 *
 * 模型输出是不可信内容：它可能被上游污染、被提示注入操纵，或单纯被诱导产出
 * 带外链的图片。这里守两道线：
 *
 * 1. **不执行、不渲染为 HTML** —— `<script>`、`<img onerror>` 这类原始 HTML
 *    不得变成真实 DOM 节点。（`react-markdown` 默认不解析原始 HTML，
 *    本测试锁住这个默认值，防止将来有人加 `rehype-raw` 时静默失去防护。）
 * 2. **不产生第三方网络请求** —— 图片一律渲染为纯文本占位，`iframe`/`video`/`audio`
 *    同样不得出现。只要 DOM 里没有这些标签，浏览器就不会去拉外部资源。
 *
 * 关于「零请求」的断言层次：jsdom 不发真实网络，故此处断言 DOM 中不存在会发起
 * 请求的元素（`img`/`iframe`/`video`/`audio`/`source`/`object`/`embed`）。
 * 真实浏览器下的请求探针断言另见 E2E；两者互补，缺一不可——
 * DOM 断言快且稳，请求探针才能证明「真的没发出去」。
 */

const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({ lng: 'zh-CN', fallbackLng: 'zh-CN', resources: { 'zh-CN': { translation: {} } } })

beforeAll(() => {
  globalThis.ResizeObserver ??= class ResizeObserver {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
})

/** 渲染一条**已完成**的 assistant 消息（流式期间走纯文本分支，不经过 Markdown）。 */
function renderAssistant(content: string) {
  const ui = (
    <I18nextProvider i18n={testI18n}>
      <AssistantChatPanel
        messages={[{ key: 'run-1', role: 'assistant', content }]}
        running={false}
        onSend={() => {}}
        onStop={() => {}}
      />
    </I18nextProvider>
  ) as ReactNode
  return render(ui)
}

/** 会触发浏览器发起网络请求的标签集合。 */
const REQUEST_TAGS = ['img', 'iframe', 'video', 'audio', 'source', 'object', 'embed', 'picture'] as const

describe('Markdown 渲染 · 原始 HTML 不可执行', () => {
  it('`<script>` 不产生 script 元素', () => {
    const { container } = renderAssistant('看看这个：<script>window.__pwned = 1</script>')
    expect(container.querySelector('script')).toBeNull()
    expect((window as unknown as { __pwned?: number }).__pwned).toBeUndefined()
  })

  it('`<img onerror>` 不产生 img 元素，也不触发内联事件', () => {
    const { container } = renderAssistant('<img src=x onerror="window.__pwned2 = 1">')
    // 关键：raw HTML 若被解析，这里会出现 img；没有 img 就不存在 onerror 的执行机会
    expect(container.querySelector('img')).toBeNull()
    expect((window as unknown as { __pwned2?: number }).__pwned2).toBeUndefined()
  })

  it('`<iframe>` / `<video>` / `<audio>` 均不产生对应元素', () => {
    const { container } = renderAssistant(
      '<iframe src="https://evil.example/frame"></iframe>' +
        '<video src="https://evil.example/v.mp4"></video>' +
        '<audio src="https://evil.example/a.mp3"></audio>',
    )
    for (const tag of ['iframe', 'video', 'audio'] as const) {
      expect(container.querySelector(tag), `${tag} 不该出现`).toBeNull()
    }
  })
})

describe('Markdown 渲染 · 不产生第三方请求', () => {
  it('外链图片渲染为纯文本占位，不生成 img', () => {
    const { container, getByText } = renderAssistant('![截图](https://example.com/x.png)')
    expect(container.querySelector('img')).toBeNull()
    // 占位而非静默丢弃：让人知道模型确实输出过图，只是没加载
    expect(getByText(/图片已省略/)).toBeTruthy()
  })

  it('站内相对路径图片同样不加载（避免把不可信路径打到自己后端）', () => {
    const { container } = renderAssistant('![x](/api/artifacts/secret.png)')
    expect(container.querySelector('img')).toBeNull()
  })

  it('任何会发起请求的标签都不出现', () => {
    const { container } = renderAssistant(
      '![a](https://x.example/a.png)\n\n<img src="https://x.example/b.png">\n\n<iframe src="https://x.example/c"></iframe>',
    )
    for (const tag of REQUEST_TAGS) {
      expect(container.querySelector(tag), `${tag} 不该出现`).toBeNull()
    }
  })
})

describe('Markdown 渲染 · 链接协议', () => {
  it('`javascript:` 协议的链接不会被写成可点击的 javascript href', () => {
    const { container } = renderAssistant('[点我](javascript:alert(1))')
    const anchors = Array.from(container.querySelectorAll('a'))
    for (const a of anchors) {
      const href = a.getAttribute('href') ?? ''
      expect(href.toLowerCase().startsWith('javascript:'), `href 不得为 javascript:，实际 ${href}`).toBe(false)
    }
  })

  it('正常外链带 noopener/noreferrer，且不在应用内跳转', () => {
    const { container } = renderAssistant('[文档](https://example.com/doc)')
    const a = container.querySelector('a')
    expect(a).not.toBeNull()
    expect(a!.getAttribute('target')).toBe('_blank')
    expect(a!.getAttribute('rel')).toContain('noopener')
    expect(a!.getAttribute('rel')).toContain('noreferrer')
  })
})

/**
 * 外链「经宿主确认」才打开（FR-519 §7 末条）。
 *
 * 模型输出里的链接属不可信内容：直接开新标签等于把「诱导用户离开宿主」做成了零成本，
 * 而且钓鱼链可以在用户毫无察觉时完成导航。这里锁住三件事：
 * 点击只弹确认、确认才开门、取消绝不开门。
 */
describe('外链 · 经宿主确认才打开', () => {
  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
  })

  /** 打开确认框、或在取消后清除弹窗残留。 */
  const clickLink = (text: string) => fireEvent.click(screen.getByText(text))

  it('点击外链只弹确认，不立即调用 window.open', async () => {
    const openSpy = vi.spyOn(window, 'open').mockImplementation(() => null)
    renderAssistant('[文档](https://example.com/doc)')
    clickLink('文档')
    expect(await screen.findByRole('dialog')).toBeTruthy()
    expect(openSpy).not.toHaveBeenCalled()
  })

  it('确认后才在新标签打开，且带 noopener/noreferrer', async () => {
    const openSpy = vi.spyOn(window, 'open').mockImplementation(() => null)
    renderAssistant('[文档](https://example.com/doc)')
    clickLink('文档')
    fireEvent.click(await screen.findByRole('button', { name: '打开' }))
    expect(openSpy).toHaveBeenCalledWith('https://example.com/doc', '_blank', 'noopener,noreferrer')
  })

  it('取消则不打开', async () => {
    const openSpy = vi.spyOn(window, 'open').mockImplementation(() => null)
    renderAssistant('[文档](https://example.com/doc)')
    clickLink('文档')
    fireEvent.click(await screen.findByRole('button', { name: '取消' }))
    expect(openSpy).not.toHaveBeenCalled()
  })

  it('站内相对链接不弹确认（站内导航不代表离开宿主）', () => {
    renderAssistant('[实例](/instances/1)')
    clickLink('实例')
    expect(screen.queryByText(/打开外部链接/)).toBeNull()
  })
})

/**
 * Markdown 结构渲染（FR-519 §7「列表、粗体、行内代码、代码块、表格均正确呈现」）。
 *
 * 与上面的安全断言互补：那边管「不该渲染的东西不渲染」，这边管「该渲染的真的渲染了」。
 * 只靠安全断言会出现一种假绿——把渲染器整个退化成纯文本，安全断言全过、结构全丢。
 */
describe('Markdown 渲染 · 结构完整性', () => {
  it('列表 / 粗体 / 行内代码 / 代码块 / 表格都渲染成对应元素', () => {
    const md = [
      '**加粗** 与 `inline-code`',
      '',
      '- 列表项一',
      '- 列表项二',
      '',
      '```ts',
      'const x = 1',
      '```',
      '',
      '| 列A | 列B |',
      '| --- | --- |',
      '| a1  | b1  |',
    ].join('\n')

    const { container } = renderAssistant(md)

    expect(container.querySelector('strong'), '粗体应渲染为 <strong>').toBeTruthy()
    expect(container.querySelector('code'), '行内代码应渲染为 <code>').toBeTruthy()
    expect(container.querySelector('ul > li'), '无序列表应渲染为 ul>li').toBeTruthy()
    expect(container.querySelector('pre code'), '代码块应渲染为 pre>code').toBeTruthy()
    expect(container.querySelector('table'), '表格应渲染为 <table>').toBeTruthy()
    expect(container.querySelectorAll('td').length, '表格应有数据单元格').toBeGreaterThan(0)
  })
})

/**
 * 流式期间按纯文本追加（FR-519 §7 末条）。
 *
 * 未闭合的 ``` 或 ** 在流式途中会渲染成「半截结构」——用户看到的是一个格式错乱的气泡，
 * 而且每次 text_delta 都可能让它跳变一次。所以流式期间必须走纯文本分支，
 * **完成之后**才切 Markdown。
 */
describe('流式 · 不渲染半截 Markdown', () => {
  function renderStreaming(content: string) {
    const ui = (
      <I18nextProvider i18n={testI18n}>
        <AssistantChatPanel
          messages={[{ key: 'r1', role: 'assistant', content, streaming: true }]}
          running
          onSend={() => {}}
          onStop={() => {}}
        />
      </I18nextProvider>
    ) as ReactNode
    return render(ui)
  }

  it('流式中的未闭合 `**加粗` 不产生 <strong>，原样显示文本', () => {
    const { container } = renderStreaming('这是**尚未闭合的加粗')
    expect(container.querySelector('strong'), '流式期间不应出现半截 <strong>').toBeNull()
    expect(container.textContent, '原文应完整可见').toContain('尚未闭合的加粗')
  })

  it('流式中的未闭合代码块不产生 <pre>', () => {
    const { container } = renderStreaming('下面是代码：\n```ts\nconst a = 1')
    expect(container.querySelector('pre'), '未闭合的 ``` 不该渲染成代码块').toBeNull()
    expect(container.textContent).toContain('const a = 1')
  })

  it('对照：同样内容在完成后会渲染出结构（证明上面的 null 不是测量失灵）', () => {
    const ui = (
      <I18nextProvider i18n={testI18n}>
        <AssistantChatPanel
          messages={[{ key: 'r2', role: 'assistant', content: '这是**已闭合的加粗**' }]}
          running={false}
          onSend={() => {}}
          onStop={() => {}}
        />
      </I18nextProvider>
    ) as ReactNode
    const { container } = render(ui)
    expect(container.querySelector('strong'), '完成后应渲染 <strong>').toBeTruthy()
  })
})

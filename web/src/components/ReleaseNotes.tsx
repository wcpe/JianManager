import { type ReactNode, type AnchorHTMLAttributes } from 'react'
import ReactMarkdown, { type Components } from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { isSafeExternalLink, confirmOpenMessage } from './release-notes-link'

/** ReleaseNotes 的 props。 */
interface ReleaseNotesProps {
  /** 更新说明原文（GitHub release body 的 markdown，FR-186）。 */
  markdown: string
}

/**
 * 更新说明 Markdown 渲染（FR-186，替换原 `<pre>` 纯文本）。
 * 用 react-markdown + remark-gfm 渲染标题/列表/代码块/链接/表格；不渲染裸 HTML（react-markdown
 * 默认即禁用，防 XSS，见 spec §不做）。长内容 `max-h` + 主题化滚动条（FR-176），暗亮主题随 token。
 * 链接不在应用内直跳，经「宿主确认」后在新标签打开（{@link isSafeExternalLink}）。
 */
export function ReleaseNotes({ markdown }: ReleaseNotesProps) {
  return (
    <div className="markdown-body max-h-72 overflow-auto text-sm text-foreground">
      <ReactMarkdown remarkPlugins={[remarkGfm]} components={mdComponents}>
        {markdown}
      </ReactMarkdown>
    </div>
  )
}

/** 点击外链：阻止默认导航，经 window.confirm 确认后在新标签打开（noopener）。 */
function handleExternalClick(href: string, e: React.MouseEvent) {
  e.preventDefault()
  if (!isSafeExternalLink(href)) return
  if (window.confirm(confirmOpenMessage(href))) {
    window.open(href, '_blank', 'noopener,noreferrer')
  }
}

/**
 * 自定义渲染器：全部用主题 token 着色，与暗/亮 + 双主题随 CSS 变量切换；
 * 链接走宿主确认；代码块/行内代码、标题、列表、表格、引用均显式样式（markdown-body 无外部 CSS）。
 */
const mdComponents: Components = {
  a: ({ href, children, ...rest }: AnchorHTMLAttributes<HTMLAnchorElement> & { children?: ReactNode }) => (
    <a
      href={href}
      onClick={(e) => href && handleExternalClick(href, e)}
      className="text-primary underline underline-offset-2 hover:opacity-80 cursor-pointer"
      {...rest}
    >
      {children}
    </a>
  ),
  h1: ({ children }) => <h1 className="text-base font-semibold mt-3 mb-1.5 first:mt-0">{children}</h1>,
  h2: ({ children }) => <h2 className="text-sm font-semibold mt-3 mb-1.5 first:mt-0">{children}</h2>,
  h3: ({ children }) => <h3 className="text-sm font-medium mt-2.5 mb-1 first:mt-0">{children}</h3>,
  h4: ({ children }) => <h4 className="text-xs font-medium mt-2 mb-1 first:mt-0 text-muted-foreground">{children}</h4>,
  p: ({ children }) => <p className="my-1.5 leading-relaxed">{children}</p>,
  ul: ({ children }) => <ul className="my-1.5 ml-4 list-disc space-y-0.5">{children}</ul>,
  ol: ({ children }) => <ol className="my-1.5 ml-4 list-decimal space-y-0.5">{children}</ol>,
  li: ({ children }) => <li className="leading-relaxed">{children}</li>,
  blockquote: ({ children }) => (
    <blockquote className="my-1.5 border-l-2 border-border pl-3 text-muted-foreground">{children}</blockquote>
  ),
  hr: () => <hr className="my-3 border-border" />,
  // 行内代码与代码块共用 <code>；react-markdown v9 不再传 inline，按是否含换行/语言类名区分由 pre 包裹。
  code: ({ children, className }) => (
    <code className={`rounded bg-muted px-1 py-0.5 font-mono text-[0.85em] ${className ?? ''}`}>{children}</code>
  ),
  pre: ({ children }) => (
    <pre className="my-2 overflow-auto rounded-md bg-muted p-3 font-mono text-xs leading-relaxed">{children}</pre>
  ),
  table: ({ children }) => (
    <div className="my-2 overflow-x-auto">
      <table className="w-full border-collapse text-xs">{children}</table>
    </div>
  ),
  th: ({ children }) => <th className="border border-border bg-muted/60 px-2 py-1 text-left font-medium">{children}</th>,
  td: ({ children }) => <td className="border border-border px-2 py-1">{children}</td>,
  img: ({ src, alt }) => <img src={typeof src === 'string' ? src : undefined} alt={alt} className="my-2 max-w-full rounded" />,
}

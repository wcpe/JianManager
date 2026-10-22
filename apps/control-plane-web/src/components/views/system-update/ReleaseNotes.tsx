import { useState, type ReactNode, type AnchorHTMLAttributes } from 'react'
import { useTranslation } from 'react-i18next'
import ReactMarkdown, { type Components } from 'react-markdown'
import remarkGfm from 'remark-gfm'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { Button } from '@jianmanager/ui/components/button'
import { Separator } from '@jianmanager/ui/components/separator'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import { isSafeExternalLink, confirmOpenMessage } from '@/lib/system-update/release-notes-link'

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
  const { t } = useTranslation()
  const [externalHref, setExternalHref] = useState<string | null>(null)
  const openExternal = () => {
    if (!externalHref) return
    window.open(externalHref, '_blank', 'noopener,noreferrer')
    setExternalHref(null)
  }

  return (
    <>
      <div className="markdown-body max-h-72 overflow-auto text-sm text-foreground">
        <ReactMarkdown remarkPlugins={[remarkGfm]} components={createMarkdownComponents(setExternalHref)}>
          {markdown}
        </ReactMarkdown>
      </div>
      <Dialog open={externalHref !== null} onOpenChange={(open) => { if (!open) setExternalHref(null) }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('releaseNotes.openExternalTitle')}</DialogTitle>
            <DialogDescription className="whitespace-pre-wrap">
              {externalHref ? confirmOpenMessage(externalHref) : ''}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setExternalHref(null)}>
              {t('common.cancel')}
            </Button>
            <Button onClick={openExternal}>
              {t('releaseNotes.openExternal')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}

/** 点击外链：阻止默认导航，经共享 Dialog 确认后在新标签打开（noopener）。 */
function handleExternalClick(href: string, e: React.MouseEvent, confirmOpen: (href: string) => void) {
  e.preventDefault()
  if (!isSafeExternalLink(href)) return
  confirmOpen(href)
}

/**
 * 自定义渲染器：全部用主题 token 着色，与暗/亮 + 双主题随 CSS 变量切换；
 * 链接走宿主确认；代码块/行内代码、标题、列表、表格、引用均显式样式（markdown-body 无外部 CSS）。
 */
function createMarkdownComponents(confirmOpen: (href: string) => void): Components {
  return {
  a: ({ href, children, ...rest }: AnchorHTMLAttributes<HTMLAnchorElement> & { children?: ReactNode }) => (
    <a
      href={href}
      onClick={(e) => href && handleExternalClick(href, e, confirmOpen)}
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
  // 分隔线：markdown 的 <hr> 是纯装饰，交给组件库的 Separator（自带 role 语义处理）
  hr: () => <Separator className="my-3" />,
  // 行内代码与代码块共用 <code>；react-markdown v9 不再传 inline，按是否含换行/语言类名区分由 pre 包裹。
  code: ({ children, className }) => (
    <code className={`rounded bg-muted px-1 py-0.5 font-mono text-[0.85em] ${className ?? ''}`}>{children}</code>
  ),
  pre: ({ children }) => (
    <pre className="my-2 overflow-auto rounded-md bg-muted p-3 font-mono text-xs leading-relaxed">{children}</pre>
  ),
  // 表格：交给组件库 Table 系列（自带横向滚动容器，故不再另套 overflow 外壳）。
  // markdown 表格是「网格观感」：保留每格边框，用 border-collapse 覆盖组件默认的 border-separate；
  // 行级边框与悬停高亮对只读文档表无意义（且会与单元格边框双线），故在 TableRow 上抵消。
  table: ({ children }) => (
    <Table className="border-collapse text-xs" containerClassName="my-2">
      {children}
    </Table>
  ),
  thead: ({ children }) => <TableHeader>{children}</TableHeader>,
  tbody: ({ children }) => <TableBody>{children}</TableBody>,
  tr: ({ children }) => <TableRow className="border-b-0 hover:bg-transparent">{children}</TableRow>,
  th: ({ children }) => (
    <TableHead className="border border-border bg-muted/60 px-2 py-1">{children}</TableHead>
  ),
  td: ({ children }) => (
    <TableCell className="whitespace-normal border border-border px-2 py-1">{children}</TableCell>
  ),
  img: ({ src, alt }) => <img src={typeof src === 'string' ? src : undefined} alt={alt} className="my-2 max-w-full rounded" />,
  }
}

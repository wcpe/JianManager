/**
 * Unified diff 着色渲染（FR-141 版本 diff 增删着色）。
 *
 * 逐行判首字符着色，用语义色 token（`--status-*`）：随明暗自适配，且与 Jian 绿 / 青绿双主题正交
 * （状态色不随品牌主题变，见 web/src/index.css 注释「结构色与状态色 --status-* 不动」），
 * 因此增删红绿在两套主题下都稳定成立。
 *
 * 文件/资源版本抽屉（FR-070）与配置版本抽屉（FR-071）共用本组件，避免两处 diff 着色实现分叉。
 */

interface UnifiedDiffProps {
  /** 完整 unified diff 文本（含 `---`/`+++`/`@@` 头与 `+`/`-` 行），按 `\n` 逐行着色。 */
  diff: string
}

/**
 * 按 unified diff 行首字符返回语义着色类：
 * `+` 新增着绿、`-` 删除着红、`@@` 与文件头（`---`/`+++`）着中性蓝、上下文行不着色。
 */
function diffLineClass(line: string): string {
  // 文件头 +++/--- 必须先于 +/- 判定，否则会被当成增删行错误着绿/红。
  if (line.startsWith('+++') || line.startsWith('---')) return 'text-status-info'
  if (line.startsWith('@@')) return 'text-status-info font-medium'
  if (line.startsWith('+')) return 'bg-status-success/10 text-status-success'
  if (line.startsWith('-')) return 'bg-status-danger/10 text-status-danger'
  return '' // 上下文行不着色，继承 <pre> 默认前景
}

/** 逐行渲染 unified diff 文本，增删行加语义底色 + 前景着色（FR-141）。 */
export function UnifiedDiff({ diff }: UnifiedDiffProps) {
  return (
    <pre className="font-mono text-[10px] leading-5">
      {diff.split('\n').map((line, index) => (
        <span key={`${index}:${line}`} className={`block whitespace-pre-wrap px-1 ${diffLineClass(line)}`}>
          {line || ' '}
        </span>
      ))}
    </pre>
  )
}

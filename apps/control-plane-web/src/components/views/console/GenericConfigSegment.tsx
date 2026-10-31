import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { FileCog, FileText } from 'lucide-react'
import { cn } from '@jianmanager/ui'
import { Button } from '@jianmanager/ui/components/button'
import { Card } from '@jianmanager/ui/components/card'
import { Panel } from '@jianmanager/ui/components/panel'

/** 发现的配置文件条目（容器经 useConfigDiscover 取数）。 */
export interface DiscoveredConfigFile {
  path: string
  supported: boolean
}

export interface GenericConfigSegmentProps {
  /** 发现的配置文件清单。 */
  files: DiscoveredConfigFile[]
  isLoading?: boolean
  /** 
   * 编辑器插槽（应用侧 `ConfigFileEditor`，含 schema 表单 / 原文双模式与版本能力）。
   * 未选文件时该插槽不渲染。
   */
  renderEditor: (args: { path: string; onClose: () => void }) => ReactNode
}

/** 
 * 结构化配置编辑分段（衔接450 复用）——不写死产品名。
 *
 * 递归发现实例工作目录下的配置文件（`config_discover`），左侧选择、右侧编辑：
 * 复用应用侧注入的编辑器获得「schema 表单 / 原文文本」双模式与配置版本能力。
 * BC 的 `config.yml`（监听端口 / online_mode / servers）与原生二进制的配置文件走同一条路径。
 *
 *§2.2.4 收口说明：BC `config.yml` 的表单覆盖「确实是标量」的关键项
 * （listeners.host / max_players / motd / query_port、ip_forward、online_mode、
 * connection_throttle[_limit]），键名与 `parseFlatYAML` 的点号路径精确对齐。
 *
 * `servers` 与 `permissions.*` **有意不提供表单**，只能原文编辑：前者是「子服名 → 属性」的
 * 映射、子服名由运维自定义无法穷举，后者是 `- 节点` 列表；两者都不是「一个键 → 一个标量」，
 * 硬塞进表单只会渲染出真实配置里不存在的输入框，保存即覆盖掉整棵子树。子服拓扑请走拓扑页。
 *
 * 列表型的 `listeners.*` 仅在该列表**恰好一项**时可安全写入；多项时代码会返回明确错误
 * 而不是替你猜改哪一个（见后端 `config_patch.go` 的 `yamlDescend`）。
 */
export function GenericConfigSegment({ files, isLoading = false, renderEditor }: GenericConfigSegmentProps) {
  const { t } = useTranslation()
  const [selected, setSelected] = useState<string | null>(null)

  // 未选时默认第一个（发现结果就绪后）。
  const active = selected ?? files[0]?.path ?? null

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-3 p-4" data-testid="config-segment">
      <div className="flex min-h-0 flex-1 flex-col gap-3 lg:flex-row">
        <Panel className="lg:w-72 lg:flex-none" title={t('config2.title')}>
          <div className="max-h-72 overflow-auto p-1 lg:max-h-none">
            {isLoading ? (
              <p className="p-2 text-xs text-muted-foreground">{t('common.loading')}</p>
            ) : files.length === 0 ? (
              <p className="p-2 text-xs text-muted-foreground">{t('config2.empty')}</p>
            ) : (
              <ul className="space-y-0.5">
                {files.map((f) => (
                  <li key={f.path}>
                    <Button
                      type="button"
                      variant="invisible"
                      size="auto"
                      onClick={() => setSelected(f.path)}
                      aria-pressed={active === f.path}
                      className={cn(
                        'flex w-full items-center gap-1.5 truncate rounded px-2 py-1 text-left text-xs font-normal transition-colors',
                        active === f.path
                          ? 'bg-accent font-medium text-primary'
                          : 'text-muted-foreground hover:bg-muted hover:text-foreground',
                      )}
                    >
                      {f.supported ? (
                        <FileCog className="size-3.5 shrink-0" />
                      ) : (
                        <FileText className="size-3.5 shrink-0" />
                      )}
                      <span className="truncate">{f.path}</span>
                    </Button>
                  </li>
                ))}
              </ul>
            )}
          </div>
        </Panel>

        <Card className="min-h-[320px] min-w-0 flex-1 overflow-hidden py-0">
          {active ? (
            renderEditor({ path: active, onClose: () => setSelected(null) })
          ) : (
            <p className="p-4 text-xs text-muted-foreground">{t('config2.selectHint')}</p>
          )}
        </Card>
      </div>
    </div>
  )
}

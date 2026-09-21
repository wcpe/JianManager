import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { FileCog, FileText } from 'lucide-react'

import { cn } from '@jianmanager/ui'
import { Panel } from '@jianmanager/ui/components/panel'
import { useConfigDiscover } from '@/api/configs'
import ConfigFileEditor from '@/components/config-explorer/ConfigFileEditor'

/**
 * 结构化配置编辑分段（FR-451 衔接，FR-449/450 复用）——不写死产品名。
 *
 * 递归发现实例工作目录下的配置文件（`config_discover`），左侧选择、右侧编辑：
 * 复用 {@link ConfigFileEditor} 获得「schema 表单 / 原文文本」双模式与配置版本能力。
 * BC 的 `config.yml`（监听端口 / online_mode / servers）与原生二进制的配置文件走同一条路径。
 */
export default function GenericConfigSegment({ instanceId }: { instanceId: number }) {
  const { t } = useTranslation()
  const { data, isLoading } = useConfigDiscover(instanceId)
  const [selected, setSelected] = useState<string | null>(null)

  const files = data?.files ?? []
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
                    <button
                      type="button"
                      onClick={() => setSelected(f.path)}
                      aria-pressed={active === f.path}
                      className={cn(
                        'flex w-full items-center gap-1.5 truncate rounded px-2 py-1 text-left text-xs transition-colors',
                        active === f.path ? 'bg-accent font-medium text-primary' : 'text-muted-foreground hover:bg-muted hover:text-foreground',
                      )}
                    >
                      {f.supported ? <FileCog className="size-3.5 shrink-0" /> : <FileText className="size-3.5 shrink-0" />}
                      <span className="truncate">{f.path}</span>
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </div>
        </Panel>

        <div className="flex min-h-[320px] min-w-0 flex-1 flex-col overflow-hidden rounded-lg border bg-card shadow-soft">
          {active ? (
            <ConfigFileEditor
              instanceId={instanceId}
              path={active}
              name={active.split('/').pop() ?? active}
              onClose={() => setSelected(null)}
              onAfterSave={() => { /* 读取查询在保存后自动失效刷新 */ }}
              onOpenVersions={() => { /* 版本抽屉仅在文件配置页；此处不阻塞编辑 */ }}
              onDirtyChange={() => { /* 详情页不接管切换守卫 */ }}
            />
          ) : (
            <p className="p-4 text-xs text-muted-foreground">{t('config2.selectHint')}</p>
          )}
        </div>
      </div>
    </div>
  )
}

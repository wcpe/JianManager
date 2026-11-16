import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Download, Loader2 } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { Combobox } from '@jianmanager/ui/components/combobox'
import { useSubmitGuard } from '@/lib/hooks/use-submit-guard'
import { NODE_MAJORS, NODE_MAJOR_OPTIONS } from '@/lib/nodes/node-majors'

/**
 * Node.js 安装段（抽屉里「安装 Node.js」这一页的全部内容）。
 *
 * 【为什么单独一个组件，而不是复用 NodeRuntimeSection】那个组件是「运行时列表 + 扫描 +
 * 安装」的复合体，塞进抽屉会让用户在安装页里看到一堆与本次动作无关的列表行和扫描按钮。
 * 安装只需要三件事：选版本 → 看将装什么 → 提交。
 *
 * 【为什么选版本用下拉而不是输入框】自由输入没有任何约束：输 `23` 会装到非 LTS，
 * 输 `999` 要等后端才报错。下拉把「哪个是 LTS、维护到什么时候」直接写在选项里——
 * 那才是用户选版本时真正要判断的事。
 */
export interface NodeInstallSectionProps {
  /** 安装是否在途（外壳 mutation 的 isPending，唯一真相）。 */
  installing?: boolean
  /** 提交安装，返回是否成功（成功则关闭抽屉由外壳决定）。 */
  onInstall: (major: number) => Promise<boolean>
}

export function NodeInstallSection({ installing = false, onInstall }: NodeInstallSectionProps) {
  const { t } = useTranslation()
  const [major, setMajor] = useState(String(NODE_MAJOR_OPTIONS[0]?.value ?? 22))
  const majorNum = Number.parseInt(major, 10)
  const valid = Number.isInteger(majorNum) && majorNum > 0

  /*
    同步闸门：这是**异步任务**（202 + taskId），连点真的会起两个下载任务。
    setState 是异步生效的，同一事件周期里连点两次都会读到 false，所以用 ref 挡。
    外壳的 `installing` 负责渲染期禁用，两者一起才完整。
  */
  const guard = useSubmitGuard(async () => {
    if (!valid) return false
    return onInstall(majorNum)
  })

  const picked = NODE_MAJORS.find((m) => String(m.major) === major)

  return (
    <div className="space-y-3">
      <p className="text-xs text-muted-foreground">
        {t(
          'nodes.runtime.nodeInstallHint',
          '给这台节点装一份受管 Node.js。依赖 Node 的插件与 Bot 工具链需要它；Java 服不受影响。',
        )}
      </p>

      <div className="space-y-1.5">
        <label className="text-xs font-medium" htmlFor="node-install-major">
          {t('nodes.runtime.nodeVersion', 'Node.js 版本')}
        </label>
        <Combobox
          id="node-install-major"
          value={major}
          onChange={setMajor}
          options={[...NODE_MAJOR_OPTIONS]}
          /* 不允许自定义：装了非 LTS 或已 EOL 的版本，后续插件行为无法预期，
             而这类问题排查起来比"少一个选项"贵得多。 */
          allowCustom={false}
          placeholder={t('nodes.runtime.nodeVersionPlaceholder', '选择版本')}
          data-testid="node-install-major"
        />
        {picked?.note && (
          <p className="text-[11px] text-muted-foreground" data-testid="node-install-note">
            {picked.note}
          </p>
        )}
      </div>

      <div className="rounded-md border bg-muted/30 px-2.5 py-2 text-xs" data-testid="node-install-preview">
        {picked
          ? t('nodes.runtime.nodeInstallPreview', '将安装 Node.js {{major}}（{{channel}}）', {
              major: picked.major,
              channel: picked.lts ? `LTS ${picked.lts}` : 'Current',
            })
          : t('nodes.runtime.nodeInstallPreviewEmpty', '请选择要安装的版本')}
      </div>

      <div className="flex items-center justify-end gap-2">
        <Button
          onClick={() => void guard.run()}
          disabled={!valid || installing || guard.pending}
          data-testid="node-install-submit"
        >
          {installing || guard.pending ? (
            <Loader2 className="mr-1 size-4 animate-spin" aria-hidden />
          ) : (
            <Download className="mr-1 size-4" aria-hidden />
          )}
          {guard.pending
            ? t('nodes.runtimeLib.dispatching', '正在下发…')
            : t('nodes.runtimeLib.installNode', '安装 Node.js')}
        </Button>
      </div>
    </div>
  )
}

/**
 * @file Input：文本输入框，薄封装原生 input（type / name / disabled / aria-* 一律透传）。
 * @input  lib/utils 的 cn、lib/focus-ring、lib/interaction-overlay 的 disabledState
 * @output Input
 * @sync   改动交互或透传契约时同步 input.test.tsx
 * @since  FR-496
 */
import * as React from "react"

import { cn } from "../lib/utils"
import { focusRing, invalidFieldState } from "../lib/focus-ring"
import { disabledState } from "../lib/interaction-overlay"

/**
 * 文本输入框（FR-496 阶段 6 重做）。
 *
 * 保持"薄封装原生 input"这一契约不变（type / name / disabled / aria-* 一律透传，
 * 受控与非受控都按浏览器语义工作），只把样式取值收敛到共享常量：
 * 焦点环与无效态此前是本文件私有的两串类名，与 Button/Select 各写一份、且已经走偏
 * （有的 3px 有的 2px），现在统一由 lib/focus-ring 提供。
 */
function Input({ className, type, ...props }: React.ComponentProps<"input">) {
  return (
    <input
      type={type}
      data-slot="input"
      className={cn(
        "h-9 w-full min-w-0 rounded-md border border-input bg-transparent px-3 py-1 text-base shadow-xs transition-[color,box-shadow] outline-none selection:bg-primary selection:text-primary-foreground file:inline-flex file:h-7 file:border-0 file:bg-transparent file:text-sm file:font-medium file:text-foreground placeholder:text-muted-foreground md:text-sm dark:bg-input/30",
        // FR-176：焦点环收敛为 2px + 40% 主色的细环（展开即 focus-visible:ring-2 +
        // focus-visible:border-ring），配 border-ring 边线已足够醒目；不得回退成
        // 3px/50% 的粗环——那会糊到邻近文字上。当前文件是全局守护测试的锚点，
        // 类名取值只能从 lib/focus-ring 取。
        focusRing,
        // 无效态描红与危险色焦点环同源：一个无效输入框在失焦时也必须是红的
        invalidFieldState,
        disabledState,
        // 输入框的禁用态额外给 not-allowed 光标：它通常是表单里唯一的可点区域，
        // 「这里不能输入」需要比只降透明度更明确的信号
        "disabled:cursor-not-allowed",
        className
      )}
      {...props}
    />
  )
}

export { Input }

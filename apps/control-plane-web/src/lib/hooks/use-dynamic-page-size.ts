import { useEffect, useState } from 'react'

/**
 * 动态分页大小：按「表格当前能放下多少行」算出每页条数。
 *
 * 【为什么需要动态】固定 20 行/页在两种情况下都不对：
 * - 节点只有 2 台时，一页 20 行意味着容器铺满但内容只占两行，下方一片空白；
 * - 小屏（如 768 高）时，一页 20 行放不下，用户每页都得滚一次——分页就失去了
 *   「一屏看完一页」的意义。
 * 动态算出来后，翻一页正好换一屏内容，分页才真正等价于「换屏」。
 *
 * 【怎么算】用 `ResizeObserver` 观测表格容器的**真实高度**，减去表头与分页条，
 * 再除以单行高度（含边框）。全部为实测值，不做跨设备估算。
 *
 * 【为什么留 1 行余量】表格底部常有半行被裁（浏览器按小数像素布局），
 * 不留余量时最后一行会「露一半」，视觉上像截断。宁可每页少一行。
 */
export function useDynamicPageSize({
  containerRef,
  rowHeight,
  headerHeight,
  footerHeight,
  min = 5,
  max = 100,
}: {
  /** 表格滚动容器（高度由布局决定）。 */
  containerRef: React.RefObject<HTMLElement | null>
  /** 单行高度（含分隔线）。 */
  rowHeight: number
  /** 表头高度。 */
  headerHeight: number
  /** 分页条高度（若分页条在容器外，传 0）。 */
  footerHeight: number
  min?: number
  max?: number
}): number {
  const [size, setSize] = useState(min)

  /*
    ── 为什么需要「重试直到容器出现」 ──

    依赖数组里除 `containerRef`（ref 对象，引用恒定）外全是常量/数字，effect 跑过一次
    就不会再跑。而 ref 的宿主极可能是**条件渲染**的：

      NodesPage 首屏 `listLoading` 为真 → 渲染 DataPanelSkeleton，NodeTable 未挂载；
      卡片布局下同样不渲染 NodeTable。

    于是首次 effect 执行时 `containerRef.current` 是 null。旧实现直接 `return`，
    此后再无人重试 → `size` 永远停在 `min`（节点列表恒 5 行/页，且切布局、
    来回导航都不恢复）。这不是理论风险：默认列表布局首屏必现。

    修法用 rAF 轮询等容器出现（而不是把 `layout`/`listLoading` 传进来）：
    后者要求每个调用方都记得同步这两个参数，漏一个就退化成本缺陷；
    轮询只依赖 ref 本身，对调用方零要求。容器一旦出现即停止轮询。
  */
  useEffect(() => {
    let observer: ResizeObserver | null = null
    let raf = 0
    let disposed = false

    const compute = (el: HTMLElement) => {
      const available = el.clientHeight - headerHeight - footerHeight
      // 预留 1 行余量，避免最后一行被小数像素裁掉半行。
      const rows = Math.floor(available / rowHeight) - 1
      setSize(Math.max(min, Math.min(max, rows)))
    }

    const attach = () => {
      if (disposed) return
      const el = containerRef.current
      if (!el) {
        // 容器还没挂载：下一帧再试。
        raf = requestAnimationFrame(attach)
        return
      }
      compute(el)
      observer = new ResizeObserver(() => compute(el))
      observer.observe(el)
    }

    attach()
    return () => {
      disposed = true
      if (raf) cancelAnimationFrame(raf)
      observer?.disconnect()
    }
  }, [containerRef, rowHeight, headerHeight, footerHeight, min, max])

  return size
}

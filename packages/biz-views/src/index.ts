/**
 * 业务视图包：跨应用可复用的**受控**复合组件。
 *
 * 与 `@jianmanager/ui` 的分工：
 * - `ui` 是设计系统原语（按钮 / 表格 / 面板 / 图表），既不含业务语义，也不取数；
 * - 本包是由这些原语拼装、且带自身交互状态的**复合视图**（如顶部加载进度条、
 *   文件浏览器、代码编辑器）。
 *
 * 本包的硬约束（受控化）：**不取数、不碰路由、不发请求、不弹 toast**。
 * 数据、路由身份与副作用一律由外壳（应用层）经 props 注入，回调经 props 回传。
 * 因此本包不得 import 应用的 `@/api`、`@/lib`、`@/components`，
 * 也不得依赖 `react-router` / `@tanstack/react-query` / `sonner`。
 *
 * 依赖方向：本包 → `@jianmanager/ui` → 通用库。反向不成立。
 */
export * from './components/TopLoadingBar'

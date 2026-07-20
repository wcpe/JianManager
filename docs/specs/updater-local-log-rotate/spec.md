# 功能规格：楔子与 updater-core 本地诊断日志轮换压缩

> 状态：草拟　·　关联 PRD：FR-353　·　分支：feature/fr-353-updater-log-rotate

## 1. 背景与目标

客户端更新器楔子（`wedge.log`）与 updater-core（`updater.log`）均以 **append 单文件** 写本地诊断日志，长期运行会膨胀成超大文件，排障困难并占盘。需在打开日志时按**大小或跨自然日**轮换，旧文件 **gzip** 压缩并限制保留份数；两边**各自**负责自己的文件。

增强 FR-090 本地诊断日志能力；保持 fail-open，不挡游戏启动。

## 2. 需求（要什么）

- **范围内**
  - 路径（不变）：
    - 楔子：`<gameDir>/.jm-updater/logs/wedge.log`
    - core：`<stateDir>/logs/updater.log`（既有 stateDir 即 `.jm-updater`）
  - 打开/创建日志文件时：
    - 若当前文件 **≥ 10 MiB**，或文件最后修改日 **早于本地自然日「今天」**（跨日），则轮换：
      1. 关闭当前写入句柄（若有）
      2. 将现有文件重命名为带时间戳的归档名（建议 `wedge.log.yyyyMMdd-HHmmss` / `updater.log.yyyyMMdd-HHmmss`）
      3. 对该归档做 **gzip**（产物 `….gz`，删除未压缩中间文件，或一步到位；实现任选，验收认 `.gz`）
      4. 新建空的当前日志文件继续 append
    - 归档保留**最近 5 个**同前缀 `.gz`（按修改时间或文件名时间戳排序），更旧删除
  - 阈值：**代码常量**（10 MiB、保留 5），不做运营配置 / 系统设置项
  - 职责：楔子模块只处理 `wedge.log*`；updater-core 只处理 `updater.log*`；可复制相似工具类，**不强制**共享 jar 依赖（YAGNI：两份小实现可接受）
  - 轮换或压缩任一步失败：**fail-open**（降级为仅控制台或继续尝试 append 新文件 / 原文件，**绝不抛到启动主路径阻断游戏**）
- **不做（范围外）**
  - 远程上传日志、管理台查看玩家日志
  - 运行中按时间定时轮换（仅在打开 Logger 时检查即可）
  - 运营可配阈值
  - core 清理 wedge 日志或反之

## 3. 设计（怎么做）

### 3.1 楔子（Java）

- 改 `WedgeLogger.create`：在 `new FileHandler(...)` 之前调用轮换例程。
- `java.util.logging.FileHandler` 仍可写当前 `wedge.log`；注意 FileHandler 的 limit 参数**不替代**本规格的跨日 + gzip 策略（本规格自管轮换，FileHandler 可用无限制 append 或与规格一致的单文件）。

### 3.2 updater-core（Java）

- 改 `Logger.create`：在 `Files.newOutputStream(APPEND)` 之前对 `updater.log` 做同样轮换。

### 3.3 轮换算法（两边一致语义）

```
shouldRotate(file):
  if !exists → false
  if size >= 10 * 1024 * 1024 → true
  if lastModified 的本地日期 < 今天 → true
  else false

rotate(file):
  archive = file + "." + yyyyMMdd-HHmmss
  rename file → archive
  gzip archive → archive.gz; delete archive
  prune: 同目录匹配 "<basename>.log.*.gz" 按时间降序保留 5
```

并发：单进程启动路径串行打开即可；不引入跨进程文件锁（玩家单实例惯例）。若 rename 失败则跳过轮换并 fail-open。

### 3.4 测试

- JUnit：临时目录构造超大文件 / 旧修改时间文件 / 超过 5 个归档 → 断言轮换、`.gz` 存在、数量上限、当前 log 可写。
- 不依赖真实 Minecraft。

### 3.5 ADR

- 不新开 ADR。

## 4. 任务拆分

- [ ] 楔子：轮换工具 + 接入 `WedgeLogger` + 单测
- [ ] updater-core：轮换工具 + 接入 `Logger` + 单测
- [ ]（可选）抽取同目录注释说明常量，避免魔法数无注释
- [ ] 文档同步：PRD FR-353、CHANGELOG 末尾追加；client-updater README 若有日志说明则补一句

## 5. 验收标准

1. **大小**：构造 ≥10MiB 的 `wedge.log` / `updater.log`，调用 create 后：存在新的当前 log（小或空）+ 至少一个 `*.gz` 归档。
2. **跨日**：构造昨日 mtime、小于 10MiB 的 log，create 后同样轮换。
3. **保留**：目录内 >5 个匹配归档时，create 后最多 5 个 `.gz`。
4. **fail-open**：对只读目录或故意失败路径，create 不抛未捕获异常阻断调用方（单测或审查）。
5. **自动化**：`client-updater` 下 wedge / updater-core 相关 Gradle test 绿。
6. **真机（需用户确认）**：真实启动一轮更新（或至少加载楔子/core），确认 `.jm-updater/logs/` 行为符合预期；可用手工塞大文件再启动验证轮换。

## 6. 风险 / 待定

- Windows 文件占用：轮换须在打开 FileHandler/OutputStream **之前** rename；若外部程序锁文件则 fail-open。
- gzip 与 JDK 版本：使用 `java.util.zip.GZIPOutputStream` 标准库即可。

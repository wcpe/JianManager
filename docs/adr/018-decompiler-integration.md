# ADR-018: 归档浏览与反编译集成——Worker 侧 archive/zip 列举 + CFR 受控反编译

- **日期**: 2026-06-23
- **状态**: accepted
- **关联 FR**: FR-075（归档浏览与反编译）
- **依赖**: [ADR-008](008-structured-launch-managed-jdk.md)（托管多 JDK，反编译复用实例绑定 JDK 或系统 JDK）、[ADR-010](010-portable-data-root.md)（CFR jar 缓存落数据根 `var/tools/`）、[ADR-002](002-grpc-over-rest.md)（列举/反编译经 gRPC 委托 Worker）

## 上下文

FR-070 资源管理器让运维能浏览/编辑实例工作目录的文本文件，但 Minecraft 运营场景里**最常见的不可直接阅读内容是 jar/zip 归档**（插件 jar、模组 jar、客户端 jar）：

- 想看某插件的 `plugin.yml`/`config.yml` 默认值、`META-INF`、内部资源，必须先解压；
- 排障时想看某个 class 的实际逻辑（无源码的第三方插件），需要反编译。

当前没有任何「打开归档看内部」「反编译 class」的能力。需要决定：

1. **谁来列归档条目、读内部文本**——是否需要 JDK；
2. **谁来反编译、用什么反编译器**——引擎选型、打包/分发、调用方式；
3. **安全边界**——反编译是「跑外部进程处理用户提供的字节」，必须只读、超时、限体积、受控 exec，绝不成为事故源（沿用监控探针「只读优先，绝不成为事故源」的基调）。

## 决策

### 1. 打开归档（列条目 / 读内部文本）：Worker 用 Go `archive/zip`，不依赖 JDK

jar 即 zip。Worker 用标准库 `archive/zip` 打开归档、列出条目树、按需流式读取单个内部条目内容。**不需要 JDK，不起任何子进程**，纯 Go 内存/流式处理：

- **列条目**：`ListArchiveEntries` 打开 `<workDir>/<archivePath>`，返回扁平条目列表（每条 `name`/`is_dir`/`size`/`compressed_size`/`modified`/`crc32`）。前端据「/」分隔自行重建子树（与现有目录树同形态）。条目数与单条目大小设上限（见安全边界）。
- **读内部文本**：`ReadArchiveEntry` 打开归档、定位某条目、流式读取其内容（截断到体积上限），交给只读编辑器展示。二进制条目（按扩展名/嗅探）标记为不可文本预览，前端给出提示而非乱码。
- **资源管理器树展开归档为子树**（FR-070 复用）：左栏目录树遇到 `.jar`/`.zip` 文件时可展开为「归档内子树」（懒加载，调 `ListArchiveEntries`），点内部文本条目→只读查看；不在工作目录里真解压（零落盘副作用）。

归档路径经既有 `validatePath` 防越界（与 FR-070 文件操作同一道闸），条目名经 zip-slip 校验（拒绝 `..` 逃逸与绝对路径条目，即便不落盘也统一拒绝异常条目）。

### 2. 反编译引擎：CFR 单 jar（MIT，零运行期依赖），经实例/系统 JDK 跑

- **选型 CFR**（`org.benf:cfr`）：单一可执行 jar（`java -jar cfr.jar <target>`），MIT 许可，零第三方依赖，对现代 Java 字节码（含 lambda/record/switch 表达式）支持良好，输出到 stdout 便于流式承接。相较 Fernflower（需 IDEA 运行期）/Procyon（多依赖），CFR 单 jar 最契合「Worker 受控调起、零额外依赖」的约束。
- **复用 JDK**：反编译只需一个 JRE 跑 CFR jar。优先用**实例绑定的 JDK**（`Instance.JDKBinPath`/`JDKPath`，ADR-008 托管多 JDK），其次用 Worker **系统 JDK**（`jdkMgr` 探测到的任一可用 JDK / `JAVA_HOME` / PATH 上的 `java`）。都没有则反编译返回明确「无可用 JDK」错误（降级），归档浏览（决策 1，不需 JDK）不受影响。

### 3. CFR jar 的打包/分发：内嵌（可选，构建期注入）+ 数据根按需 provision（带 sha256 校验）回退

CFR jar（~2 MB）的获取顺序，Worker 首次反编译时解析一次并缓存路径：

1. **显式配置路径**（`worker.yaml` `decompiler.cfr_path` 或环境变量 `JIANMANAGER_DECOMPILER_CFR_PATH`）——运维离线放置时直接指定，最高优先级；
2. **内嵌 jar**（`go:embed`，构建期由 `make embed-cfr` 注入 `internal/worker/embed/cfr/cfr.jar`，**gitignore 不入库**，避免 2 MB 二进制污染仓库）——内嵌存在则首次使用时写到数据根缓存目录复用；
3. **数据根缓存**（`var/tools/cfr-<version>.jar`，ADR-010 可移植数据根）——已 provision 过则直接用；
4. **按需下载**（从 Maven Central `repo1.maven.org/.../org/benf/cfr/<version>/cfr-<version>.jar`，**下载后校验 SHA-256 pin**，匹配才落地到数据根缓存，否则丢弃报错）——首次无内嵌时联网拉取，之后走第 3 步缓存（离线可用）。

版本号与 sha256 pin 为 Worker 侧构建期常量（升级 CFR 时同步改常量与 pin）。下载是「内容可信靠 sha256 pin、来源仅作分发」，与 ADR-022 客户端 manifest 信任模型同构（pin 校验内容，不信任传输通道）。

### 4. 反编译调用：只读 + 超时 + 体积上限 + 受控 exec

`DecompileClass` 接收实例 UUID + 归档内/工作目录内目标（`.class` 单文件，或 `.jar` 内某 class，或整个 `.jar`），Worker：

1. **解析目标字节**：工作目录内 `.class` → 直接定位文件；`.jar` 内某 entry → 从 zip 抽出该 class 到临时文件；整 jar → 直接把 jar 路径交给 CFR。所有路径经 `validatePath`/zip-slip 校验。
2. **受控 exec**：`exec.CommandContext(ctx, javaBin, "-jar", cfrJar, target, ...CFR 只读参数)`，CFR 仅做「读字节→输出源码到 stdout」，**不写工作目录、不联网、不执行目标代码**（CFR 是静态字节码分析，不加载/运行 class）。临时目录用后即删。
3. **超时**：`context.WithTimeout`（默认 30s，可配 `decompiler.timeout`）。超时 kill 子进程并返回降级错误。
4. **体积上限**：
   - 输入：目标 class/jar 字节数上限（默认 16 MiB，jar 反编译上限更保守）；超限拒绝（避免巨 jar 拖垮节点）。
   - 输出：捕获 stdout 到上限（默认 4 MiB 源码）即截断，标记 `truncated=true`。
5. **失败降级**：CFR 退出非 0、超时、无 JDK、无 CFR jar、输入超限——一律返回结构化错误（`error` 文案 + 不抛 panic），前端只读视图显示「反编译失败/降级」而非崩溃。

### 5. gRPC 面（加性新增，protoc 重新生成，严禁 sed）

`proto/worker.proto` 加性新增（不动既有 message/RPC 编号）：

- `rpc ListArchiveEntries(ListArchiveEntriesRequest) returns (ListArchiveEntriesResponse)`——列归档条目；
- `rpc ReadArchiveEntry(ReadArchiveEntryRequest) returns (ReadArchiveEntryResponse)`——读归档内某条目内容；
- `rpc DecompileClass(DecompileClassRequest) returns (DecompileClassResponse)`——反编译 class/jar 出源码。

经 `make proto` 重新生成 `proto/workerpb`（**严禁 sed 改 `worker.pb.go`**，见 commit c1cb5af 教训）。CP 加 `FileService` 方法转发 + `FileHandler` 端点（挂在既有 `/instances/:id/files` 组下，加性追加路由），权限复用文件「查看」级（`instance:file` 读，反编译属只读浏览）。

### 6. 前端（复用 FR-070 资源管理器）

- 目录树（`FileTree`）支持把 `.jar`/`.zip` 展开为归档子树（懒加载条目）。
- 点归档内文本条目 → 只读 `CodeEditor`（`readOnly`）展示内部文本。
- 归档内 `.class` 或工作目录 `.class`/`.jar` → 「反编译」动作 → 只读 `CodeEditor`（Java 高亮）展示 CFR 源码，含超时/降级/截断态提示。

## 后果

### 正面

- 归档浏览零依赖、零落盘、纯 Go，立即可用且安全（不起进程）。
- 反编译复用已有托管/系统 JDK，CFR 单 jar 零额外运行期依赖；分发兼顾「内嵌（离线即用）」与「按需下载（仓库不带二进制）」，且下载有 sha256 pin 保内容可信。
- 全程只读 + 超时 + 限体积 + 受控 exec（不运行目标代码、不写工作目录、不联网执行），绝不成为节点事故源。
- gRPC/端点/前端全部加性追加，复用 FR-070 资源管理器与只读编辑器，不重排既有结构。

### 负面 / 权衡

- 反编译质量取决于 CFR；混淆/极新字节码可能反编译不完美——可接受（运维用途为「读个大概逻辑」，非精确还原）。
- 内嵌 jar 不入库（gitignore），fresh clone 的 Worker 首次反编译需联网下载 CFR（或运维预置）；离线且未预置时反编译降级——以 sha256 pin + 数据根缓存 + 配置路径覆盖三重手段缓解。
- CFR 输出体积大的 jar 会被截断——以输出上限保护节点内存，前端提示截断。

## 备选方案（未采纳）

- **自写字节码解析展示**：工作量大且无反编译价值，放弃。
- **Fernflower / Procyon**：Fernflower 需 IDEA 运行期、Procyon 多依赖，均不如 CFR 单 jar 契合「零额外依赖、受控调起」约束。
- **把 jar 真解压到工作目录再浏览**：有落盘副作用、污染工作目录、需清理，放弃；改为内存/流式 `archive/zip`。
- **反编译放 CP 侧**：违反架构不变量（CP 不直接操作节点文件/进程，须经 gRPC 委托 Worker），放弃。

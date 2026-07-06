# FR-114 断网首启验收 Runbook

## 目标

验证 Worker 在部署 ServerProbe 前已把 TabooLib 运行期依赖预置到实例 `libraries/`，目标机器断开公网后，Paper 实例首次启动仍能 enable ServerProbe，并能经插件桥上报指标。

## 前置条件

- Control Plane 与 Worker 已按当前分支构建并运行。
- 测试节点可创建 Paper 实例，且可在 OS 或网络层临时阻断公网访问。
- ServerProbe jar 使用包含 `META-INF/taboolib/env.properties` 与 `version.properties` 的正式构建产物。
- 已获得平台管理员账号，用于创建实例、部署探针和查看监控指标。

## 步骤

1. 创建一个全新的 Paper 实例，不要复用已有 `libraries/` 缓存。
2. 在实例未启动前触发探针部署，确认 Worker 返回成功。
3. 检查实例目录下存在 `libraries/org/jetbrains/kotlin/` 与 `libraries/io/izzel/taboolib/` 相关 jar/pom 文件。
4. 阻断测试节点到公网 Maven/GitHub 源的访问，但保留 Worker 与 Control Plane 的内网连接。
5. 首次启动实例，等待 ServerProbe enable 完成。
6. 在监控页或 API 中确认该实例能上报探针指标。
7. 恢复网络，清理测试实例。

## 通过标准

- 首次启动期间日志无 TabooLib/Kotlin 依赖下载失败。
- ServerProbe enable 成功，插件桥保持连接。
- Control Plane 能收到该实例的基础指标。
- 若 `file-libs` 元数据为绝对路径或包含 `..`，Worker 必须拒绝部署并返回明确错误。

## 失败定位

- 缺少 `libraries/` 文件：检查 ServerProbe jar 元数据是否缺失或 Worker 下载 Maven 依赖失败。
- enable 失败但缓存存在：检查 TabooLib 模块是否新增传递依赖，必要时补充预置坐标。
- 指标不上报：先确认插件桥连接与 Worker/CP 内网连通性，不把网络控制面问题归因到依赖缓存。

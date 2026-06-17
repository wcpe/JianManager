# BUG-005: 启动命令多余引号修复

## 问题

实例配置的启动命令被附带多余引号，导致 Windows 执行时报错：
`'"C:\Users\Admin\.jdks\jbr-17.0.12\bin\java.exe\"' 不是内部或外部命令`

## 根因分析

代码链路（前端→API→gRPC→Worker→exec.Command）中没有任何环节添加引号。
引号来自用户输入：用户从其他来源复制命令时带入了外层引号包裹。

但 Worker 端 `exec.Command("cmd.exe", "/c", startCommand)` 对含引号的命令处理不当，
cmd.exe 的 `/c` 模式会将引号作为可执行文件名的一部分。

## 修复方案

### 1. 后端 sanitize（Control Plane 层）

- `service/instance.go`: `sanitizeStartCommand()` 去除外层引号包裹
- 仅当整个命令被同种引号完整包裹且内部无同类引号时才去除
- 在 `Create()` 和 `Update()` 两个入口调用

### 2. Worker 端 cmd.exe 引号处理

- `process/direct.go`: 改用 `cmd /s /c "startCommand"`
- `daemon/wrapper.go`: 改用 `cmd /s /c "startCommand"`
- `/s` 让 cmd.exe 先剥掉外层引号再解析，正确处理含空格路径

### 3. 前端提示

- `CreateInstanceDialog.tsx`: 启动命令输入框下方添加提示文字

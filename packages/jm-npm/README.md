# @jianmanager/jm

JianManager 命令行客户端 `jm`：经 Control Plane HTTPS + 用户令牌（`jmut_`）管理节点与实例。

## 安装

```bash
npm install -g @jianmanager/jm
```

安装后即可使用 `jm`。主包本身不含二进制，会按当前平台自动选用对应的
`@jianmanager/jm-<platform>` 平台包。

支持的平台：

| 平台 | 包名 |
|---|---|
| Linux x64 | `@jianmanager/jm-linux-x64` |
| Linux arm64 | `@jianmanager/jm-linux-arm64` |
| macOS x64 | `@jianmanager/jm-darwin-x64` |
| macOS arm64（Apple Silicon） | `@jianmanager/jm-darwin-arm64` |
| Windows x64 | `@jianmanager/jm-win32-x64` |

不使用 npm 时，可从 [GitHub Releases](https://github.com/wcpe/JianManager/releases) 直接下载对应平台的二进制。

## 用法

```bash
jm config set-url https://cp.example.com   # 配置 Control Plane 地址
jm auth login                              # 设备授权登录（浏览器确认）
jm auth status                             # 查看登录状态

jm api GET /api/v1/instances               # 直接调用任意 CP 接口
jm list instances                          # 高频命令糖
jm instance status 42

jm skill install --host claude             # 安装 Agent 运维知识到宿主
```

令牌保存在系统钥匙串（Windows Credential Manager / macOS Keychain /
Linux Secret Service），不落盘明文。`jm auth logout` 会同时清除本地凭证并吊销远端令牌。

完整说明见 [docs/specs/jm-agent-cli/spec.md](https://github.com/wcpe/JianManager/blob/main/docs/specs/jm-agent-cli/spec.md)。

## 环境变量

| 变量 | 说明 |
|---|---|
| `JM_URL` | Control Plane 基址（优先级低于 `--url`，高于配置文件） |
| `JM_TOKEN` | 令牌（优先级低于 `--token`，高于钥匙串；供 CI 使用） |

## 许可

见仓库根 [LICENSE](https://github.com/wcpe/JianManager/blob/main/LICENSE)。

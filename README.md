# JianManager

游戏服务器多节点管理平台。Go + 内嵌 React 架构，单二进制部署。

## 功能

- **实例管理**: 创建/启动/停止/重启游戏服务器实例，状态机驱动
- **节点管理**: Worker Node 自动注册、心跳上报、离线检测
- **终端**: 浏览器直连 Worker Node WebSocket，xterm.js 实时终端
- **文件管理**: 在线浏览/编辑实例工作目录文件
- **Bot 平台**: Mineflayer Bot 管理，支持 follow/guard/patrol/idle 行为
- **监控**: CPU/内存/磁盘指标采集，Recharts 仪表盘
- **告警**: 阈值触发告警，Webhook 通知
- **定时任务**: Cron 调度器，支持启停/命令/备份
- **备份恢复**: 手动创建备份，通过 gRPC 委托 Worker 执行
- **模板**: 预设服务端模板，一键创建实例
- **审计日志**: 关键操作自动记录
- **i18n**: 中英文国际化

## 快速开始

### 环境要求

- Go 1.22+
- Node.js 20+
- npm

### 安装依赖

```bash
make install
```

### 开发模式

**启动 Control Plane (后端 + 前端嵌入)**:
```bash
make dev-cp
# 访问 http://localhost:8080
```

**启动前端开发服务器 (热重载)**:
```bash
make dev-web
# 访问 http://localhost:5173 (自动代理 API 到 8080)
```

**启动 Worker Node**:
```bash
# 新终端
JIANMANAGER_CONTROL_PLANE_GRPC=localhost:9100 go run ./cmd/worker
```

### 生产构建

```bash
# 构建所有（前端 + 嵌入 + Go 二进制）
make build

# 产物
ls bin/
# control-plane.exe  # Control Plane（含前端）
# worker.exe          # Worker Node
```

### Docker 部署

```bash
# 构建镜像
make docker

# 启动所有服务
make docker-up

# 查看日志
docker compose logs -f

# 停止
make docker-down
```

## 架构

```
浏览器 (React SPA)
    │ HTTP REST /api/v1/*
    ▼
Control Plane (Go)
    │ gRPC
    ▼
Worker Node (Go) × N
    ├── 进程管理 (direct/daemon/docker)
    ├── WebSocket 终端服务
    ├── RCON 指标采集
    └── Bot 管理 → Node.js 子进程 (Mineflayer)
```

### 三进程模型

| 进程 | 语言 | 部署 | 职责 |
|---|---|---|---|
| Control Plane | Go | 1 个实例 | API、认证、调度、gRPC 客户端池、前端静态文件 |
| Worker Node | Go | N 个实例 | gRPC 服务端、进程管理、WS 终端、指标采集 |
| Bot Worker | Node.js | 按需 spawn | Mineflayer 连接、行为引擎 |

### 端口

| 端口 | 服务 | 说明 |
|---|---|---|
| 8080 | HTTP | Control Plane API + 前端 |
| 9100 | gRPC | Control Plane ↔ Worker Node |
| 9101 | gRPC | Worker Node gRPC |
| 9102 | WebSocket | Worker Node 终端服务 |

## 配置

### Control Plane

配置文件: `configs/control-plane.yaml`

```yaml
server:
  host: 0.0.0.0
  port: 8080
  dev_mode: false

grpc:
  port: 9100

database:
  driver: sqlite
  dsn: data/jianmanager.db

jwt:
  secret: your-secret-here
  access_ttl: 15m
  refresh_ttl: 168h

log:
  level: info
  format: json
```

### Worker Node

环境变量:

| 变量 | 说明 | 默认值 |
|---|---|---|
| `JIANMANAGER_NODE_NAME` | 节点名称 | node-01 |
| `JIANMANAGER_CONTROL_PLANE_GRPC` | Control Plane gRPC 地址 | localhost:9100 |
| `JIANMANAGER_GRPC_PORT` | Worker gRPC 端口 | 9101 |
| `JIANMANAGER_WS_PORT` | Worker WebSocket 端口 | 9102 |
| `JIANMANAGER_WORK_DIR` | 实例工作目录 | ./servers |
| `JIANMANAGER_JWT_SECRET` | JWT 密钥 | dev-secret-change-me |

### 环境变量覆盖

所有配置项可通过 `JIANMANAGER_` 前缀环境变量覆盖:

```bash
JIANMANAGER_SERVER_PORT=8080
JIANMANAGER_DB_DRIVER=sqlite
JIANMANAGER_DB_DSN=/app/data/jianmanager.db
JIANMANAGER_JWT_SECRET=your-secret
```

## API

### 认证

```bash
# 登录
curl -X POST http://localhost:8080/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"admin123"}'

# 返回: {"accessToken":"...","refreshToken":"...","expiresIn":900}
```

### 节点

```bash
# 列出节点
curl http://localhost:8080/api/v1/nodes \
  -H "Authorization: Bearer <token>"
```

### 实例

```bash
# 创建实例
curl -X POST http://localhost:8080/api/v1/instances \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -d '{
    "nodeId": 1,
    "name": "Survival Server",
    "type": "minecraft_java",
    "processType": "daemon",
    "startCommand": "java -Xmx2G -jar paper.jar nogui"
  }'

# 启动实例
curl -X POST http://localhost:8080/api/v1/instances/1/start \
  -H "Authorization: Bearer <token>"
```

## 开发

### 测试

```bash
make test           # 运行测试
make test-cover     # 测试覆盖率
make vet            # 静态分析
make lint           # golangci-lint
make lint-web       # 前端类型检查
make lint-bot       # Bot Worker 类型检查
```

### 目录结构

```
cmd/
  control-plane/    # Control Plane 入口
  worker/           # Worker Node 入口
internal/
  controlplane/     # Control Plane 内部包
    config/         # 配置加载
    database/       # 数据库初始化
    middleware/     # 认证、限流中间件
    model/          # 数据模型
    router/         # API 路由
    service/        # 业务逻辑
    grpc/           # gRPC 客户端池
    embed/          # 前端嵌入
  worker/           # Worker Node 内部包
    process/        # 进程管理器
    daemon/         # 守护进程协议
    grpc/           # gRPC 服务端
    ws/             # WebSocket 终端
    metrics/        # 指标采集 + RCON
    register/       # 节点注册
    heartbeat/      # 心跳上报
proto/
  workerpb/         # gRPC 桩代码
web/                # React 前端
bot-worker/         # Node.js Bot Worker
configs/            # 配置文件样例
docs/               # 文档
```

## 文档

- [DEPLOY.md](docs/DEPLOY.md) — 部署指南（发布产物运维）
- [PRD.md](docs/PRD.md) — 产品需求
- [ARCHITECTURE.md](docs/ARCHITECTURE.md) — 系统架构
- [API.md](docs/API.md) — API 参考
- [CONVENTIONS.md](docs/CONVENTIONS.md) — 编码规范

## 许可证

MIT

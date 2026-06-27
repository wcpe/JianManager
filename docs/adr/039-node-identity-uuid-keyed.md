# ADR-039: 节点身份由 name 锚定改为 UUID 锚定（修复重名覆盖）

- **日期**: 2026-06-27
- **状态**: accepted（取代 ADR-020 §1「name 命中即重注册、不强制 token」的注册分叉立场）
- **上下文**: `ControlPlaneHandler.Register`（`internal/controlplane/grpc/handler.go`）当前**按节点名匹配**已有节点：`h.db.Where("name = ?", req.Name).First(&node)`。命中即走「已有节点」分支，**无条件**用本次请求覆写该行的 `host`/`grpc_port`/`ws_port` 等并重建反向 gRPC 连接。后果：当**另一台全新机器**用一个已存在的节点名注册时，CP 把旧节点行的连接信息改写到新机器、并把**旧节点的 `node_uuid`/`node_secret` 回传给新机器**，于是两台机器共享同一身份、反向连接池指向最后注册者。旧节点上按 `node_id` 外键挂着的 JDK / 实例 / 制品全部被错误路由到新机器——表现为 **JDK 卸不掉、实例起不来、制品删不掉**。更糟的是「name 命中」分支**绕过了 enrollment token 门槛**（ADR-020 §1 自己记下的「对伪造已存在节点名重注册仍开放」遗留风险）。根因是**注册以「名字」这一可重复、无需证明的字段作身份**，而名字相同 ≠ 同一台机器。

## 决策

把节点身份从「name 锚定」改为「**UUID（+ node_secret）锚定**」。注册时**先按持有的身份凭据匹配，再退到 token 准入**，名字降为可变标签且加唯一性约束。

### 1. 注册匹配优先级（Register handler 重写 + Worker 注册改造）

> **当前现实**（`internal/worker/register/register.go`）：Worker 重注册时 `RegisterRequest` **只带 name + 系统信息、不出示任何身份凭据**（仅新节点首注册经 metadata 带 `enroll-token`）；CP 全靠 `Where("name = ?", name)` 命中。Worker 虽已持久化身份（`register/identity.go`，ADR-020 §3）、心跳也已经 metadata 带 `node-secret`，但**注册这一步从不出示身份**——这正是覆写的根。修复需 CP 与 Worker **两侧配合**。

- **Worker 侧改造**：重注册时从本地身份文件读 `node_uuid`/`node_secret`，经 gRPC metadata（header `node-uuid`/`node-secret`，与心跳/`enroll-token` 同手法、**不改 proto**）出示身份。
- **CP 侧 Register 匹配优先级**：
  1. **携带 `node-uuid` 命中库中节点 + `node-secret` 匹配** → **按 UUID 重注册**：更新 host/port/os/arch、名字按上报值更新（允许改名，受 §3 唯一约束）。secret 不符 → `PermissionDenied`。
  2. **过渡兼容（未升级的旧 Worker，只带 name）**：name 命中既有节点**且本次连接 host 与库存 host 一致**（同机重启信号）→ 放行重注册并告警「建议升级 Worker」。host 不一致（正是"另一台机器冒用同名"）→ 不放行，落到 3。
  3. **无 UUID 证明、也非「同机 host 命中」** → 视为**新节点首注册**：必须带有效 enrollment token（ADR-020 准入不变），建**全新 UUID** 节点行；若上报名与既有节点**撞名** → **直接拒绝**（明确报错「节点名已被占用，请改名」），**绝不覆写**。

> 关键：覆写之所以可能，是因为旧路径「name 命中」**不要求任何身份证明**。新机器既无 `node-uuid`、host 也与库存不符（在异机），于是过不了 1/2、被挤进 3 的 token + 撞名拒绝——既堵死覆写，又不误伤真实重启（同机 host 命中走 2，升级后走 1）。过渡兼容（2）在全网 Worker 都带 uuid/secret 后可移除（届时纯 UUID 锚定）。
>
> 已知边界：未升级的旧节点若**同时改了 IP 又重启**（host 不符、又无 uuid），会落到 3 并因撞名被拒——需升级 Worker（走 1）或重新 enroll。属罕见，且远优于「静默覆写」。

### 2. 坏节点检测与修复

为已被覆盖污染的存量数据提供**检测 + 修复**：

- **检测**：扫描节点表，标出「身份疑似被串改」的行（如 last_heartbeat 来源 host 与登记 host 不符、或同一 UUID 短时间内 host 抖动）。提供只读诊断（CP API + 节点页入口）。
- **修复**：允许运营者将「被挤占的」机器**作为新节点重新 enroll**（生成新 UUID/secret），并**重挂/清理**其孤立的 JDK/实例/制品引用（迁移到正确节点或标记失效）。修复为破坏性操作，走二次确认 + 审计（FR-059/FR-015）。
- 修复能力随 FR-177（节点页重做）落地为可视化入口；后端修复逻辑随本 ADR 的 fix 提交。

### 3. 名字唯一性

`model.Node.Name` 加唯一性保障（唯一索引或 handler 层校验），杜绝两节点同名造成的歧义与覆写路径。改名（§1.1）同样受唯一约束校验。

## 理由

- **以「能证明的身份」而非「可重复的标签」锚定**：UUID/secret 是机器持有的私密凭据，名字是人给的可重复标签。用前者匹配，撞名不再等于撞身份。
- **最小破网**：真实重启（持 UUID/secret）走重注册；ADR-020 之前的 legacy 节点（持 secret 无 UUID）仍可重注册；只有「无任何凭据的陌生机器」被推入 token 路径并在撞名时被拒——正是要堵的那条路径。
- **不改 proto**：UUID 经 metadata 传递，沿用 ADR-020 既有手法，零 proto 变更、与 token/secret 同构。
- **修复存量**：仅堵未来不够——已污染的数据需可检测、可恢复，否则受害用户仍卡死。

## 后果

- `Register` handler 重写为「UUID 证明 → 同机 host 兼容 → token 新建」匹配；需测试覆盖：UUID 命中 / 同机 host 命中放行 / 异机撞名拒绝 / token 新建 / secret 不符拒绝。
- `model.Node.Name` 加唯一约束 → 需 migration；存量若已有重名行，migration 前需先跑修复/去重。
- Worker 注册逻辑（`internal/worker/register`）改造：当前 `Config`/`RegisterRequest` 不含身份，需在重注册时从 `identity.go` 读 `node_uuid`/`node_secret` 经 metadata 出示（心跳已有 secret-over-metadata 可复用）。
- 新增坏节点检测/修复的 CP service + API + 审计；UI 入口随 FR-177。
- ADR-020 §1「name 命中重注册不强制 token」立场被本 ADR 取代：重注册改由 UUID/secret 证明，撞名新机器被拒。

## 替代方案

- **保持 name 匹配、仅给 name 加唯一约束**：能挡「建两个同名节点」，但挡不住「同名机器重注册覆写」——唯一约束允许 UPDATE 同名行，覆写照旧；放弃（必须以身份凭据匹配）。
- **给 `RegisterRequest` 加必填 `node_uuid` 字段、强制所有注册带 UUID**：破坏 ADR-020 之前的 legacy 节点（无身份文件）重启；放弃（metadata 可选携带 + secret 兜底兼容）。
- **撞名自动加后缀（node-01-2）静默建新节点**：避免拒绝的"摩擦"，但会悄悄制造重名近似节点、掩盖运营者的命名冲突意图；放弃（显式拒绝 + 提示改名，让运营者知情）。

## 关系

- **ADR-002（gRPC 节点通信）**：身份匹配仍在唯一的 gRPC `Register` 内，未新增 RPC 协议。
- **ADR-020（节点 enrollment 与部署）**：本 ADR 取代其 §1 的「name 命中重注册」分叉立场；§2 安装脚本、§3 身份文件持久化、enrollment token 准入机制均继续有效，本 ADR 正是让 §3 的 UUID 身份真正成为注册匹配键。
- **FR-004（节点注册与心跳）**：本 ADR 修正其 `Register` 的身份匹配缺陷；心跳 `node_secret` 鉴权不变。
- **FR-177（节点管理页重做）**：承载坏节点检测/修复的可视化入口。

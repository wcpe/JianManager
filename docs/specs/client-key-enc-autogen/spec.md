# 功能规格：拉取密钥加密器自动生成与持久化

> 状态：待审　·　关联 PRD：FR-263　·　修订 ADR-044

## 1. 背景与目标

ADR-044 规定拉取密钥可逆加密存储（AES-256-GCM），加密密钥经 `JIANMANAGER_CLIENT_KEY_ENC_SECRET` 环境变量注入。未配时优雅降级——密钥无 KeyEnc、不可查看明文（revealable=false）。真机暴露：用户没配环境变量，密钥查看按钮灰色点不了，无法复制密钥到 jm-updater.json。

FR-248 已为签名密钥做了"自动生成 + 持久化"的先例。本 FR 对加密器做同样处理——启动时自动生成密钥并持久化，零配置即可用。

**目标**：CP 启动时未注入 env 加密密钥 → 自动生成 AES-256-GCM 密钥并持久化到数据根文件 → 跨重启用同一密钥 → 密钥始终可查看可复制。env 注入仍优先（双轨）。

## 2. 需求

### 范围内
- `ResolveKeyEncryptor` 改为三轨优先级：env 注入 > 自动生成持久化 > dev 回退
- 自动生成：32 字节 AES-256-GCM 密钥（`crypto/rand`），base64 编码
- 持久化：`<dataRoot>/etc/client-key-enc.key`（0600，先写临时文件再原子 rename）
- 跨重启稳定：启动时先读已有文件，存在则用、不存在才生成
- dev_mode：保持内置开发密钥回退（不生成文件）
- 生产未配 env 且自动生成成功 → 加密器可用 → 密钥可查看
- 修订 ADR-044：存储策略从"env 注入"改为"env 注入 > 自动生成 > dev 回退"

### 不做
- 加密密钥轮换（YAGNI，后续如需再加）
- 前端展示加密密钥来源（非必要，密钥能查看即可）
- 加密密钥备份/导出

## 3. 设计

### 3.1 服务端（CP）
- `service.ResolveKeyEncryptor(envSecret, devMode, dataRoot)`：
  - envSecret 非空 → 用 env（优先）
  - devMode=true 且 envSecret 空 → 内置开发密钥
  - devMode=false 且 envSecret 空 → 读 `<dataRoot>/etc/client-key-enc.key`：
    - 文件存在 → 读取并解析
    - 文件不存在 → 生成 32 字节随机密钥 → base64 → 写文件（0600，原子 rename）→ 返回加密器
  - 任何步骤失败 → 返回 nil（降级，不崩，密钥不可查看但其余功能正常）

### 3.2 main.go 改动
- `ResolveKeyEncryptor` 加 `dataRoot` 参数
- 去掉"未配则 warn 不可查看"的日志，改为"已自动生成加密密钥"的 info

### 3.3 ADR-044 修订
- 存储策略段加"自动生成持久化"为默认路径
- env 注入仍优先但非必须

## 4. 任务拆分
- [ ] service：ResolveKeyEncryptor 改三轨 + 自动生成 + 持久化
- [ ] main.go：传 dataRoot
- [ ] 测试：自动生成 → 密钥可查看 / 重启同密钥 / env 优先 / dev 回退
- [ ] ADR-044 修订 + doc-sync

## 5. 验收标准
- 全新数据根启动 → 自动生成加密密钥 → 新建密钥 revealable=true → 查看弹窗 + 复制成功
- 重启后用同一密钥 → 旧密钥仍可查看
- env 注入优先于自动生成
- dev 模式回退内置开发密钥
- `go build`/`test` 绿
- **【需真机】** 全新启动 → 建密钥 → 查看明文 → 复制到剪贴板

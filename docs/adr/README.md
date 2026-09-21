# 架构决策记录（ADR）

记录本项目的重大架构决策：背景、决策、理由、后果与被否的备选。每条决策一个文件，便于后来者理解「为什么是这样」。文件名 `NNN-标题.md`，编号 = 目录内现有最大编号 + 1，**永不复用、不补洞**。

## 生命周期（不可变 + 取代）

- ADR 一旦「accepted」即**不可变**：不编辑旧 ADR 的决策正文——它是决策史，只增不删。
- 决策变了 → 写一条**新 ADR 取代旧的**：新 ADR 背景里写「取代 ADR-NNN」；旧 ADR 只把状态行改为「superseded-by ADR-MMM」并加链接，正文一字不动；同步修改受影响的 `ARCHITECTURE.md` 与 `.claude/rules/`（尤其 `architecture-invariants.md`、`decision-alignment.md` 的关键决策表）。
- **何时写**：引入新技术、采用或推翻一个架构模式、有长期影响且有争议的取舍。日常变更归 PRD 状态列 + CHANGELOG，**不写 ADR**——增长过快是滥写信号。

## 导航（别慌通读）

理解系统现状看 [`../ARCHITECTURE.md`](../ARCHITECTURE.md)（永远是当前真貌的综合），ADR 只在追问「当初为什么」时按需翻；**当前架构 = 未被取代的活跃集**，被取代的归档不打扰。当前生效的关键决策速查表见 [`.claude/rules/decision-alignment.md`](../../.claude/rules/decision-alignment.md)。

当前 Bot Worker 分发与节点依赖根决策见 [ADR-072](072-managed-node-package-root.md)；它完整取代 ADR-070，同时保留 CP 内嵌、自愈物化与 ESM 链接模型。

## 索引（最近新增）

- [ADR-093](093-process-lifecycle-resilience.md) 进程生命周期韧性与孤儿治理（FR-455/456/459：防误杀、孤儿周期兜底、状态真源收敛、重推多源化）
- [ADR-092](092-config-surface-and-direct-probe.md) 实例配置源明面化与 MC 直探能力（FR-446/447/451：SLP+Query 直探、配置项内联/文件引用二态）
- [ADR-091](091-instance-capability-profile.md) 实例能力画像与多形态详情界面（FR-445/448/449/450/452/453：(type,role) 声明式画像驱动 Tab 显隐）
- [ADR-090](090-binary-provision-and-beacon-sync.md) 通用二进制搭建与 Beacon 可选协同（FR-441~444）
- [ADR-089](089-configurable-permission-tree-and-six-domain-nav.md) 可配置权限树与六域导航 IA（FR-431/432）
- [ADR-088](088-client-dist-ia-merge-ops-page.md) 客户端分发信息架构合并（FR-430）
- [ADR-087](087-console-immersive-visible-controls.md) 沉浸控制台采用可见控制优先的工作台（取代未入库的 tmux 前缀键提案）
- [ADR-085](085-local-serverprobe-upload-source.md) ServerProbe 本地上传来源（FR-411，修订 ADR-083 来源范围）
- [ADR-078](078-explorer-cross-window-clipboard-dnd.md) 资源管理器跨窗剪贴板与拖放总线（FR-377）
- [ADR-079](079-instance-reverse-reconcile.md) CP↔Worker 实例反向对账（FR-326：无主运行时宽限+默认不自动杀）
- [ADR-075](075-bot-command-orchestration.md) Bot 命令编排与动作成功边界（仅取代 ADR-074 的业务成功真源部分）
- [ADR-074](074-bot-distributed-load.md) Bot 目标实例与执行节点解耦，采用 Control Plane 分布式调度（FR-351～357，部分修订 ADR-006）
- [ADR-073](073-artifact-external-object-storage.md) 制品库外置对象存储与 302 预签名分发（FR-347，修订 ADR-011 存储节）

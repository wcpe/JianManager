package top.jm.updater.wedge;

import java.lang.instrument.Instrumentation;

/**
 * JM 客户端 OTA 楔子（javaagent）。
 *
 * <p>经启动器 JVM 参数 {@code -javaagent:wedge.jar=<gameDir>} 注入；premain 自定位、
 * 动态加载 updater-core 并调用其 {@code run} 入口、同步等待，更新失败 fail-static、
 * 楔子自身任何异常 fail-open——绝不挡住游戏启动（ADR-021 决策 6）。
 *
 * <p>协议见 {@code docs/specs/client-distribution/contract.md} §6。
 */
public final class Wedge {

    private Wedge() {
    }

    /**
     * javaagent 入口。{@code agentArgs} = gameDir（契约 §6.1）。全程 fail-open。
     */
    public static void premain(String agentArgs, Instrumentation inst) {
        try {
            // TODO(FR-089): getCodeSource 自定位 → 读同目录 jm-updater.json（channel/key/endpoint/timeoutSec）
            // TODO(FR-089): 解析 gameDir（agentArgs 优先，兜底 sun.java.command 的 --gameDir）
            // TODO(FR-089): URLClassLoader 内存加载 updater-core.jar → 反射 Core.run(ctx) → 同步等待 + 超时
            // TODO(FR-089): run 返回非 0 或超时 → fail-static 放行 + 提示
            // TODO(FR-089): 与 authlib-injector 等其他 -javaagent 共存验证
            System.out.println("[jm-updater] wedge premain（骨架，待 FR-089 实现）gameDir=" + agentArgs);
        } catch (Throwable t) {
            // 楔子唯一允许的失败模式：放行游戏（ADR-021 决策 6）。
            System.err.println("[jm-updater] wedge fail-open: " + t);
        }
    }
}

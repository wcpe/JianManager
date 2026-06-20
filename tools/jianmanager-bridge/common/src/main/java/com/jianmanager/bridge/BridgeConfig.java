package com.jianmanager.bridge;

/**
 * 插件桥连接配置（由平台签发后写入插件配置文件）。
 *
 * <p>wsUrl 与 token 由 Control Plane 的 {@code POST /instances/:id/plugin-token} 接口返回，
 * instanceUuid 为该实例的 UUID。插件握手时拼成
 * {@code <wsUrl>?token=<token>&instance=<instanceUuid>} 连入 Worker。
 */
public final class BridgeConfig {
    private final String wsUrl;
    private final String token;
    private final String instanceUuid;
    private final boolean enabled;
    private final long reconnectDelayMillis;

    public BridgeConfig(String wsUrl, String token, String instanceUuid, boolean enabled, long reconnectDelayMillis) {
        this.wsUrl = wsUrl;
        this.token = token;
        this.instanceUuid = instanceUuid;
        this.enabled = enabled;
        this.reconnectDelayMillis = reconnectDelayMillis;
    }

    public String wsUrl() {
        return wsUrl;
    }

    public String token() {
        return token;
    }

    public String instanceUuid() {
        return instanceUuid;
    }

    public boolean enabled() {
        return enabled;
    }

    public long reconnectDelayMillis() {
        return reconnectDelayMillis;
    }

    /** 配置是否完整可用（启用且 wsUrl/token 非空）。 */
    public boolean isUsable() {
        return enabled
                && wsUrl != null && !wsUrl.isBlank()
                && token != null && !token.isBlank();
    }
}

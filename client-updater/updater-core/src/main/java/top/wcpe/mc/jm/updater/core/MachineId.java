package top.wcpe.mc.jm.updater.core;

import java.lang.management.ManagementFactory;
import java.lang.management.OperatingSystemMXBean;
import java.net.InetAddress;
import java.net.NetworkInterface;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.Paths;
import java.nio.file.StandardCopyOption;
import java.util.ArrayList;
import java.util.Collections;
import java.util.Enumeration;
import java.util.List;

/**
 * 客户端机器码身份（FR-092）：稳定、跨平台、<b>不可逆</b>的机器标识，随 manifest/制品/遥测携带（X-Machine-Id）。
 *
 * <p>多硬件/环境特征（NIC MAC / CPU / 内存 / 主机名 / os）组合 SHA-256（64 hex），不暴露原始硬件信息。
 * 首次计算后<b>持久化</b>于 {@code <userHome>/.jm-updater/machine-id}（per-machine 稳定、硬件部分变化容错）；
 * 后续直接读持久值。全程 best-effort、<b>绝不抛</b>——任一特征采集失败即跳过，极端兜底返回固定串的 hash。
 *
 * <p><b>客户端生成、不可信</b>：仅统计 + 辅助限流，不作信任/授权依据（ADR-023，限流以 IP 为主）。
 * 不引外部依赖、不 shell 外部命令（避免拖慢游戏启动 / 权限问题）。
 */
final class MachineId {

    private MachineId() {
    }

    /** 取稳定机器码（默认持久化于 userHome）。 */
    static String get() {
        return get(defaultPersistFile());
    }

    /** 取稳定机器码，持久化于指定文件（便于测试注入，避免污染真实 userHome）。 */
    static String get(Path persistFile) {
        String existing = readPersisted(persistFile);
        if (existing != null) {
            return existing;
        }
        String id = compute();
        writePersisted(persistFile, id); // 失败忽略（退化为每次按特征计算，仍尽量稳定）。
        return id;
    }

    /** 默认持久化路径：{@code <userHome>/.jm-updater/machine-id}。 */
    static Path defaultPersistFile() {
        return Paths.get(System.getProperty("user.home", "."), ".jm-updater", "machine-id");
    }

    /** 组合多硬件/环境特征 → SHA-256（纯函数，不读写持久化）。 */
    static String compute() {
        StringBuilder sb = new StringBuilder("jm-machine|v1");
        sb.append("|os=").append(System.getProperty("os.name", ""));
        sb.append("|arch=").append(System.getProperty("os.arch", ""));
        sb.append("|user=").append(System.getProperty("user.name", ""));
        sb.append("|cpu=").append(Runtime.getRuntime().availableProcessors());
        sb.append("|mem=").append(totalMemory());
        sb.append("|host=").append(hostname());
        sb.append("|mac=").append(macFingerprint());
        return Hashes.sha256(sb.toString().getBytes(StandardCharsets.UTF_8));
    }

    private static String readPersisted(Path f) {
        try {
            if (Files.isRegularFile(f)) {
                String s = new String(Files.readAllBytes(f), StandardCharsets.UTF_8).trim();
                if (isHex64(s)) {
                    return s;
                }
            }
        } catch (Throwable ignore) {
            // 读失败按未持久化处理。
        }
        return null;
    }

    private static void writePersisted(Path f, String id) {
        try {
            Files.createDirectories(f.getParent());
            Path tmp = f.resolveSibling("machine-id.tmp");
            Files.write(tmp, id.getBytes(StandardCharsets.UTF_8));
            try {
                Files.move(tmp, f, StandardCopyOption.REPLACE_EXISTING, StandardCopyOption.ATOMIC_MOVE);
            } catch (java.nio.file.AtomicMoveNotSupportedException e) {
                Files.move(tmp, f, StandardCopyOption.REPLACE_EXISTING);
            }
        } catch (Throwable ignore) {
            // 持久化失败不致命：本次返回计算值，下次再算（硬件稳定则结果一致）。
        }
    }

    private static long totalMemory() {
        try {
            OperatingSystemMXBean os = ManagementFactory.getOperatingSystemMXBean();
            if (os instanceof com.sun.management.OperatingSystemMXBean) {
                return ((com.sun.management.OperatingSystemMXBean) os).getTotalMemorySize();
            }
        } catch (Throwable ignore) {
            // 取不到内存特征即跳过。
        }
        return 0;
    }

    private static String hostname() {
        try {
            return InetAddress.getLocalHost().getHostName();
        } catch (Throwable t) {
            return "";
        }
    }

    /** 非回环/非虚拟网卡 MAC 的排序去重指纹（hex）；取不到则空。 */
    private static String macFingerprint() {
        List<String> out = new ArrayList<>();
        try {
            Enumeration<NetworkInterface> e = NetworkInterface.getNetworkInterfaces();
            while (e != null && e.hasMoreElements()) {
                NetworkInterface ni = e.nextElement();
                try {
                    if (ni.isLoopback() || ni.isVirtual()) {
                        continue;
                    }
                    byte[] mac = ni.getHardwareAddress();
                    if (mac == null || mac.length == 0) {
                        continue;
                    }
                    String hex = Hashes.hex(mac);
                    if (!out.contains(hex)) {
                        out.add(hex);
                    }
                } catch (Throwable ignore) {
                    // 单网卡读失败跳过。
                }
            }
        } catch (Throwable ignore) {
            // 枚举失败即空指纹。
        }
        Collections.sort(out);
        return String.join(",", out);
    }

    private static boolean isHex64(String s) {
        if (s == null || s.length() != 64) {
            return false;
        }
        for (int i = 0; i < 64; i++) {
            char c = s.charAt(i);
            if (!((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f'))) {
                return false;
            }
        }
        return true;
    }
}

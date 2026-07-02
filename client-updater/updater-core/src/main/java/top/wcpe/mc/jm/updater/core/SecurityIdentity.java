package top.wcpe.mc.jm.updater.core;

/** 启动安全画像身份与版本字段。 */
final class SecurityIdentity {

    final String channel;
    final String playerName;
    final String machineId;
    final String installId;
    final String coreVersion;
    final String wedgeVersion;
    final long manifestVersion;

    SecurityIdentity(String channel, String playerName, String machineId, String installId,
                     String coreVersion, String wedgeVersion, long manifestVersion) {
        this.channel = nullToEmpty(channel);
        this.playerName = nullToEmpty(playerName);
        this.machineId = nullToEmpty(machineId);
        this.installId = nullToEmpty(installId);
        this.coreVersion = nullToEmpty(coreVersion);
        this.wedgeVersion = nullToEmpty(wedgeVersion);
        this.manifestVersion = manifestVersion;
    }

    private static String nullToEmpty(String value) {
        return value == null ? "" : value;
    }
}

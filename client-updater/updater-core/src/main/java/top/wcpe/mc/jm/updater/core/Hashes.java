package top.wcpe.mc.jm.updater.core;

import java.io.IOException;
import java.io.InputStream;
import java.nio.file.Files;
import java.nio.file.Path;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;

/**
 * 文件/字节哈希工具（契约 §2/§3）。
 *
 * <p>sha256 = 信任校验（强）；md5 = 本地快筛（弱，绝不作信任依据，ADR-022 决策 3）。
 * 仅用 JDK {@link MessageDigest}。
 */
final class Hashes {

    private Hashes() {
    }

    /** 文件内容 sha256（小写 hex）。 */
    static String sha256(Path file) throws IOException {
        return digestFile(file, "SHA-256");
    }

    /** 文件内容 md5（小写 hex）。 */
    static String md5(Path file) throws IOException {
        return digestFile(file, "MD5");
    }

    /** 字节 sha256（小写 hex）。 */
    static String sha256(byte[] data) {
        try {
            return hex(MessageDigest.getInstance("SHA-256").digest(data));
        } catch (NoSuchAlgorithmException e) {
            throw new IllegalStateException(e);
        }
    }

    private static String digestFile(Path file, String algo) throws IOException {
        MessageDigest md;
        try {
            md = MessageDigest.getInstance(algo);
        } catch (NoSuchAlgorithmException e) {
            throw new IllegalStateException(e);
        }
        byte[] buf = new byte[64 * 1024];
        try (InputStream in = Files.newInputStream(file)) {
            int n;
            while ((n = in.read(buf)) != -1) {
                md.update(buf, 0, n);
            }
        }
        return hex(md.digest());
    }

    static String hex(byte[] bytes) {
        StringBuilder sb = new StringBuilder(bytes.length * 2);
        for (byte b : bytes) {
            sb.append(Character.forDigit((b >> 4) & 0xF, 16));
            sb.append(Character.forDigit(b & 0xF, 16));
        }
        return sb.toString();
    }
}

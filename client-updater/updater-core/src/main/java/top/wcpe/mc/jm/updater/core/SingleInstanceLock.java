package top.wcpe.mc.jm.updater.core;

import java.io.IOException;
import java.nio.channels.FileChannel;
import java.nio.channels.FileLock;
import java.nio.channels.OverlappingFileLockException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.StandardOpenOption;

/**
 * 单实例并发锁（契约 §6 / FR-090）：单 gameDir 仅一个 updater 运行，
 * 防同时开两个游戏 / 重复启动并发改目录。
 *
 * <p>基于 {@link FileLock} 排他锁文件 {@code <gameDir>/.jm-updater/updater.lock}。
 * 进程退出/崩溃由 OS 自动释放锁，无残留死锁。
 */
final class SingleInstanceLock implements AutoCloseable {

    private final FileChannel channel;
    private final FileLock lock;

    private SingleInstanceLock(FileChannel channel, FileLock lock) {
        this.channel = channel;
        this.lock = lock;
    }

    /**
     * 尝试获取锁。已被其他实例持有则返回 {@code null}（调用方据此退让，不并发改目录）。
     */
    static SingleInstanceLock tryAcquire(Path stateDir) throws IOException {
        Files.createDirectories(stateDir);
        Path lockFile = stateDir.resolve("updater.lock");
        FileChannel ch = FileChannel.open(lockFile,
                StandardOpenOption.CREATE, StandardOpenOption.WRITE);
        try {
            FileLock fl = ch.tryLock();
            if (fl == null) {
                ch.close();
                return null;
            }
            return new SingleInstanceLock(ch, fl);
        } catch (OverlappingFileLockException e) {
            ch.close();
            return null;
        } catch (IOException e) {
            ch.close();
            throw e;
        }
    }

    @Override
    public void close() {
        try {
            if (lock != null && lock.isValid()) {
                lock.release();
            }
        } catch (IOException ignore) {
            // 释放失败由进程退出兜底。
        }
        try {
            channel.close();
        } catch (IOException ignore) {
            // 同上。
        }
    }
}

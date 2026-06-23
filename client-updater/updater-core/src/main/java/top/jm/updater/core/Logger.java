package top.jm.updater.core;

import java.io.IOException;
import java.io.PrintStream;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.StandardOpenOption;
import java.time.LocalDateTime;
import java.time.format.DateTimeFormatter;

/**
 * 本地诊断日志（契约 FR-090「本地诊断日志，供排障与遥测 FR-094」）。
 *
 * <p>同时写 {@code <gameDir>/.jm-updater/updater.log} 与 stderr（楔子控制台可见）。
 * 凭据（key/token）绝不入日志。
 */
final class Logger implements AutoCloseable {

    private static final DateTimeFormatter TS = DateTimeFormatter.ofPattern("yyyy-MM-dd HH:mm:ss.SSS");

    private final PrintStream console;
    private PrintStream file;

    private Logger(PrintStream console, PrintStream file) {
        this.console = console;
        this.file = file;
    }

    /** 创建写入指定状态目录的日志器；文件不可写时降级为仅控制台。 */
    static Logger create(Path stateDir) {
        PrintStream file = null;
        try {
            Files.createDirectories(stateDir);
            Path logFile = stateDir.resolve("updater.log");
            file = new PrintStream(Files.newOutputStream(logFile,
                    StandardOpenOption.CREATE, StandardOpenOption.APPEND), true, "UTF-8");
        } catch (IOException | SecurityException e) {
            // 日志文件不可写不应阻断更新（契约 fail-open 精神）。
        }
        return new Logger(System.err, file);
    }

    /** 仅控制台输出（无可写状态目录时的兜底）。 */
    static Logger consoleOnly() {
        return new Logger(System.err, null);
    }

    void info(String msg) {
        write("INFO", msg);
    }

    void warn(String msg) {
        write("WARN", msg);
    }

    void error(String msg) {
        write("ERROR", msg);
    }

    void debug(String msg) {
        write("DEBUG", msg);
    }

    private void write(String level, String msg) {
        String line = LocalDateTime.now().format(TS) + " [" + level + "] [jm-updater-core] " + msg;
        console.println(line);
        if (file != null) {
            file.println(line);
        }
    }

    @Override
    public void close() {
        if (file != null) {
            file.close();
            file = null;
        }
    }
}

package top.wcpe.mc.jm.updater.wedge;

import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

import java.io.File;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;

import static org.junit.jupiter.api.Assertions.assertTrue;

/** Wedge 本地日志文件回归测试：玩家侧排障必须能看到楔子启动与失败原因。 */
class WedgeLoggerTest {

    @Test
    void writesWedgeLogFileWhenConfigMissing(@TempDir Path tmp) throws Exception {
        File wedgeDir = tmp.resolve("wedge").toFile();
        File gameDir = tmp.resolve("game").toFile();
        wedgeDir.mkdirs();

        Wedge.runUpdate(wedgeDir, gameDir.getAbsolutePath(), Messages.forLanguage("zh"));

        Path logFile = gameDir.toPath().resolve(".jm-updater/logs/wedge.log");
        assertTrue(Files.isRegularFile(logFile), "楔子应写入 .jm-updater/logs/wedge.log");
        String log = new String(Files.readAllBytes(logFile), StandardCharsets.UTF_8);
        assertTrue(log.contains("未找到 jm-updater.json"), "日志应包含配置缺失原因");
    }
}

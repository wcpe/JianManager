package top.wcpe.mc.jm.updater.wedge;

import java.io.File;
import java.io.IOException;
import java.util.logging.FileHandler;
import java.util.logging.Formatter;
import java.util.logging.Level;
import java.util.logging.LogRecord;
import java.util.logging.Logger;

/** 楔子本地诊断日志，写入 gameDir/.jm-updater/logs/wedge.log。 */
final class WedgeLogger implements AutoCloseable {

    private static final Formatter FORMATTER = new Formatter() {
        @Override
        public String format(LogRecord record) {
            return String.format("%1$tF %1$tT.%1$tL [%2$s] [jm-updater-wedge] %3$s%n",
                    record.getMillis(), levelName(record.getLevel()), formatMessage(record));
        }
    };

    private final Logger logger;
    private final FileHandler handler;

    private WedgeLogger(Logger logger, FileHandler handler) {
        this.logger = logger;
        this.handler = handler;
    }

    static WedgeLogger create(File gameDir) {
        FileHandler handler = null;
        Logger logger = Logger.getLogger("top.wcpe.mc.jm.updater.wedge." + System.identityHashCode(gameDir));
        logger.setUseParentHandlers(false);
        logger.setLevel(Level.ALL);
        try {
            File logDir = new File(new File(gameDir, ".jm-updater"), "logs");
            if (!logDir.isDirectory()) {
                logDir.mkdirs();
            }
            handler = new FileHandler(new File(logDir, "wedge.log").getAbsolutePath(), true);
            handler.setEncoding("UTF-8");
            handler.setFormatter(FORMATTER);
            handler.setLevel(Level.ALL);
            logger.addHandler(handler);
        } catch (IOException | SecurityException e) {
            handler = null;
        }
        return new WedgeLogger(logger, handler);
    }

    void info(String msg) {
        logger.log(Level.INFO, msg);
    }

    void warn(String msg) {
        logger.log(Level.WARNING, msg);
    }

    void error(String msg) {
        logger.log(Level.SEVERE, msg);
    }

    void debug(String msg) {
        logger.log(Level.FINE, msg);
    }

    @Override
    public void close() {
        if (handler != null) {
            handler.close();
            logger.removeHandler(handler);
        }
    }

    private static String levelName(Level level) {
        if (level.intValue() >= Level.SEVERE.intValue()) {
            return "ERROR";
        }
        if (level.intValue() >= Level.WARNING.intValue()) {
            return "WARN";
        }
        if (level.intValue() >= Level.INFO.intValue()) {
            return "INFO";
        }
        return "DEBUG";
    }
}

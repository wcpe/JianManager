package top.wcpe.mc.jm.updater.core;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** core 进度文案 i18n（FR-099）：中/英选择 + 回退 + 数字/时长格式稳定。 */
class CoreMessagesTest {

    @Test
    void zhAndEnSelection() {
        assertTrue(CoreMessages.forLanguage("zh").title().contains("客户端"));
        assertEquals("JM Client Update", CoreMessages.forLanguage("en").title());
    }

    @Test
    void unknownLanguageFallsBackToEnglish() {
        assertEquals("JM Client Update", CoreMessages.forLanguage("fr").title());
        assertTrue(CoreMessages.forLanguage("de").downloading().startsWith("Downloading"));
    }

    @Test
    void etaWordingLocalized() {
        assertTrue(CoreMessages.forLanguage("zh").eta(-1).contains("计算中"));
        assertTrue(CoreMessages.forLanguage("en").eta(-1).contains("--"));
        assertTrue(CoreMessages.forLanguage("zh").eta(12).contains("12s"));
        assertTrue(CoreMessages.forLanguage("en").eta(12).startsWith("ETA"));
    }

    @Test
    void humanBytesStableDecimal() {
        assertEquals("512 B", CoreMessages.humanBytes(512));
        assertEquals("1.0 KB", CoreMessages.humanBytes(1024));
        assertEquals("1.0 MB", CoreMessages.humanBytes(1024 * 1024));
        assertTrue(CoreMessages.humanSpeed(3_355_443L).endsWith("MB/s"));
    }

    @Test
    void humanDurationFormat() {
        assertEquals("12s", CoreMessages.humanDuration(12));
        assertEquals("2m 05s", CoreMessages.humanDuration(125));
        assertEquals("--", CoreMessages.humanDuration(-1));
    }
}

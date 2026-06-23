package top.jm.updater.wedge;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertNotEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

class MessagesTest {

    @Test
    void chineseAndEnglishDiffer() {
        Messages zh = Messages.forLanguage("zh");
        Messages en = Messages.forLanguage("en");
        for (Messages.Key key : Messages.Key.values()) {
            assertNotEquals(zh.get(key), en.get(key), "中英文文案应不同: " + key);
        }
    }

    @Test
    void unknownLanguageFallsBackToEnglish() {
        Messages fr = Messages.forLanguage("fr");
        Messages en = Messages.forLanguage("en");
        assertTrue(fr.get(Messages.Key.STARTING).equals(en.get(Messages.Key.STARTING)),
                "未知语言回退英文");
    }

    @Test
    void chineseMessagesAreNonEmpty() {
        Messages zh = Messages.forLanguage("zh");
        for (Messages.Key key : Messages.Key.values()) {
            assertTrue(zh.get(key).length() > 0, "文案非空: " + key);
        }
    }
}

package top.jm.updater.wedge;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNull;

class GameDirResolverTest {

    @Test
    void agentArgsTakesPriority() {
        assertEquals("/games/sb",
                GameDirResolver.resolve("/games/sb", "java --gameDir /other Main"));
    }

    @Test
    void agentArgsStripsSurroundingQuotes() {
        assertEquals("C:/Games/My Pack",
                GameDirResolver.resolve("\"C:/Games/My Pack\"", null));
    }

    @Test
    void fallsBackToJavaCommandSpaceForm() {
        assertEquals("/home/p/.minecraft",
                GameDirResolver.resolve(null,
                        "net.minecraft.client.main.Main --gameDir /home/p/.minecraft --width 800"));
    }

    @Test
    void fallsBackToJavaCommandEqualsForm() {
        assertEquals("/home/p/.minecraft",
                GameDirResolver.resolve("",
                        "Main --gameDir=/home/p/.minecraft --version 1.20"));
    }

    @Test
    void parsesQuotedGameDirWithSpaces() {
        assertEquals("C:/Users/Joe/My Pack",
                GameDirResolver.parseGameDirFlag(
                        "Main --gameDir \"C:/Users/Joe/My Pack\" --width 1280"));
    }

    @Test
    void returnsNullWhenNoSource() {
        assertNull(GameDirResolver.resolve(null, "java -jar launcher.jar nogamedirflag"));
        assertNull(GameDirResolver.resolve("   ", null));
    }

    @Test
    void doesNotMatchGameDirPrefix() {
        // --gameDirectory 不是 --gameDir。
        assertNull(GameDirResolver.parseGameDirFlag("Main --gameDirectory /x"));
    }

    @Test
    void gameDirAtEndOfCommand() {
        assertEquals("/last",
                GameDirResolver.parseGameDirFlag("Main --width 800 --gameDir /last"));
    }
}

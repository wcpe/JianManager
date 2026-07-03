package top.wcpe.mc.jm.updater.core;

import org.junit.jupiter.api.Test;

import java.util.Properties;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** 构建元信息：版本、提交与 dirty 标记必须可读且可展示。 */
class BuildInfoTest {

    @Test
    void fromPropertiesBuildsCleanDisplayVersion() {
        Properties p = new Properties();
        p.setProperty("version", "0.1.0-SNAPSHOT");
        p.setProperty("gitCommit", "abc123def456");
        p.setProperty("dirty", "false");
        p.setProperty("buildTime", "2026-07-03T00:00:00Z");

        BuildInfo info = BuildInfo.fromProperties(p);

        assertEquals("0.1.0-SNAPSHOT", info.version());
        assertEquals("abc123def456", info.gitCommit());
        assertFalse(info.dirty());
        assertEquals("2026-07-03T00:00:00Z", info.buildTime());
        assertEquals("0.1.0-SNAPSHOT+abc123def456", info.display());
    }

    @Test
    void fromPropertiesMarksDirtyDisplayVersion() {
        Properties p = new Properties();
        p.setProperty("version", "0.1.0-SNAPSHOT");
        p.setProperty("gitCommit", "abc123def456");
        p.setProperty("dirty", "true");

        BuildInfo info = BuildInfo.fromProperties(p);

        assertTrue(info.dirty());
        assertEquals("0.1.0-SNAPSHOT+abc123def456.dirty", info.display());
    }

    @Test
    void fromPropertiesFallsBackToUnknownValues() {
        BuildInfo info = BuildInfo.fromProperties(new Properties());

        assertEquals("unknown", info.version());
        assertEquals("unknown", info.gitCommit());
        assertFalse(info.dirty());
        assertEquals("", info.buildTime());
        assertEquals("unknown", info.display());
    }

    @Test
    void currentLoadsGeneratedResource() {
        BuildInfo info = BuildInfo.current();

        assertNotNull(info.version());
        assertNotNull(info.gitCommit());
        assertNotNull(info.buildTime());
        assertFalse(info.display().trim().isEmpty());
    }
}

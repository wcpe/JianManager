package top.wcpe.mc.jm.updater.core;

import org.junit.jupiter.api.Test;

import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** 启动安全画像 hello：字段齐备，且不受诊断遥测开关影响。 */
class SecurityHelloTest {

    @Test
    void buildBodyContainsRequiredProfileFields() {
        SecurityIdentity identity = new SecurityIdentity(
                "skyblock-s1", "Alex", "machine-1", "install-1", "12", "3", 8L);

        String json = SecurityHello.buildBody(identity);

        @SuppressWarnings("unchecked")
        Map<String, Object> body = (Map<String, Object>) Json.parse(json);
        assertEquals("skyblock-s1", body.get("channel"));
        assertEquals("Alex", body.get("playerName"));
        assertEquals("machine-1", body.get("machineId"));
        assertEquals("install-1", body.get("installId"));
        assertEquals("12", body.get("coreVersion"));
        assertEquals("3", body.get("wedgeVersion"));
        assertEquals("8", body.get("manifestVersion"));
        assertNotNull(body.get("os"));
        assertNotNull(body.get("osVersion"));
        assertNotNull(body.get("arch"));
        assertNotNull(body.get("javaVendor"));
        assertNotNull(body.get("javaVersion"));
        assertNotNull(body.get("javaArch"));
        assertNotNull(body.get("launcher"));
        assertNotNull(body.get("locale"));
        assertNotNull(body.get("timezone"));
        assertNotNull(body.get("memoryTier"));
    }

    @Test
    void emptyPlayerNameStillPresentInBody() {
        SecurityIdentity identity = new SecurityIdentity(
                "skyblock-s1", "", "machine-1", "install-1", "12", "", -1L);

        @SuppressWarnings("unchecked")
        Map<String, Object> body = (Map<String, Object>) Json.parse(SecurityHello.buildBody(identity));

        assertTrue(body.containsKey("playerName"), "玩家名缺失时也必须上报空字段供服务端判险");
        assertEquals("", body.get("playerName"));
    }

    @Test
    void telemetryDisabledDoesNotDisableSecurityHello() {
        TestFixtures.MemoryTransport transport = new TestFixtures.MemoryTransport();
        SecurityIdentity identity = new SecurityIdentity(
                "skyblock-s1", "Alex", "machine-1", "install-1", "12", "", -1L);

        SecurityHello.postBestEffort(transport, identity);

        assertNotNull(transport.lastHello, "hello 是必要安全画像，不受 telemetry=false 影响");
        @SuppressWarnings("unchecked")
        Map<String, Object> body = (Map<String, Object>) Json.parse(transport.lastHello);
        assertEquals("Alex", body.get("playerName"));
        assertEquals("install-1", body.get("installId"));
    }
}

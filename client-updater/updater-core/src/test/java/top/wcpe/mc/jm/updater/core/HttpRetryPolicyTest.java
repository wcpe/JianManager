package top.wcpe.mc.jm.updater.core;

import org.junit.jupiter.api.Test;

import java.io.IOException;
import java.time.Instant;
import java.util.Arrays;
import java.util.List;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertThrows;

/** 429 / Retry-After 退避策略：可测 sleeper，避免单测真实等待。 */
class HttpRetryPolicyTest {

    @Test
    void parsesRetryAfterSeconds() {
        assertEquals(3000L, HttpRetryPolicy.parseRetryAfterMillis("3", Instant.EPOCH));
    }

    @Test
    void parsesRetryAfterHttpDate() {
        long millis = HttpRetryPolicy.parseRetryAfterMillis(
                "Thu, 01 Jan 1970 00:00:05 GMT", Instant.EPOCH);

        assertEquals(5000L, millis);
    }

    @Test
    void rateLimitedResponseBacksOffBeforeRetry() throws Exception {
        RecordingSleeper sleeper = new RecordingSleeper();
        ScriptedCall call = new ScriptedCall(
                new HttpRetryPolicy.HttpFailure(429, "RATE_LIMITED", "2"),
                "ok");

        String result = HttpRetryPolicy.execute("manifest", call, sleeper, Instant.EPOCH);

        assertEquals("ok", result);
        assertEquals(Arrays.asList(2000L), sleeper.delays);
        assertEquals(2, call.attempts);
    }

    @Test
    void invalidClientKeyDoesNotRetryStorm() {
        RecordingSleeper sleeper = new RecordingSleeper();
        ScriptedCall call = new ScriptedCall(
                new HttpRetryPolicy.HttpFailure(403, "INVALID_CLIENT_KEY", "1"));

        IOException error = assertThrows(IOException.class,
                () -> HttpRetryPolicy.execute("manifest", call, sleeper, Instant.EPOCH));

        assertEquals("manifest 请求被拒绝 HTTP 403 errCode=INVALID_CLIENT_KEY", error.getMessage());
        assertFalse(sleeper.delays.isEmpty(), "带 Retry-After 时可单次退避后放弃");
        assertEquals(1, call.attempts, "INVALID_CLIENT_KEY 不得快速重试形成风暴");
    }

    private static final class RecordingSleeper implements HttpRetryPolicy.Sleeper {
        final List<Long> delays = new java.util.ArrayList<>();

        @Override
        public void sleep(long millis) {
            delays.add(millis);
        }
    }

    private static final class ScriptedCall implements HttpRetryPolicy.Call<String> {
        private final Object[] results;
        int attempts;

        ScriptedCall(Object... results) {
            this.results = results;
        }

        @Override
        public String run() throws IOException {
            Object result = results[attempts++];
            if (result instanceof IOException) {
                throw (IOException) result;
            }
            return (String) result;
        }
    }
}

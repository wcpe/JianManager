package top.wcpe.mc.jm.updater.core;

import java.io.IOException;
import java.time.Instant;
import java.time.ZonedDateTime;
import java.time.format.DateTimeFormatter;
import java.time.format.DateTimeParseException;
import java.util.Arrays;
import java.util.HashSet;
import java.util.Set;

/** HTTP 429 / 可恢复 403 的 Retry-After 退避策略。 */
final class HttpRetryPolicy {

    private static final int MAX_ATTEMPTS = 3;
    private static final long MAX_DELAY_MS = 30_000L;
    private static final Set<String> NO_RETRY_CODES = new HashSet<>(Arrays.asList(
            "INVALID_CLIENT_KEY", "CLIENT_KEY_SUSPENDED"));

    private HttpRetryPolicy() {
    }

    interface Call<T> {
        T run() throws IOException;
    }

    interface Sleeper {
        void sleep(long millis) throws InterruptedException;
    }

    static final class ThreadSleeper implements Sleeper {
        @Override
        public void sleep(long millis) throws InterruptedException {
            Thread.sleep(millis);
        }
    }

    static final class HttpFailure extends IOException {
        final int status;
        final String errCode;
        final String retryAfter;

        HttpFailure(int status, String errCode, String retryAfter) {
            super("HTTP " + status + (errCode == null || errCode.isEmpty() ? "" : " errCode=" + errCode));
            this.status = status;
            this.errCode = errCode == null ? "" : errCode;
            this.retryAfter = retryAfter;
        }
    }

    static <T> T execute(String operation, Call<T> call, Sleeper sleeper, Instant now) throws IOException {
        int attempts = 0;
        while (true) {
            attempts++;
            try {
                return call.run();
            } catch (HttpFailure failure) {
                boolean noRetry = NO_RETRY_CODES.contains(failure.errCode);
                if (!shouldRetry(failure) || noRetry || attempts >= MAX_ATTEMPTS) {
                    sleepOnceIfRequested(failure, sleeper, now);
                    throw readable(operation, failure);
                }
                sleepBeforeRetry(failure, sleeper, now);
            }
        }
    }

    static long parseRetryAfterMillis(String value, Instant now) {
        if (value == null || value.trim().isEmpty()) {
            return 0L;
        }
        String trimmed = value.trim();
        try {
            long seconds = Long.parseLong(trimmed);
            return clampDelay(seconds * 1000L);
        } catch (NumberFormatException ignored) {
            // 非秒数时按 HTTP-date 尝试解析。
        }
        try {
            Instant target = ZonedDateTime.parse(trimmed, DateTimeFormatter.RFC_1123_DATE_TIME).toInstant();
            return clampDelay(target.toEpochMilli() - now.toEpochMilli());
        } catch (DateTimeParseException e) {
            return 0L;
        }
    }

    private static boolean shouldRetry(HttpFailure failure) {
        if (failure.status == 429) {
            return true;
        }
        return failure.status == 403 && ("CHANNEL_PROTECTED".equals(failure.errCode)
                || "RATE_LIMITED".equals(failure.errCode));
    }

    private static void sleepBeforeRetry(HttpFailure failure, Sleeper sleeper, Instant now) throws IOException {
        long delay = parseRetryAfterMillis(failure.retryAfter, now);
        if (delay <= 0L) {
            delay = 1000L;
        }
        sleep(sleeper, delay);
    }

    private static void sleepOnceIfRequested(HttpFailure failure, Sleeper sleeper, Instant now) throws IOException {
        long delay = parseRetryAfterMillis(failure.retryAfter, now);
        if (delay > 0L) {
            sleep(sleeper, delay);
        }
    }

    private static void sleep(Sleeper sleeper, long delay) throws IOException {
        try {
            sleeper.sleep(delay);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new IOException("等待 Retry-After 被中断", e);
        }
    }

    private static IOException readable(String operation, HttpFailure failure) {
        return new IOException(operation + " 请求被拒绝 HTTP " + failure.status
                + (failure.errCode.isEmpty() ? "" : " errCode=" + failure.errCode));
    }

    private static long clampDelay(long delay) {
        if (delay <= 0L) {
            return 0L;
        }
        return Math.min(delay, MAX_DELAY_MS);
    }
}

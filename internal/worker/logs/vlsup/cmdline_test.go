package vlsup

import (
	"strings"
	"testing"
)

func baseCfg(ns Namespace) InstanceConfig {
	return InstanceConfig{
		Namespace:       ns,
		Port:            DefaultPort(ns),
		StorageDataPath: StoragePathUnder("C:\\data", ns),
		RetentionPeriod: DefaultRetentionPeriod,
		AuthUsername:    DefaultAuthUsername,
		AuthPassword:    "local-only",
	}
}

func TestBuildArgsHotDefaults(t *testing.T) {
	cfg := baseCfg(NamespaceHot)
	args, err := BuildArgs(cfg)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-storageDataPath=" + cfg.StorageDataPath,
		"-httpListenAddr=127.0.0.1:19441",
		"-retentionPeriod=30d",
		"-memory.allowedBytes=536870912",
		"-httpAuth.username=jm",
		"-httpAuth.password=local-only",
	} {
		if !containsArg(args, want) {
			t.Fatalf("missing arg %q in %v", want, args)
		}
	}
	if strings.Contains(joined, "0.0.0.0") {
		t.Fatalf("must bind localhost only, got %s", joined)
	}
}

func TestBuildArgsColdOmitsDefaultCache(t *testing.T) {
	cfg := baseCfg(NamespaceCold)
	args, err := BuildArgs(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range args {
		if strings.HasPrefix(a, "-memory.allowedBytes=") {
			t.Fatalf("cold without override must omit cache flag, got %v", args)
		}
	}
	if !containsArg(args, "-httpListenAddr=127.0.0.1:19442") {
		t.Fatalf("cold port mismatch: %v", args)
	}
}

func TestBuildArgsExplicitCache(t *testing.T) {
	cfg := baseCfg(NamespaceRehydrate)
	cfg.MemoryAllowedBytes = 64 * 1024 * 1024
	args, err := BuildArgs(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !containsArg(args, "-memory.allowedBytes=67108864") {
		t.Fatalf("expected explicit cache flag: %v", args)
	}
}

func TestBuildArgsValidation(t *testing.T) {
	cfg := baseCfg(NamespaceHot)
	cfg.StorageDataPath = ""
	if _, err := BuildArgs(cfg); err == nil {
		t.Fatal("empty storage path must fail")
	}
	cfg = baseCfg("nope")
	if _, err := BuildArgs(cfg); err == nil {
		t.Fatal("unknown namespace must fail")
	}
	cfg = baseCfg(NamespaceHot)
	cfg.Port = 0
	if _, err := BuildArgs(cfg); err == nil {
		t.Fatal("invalid port must fail")
	}
	cfg = baseCfg(NamespaceHot)
	cfg.AuthPassword = ""
	if _, err := BuildArgs(cfg); err == nil {
		t.Fatal("empty auth must fail")
	}
}

func TestEffectiveCacheBytes(t *testing.T) {
	hot := baseCfg(NamespaceHot)
	if got := hot.EffectiveCacheBytes(); got != DefaultHotCacheBytes {
		t.Fatalf("hot default cache: got %d want %d", got, DefaultHotCacheBytes)
	}
	cold := baseCfg(NamespaceCold)
	if got := cold.EffectiveCacheBytes(); got != 0 {
		t.Fatalf("cold default should omit cache, got %d", got)
	}
}

func TestListenAddrLoopbackOnly(t *testing.T) {
	addr := ListenAddr(19441)
	if !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Fatalf("listen addr must be loopback: %s", addr)
	}
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

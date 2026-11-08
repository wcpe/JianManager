package retention

import (
	"math"
	"testing"
	"time"

	"github.com/wcpe/JianManager/internal/platform/vlruntime"
)

func TestRuntimeHeadroomRejectsEqualTTLAndAccountsUTCDayAndSLA(t *testing.T) {
	hot := 90 * 24 * time.Hour
	req := RuntimeHeadroom{DriverInterval: 15 * time.Minute, CopyVerifyBudget: time.Hour, RecoveryBudget: 24 * time.Hour}
	for _, native := range []string{"90d", "91d", "92d", "23h", "NaNd"} {
		if _, err := ValidateRuntimeHeadroom(native, hot, req); err == nil {
			t.Errorf("余量不足必须拒绝: %q", native)
		}
	}
	margin, err := ValidateRuntimeHeadroom(vlruntime.DefaultRetentionPeriod, hot, req)
	// 2026-10-29：默认原生 TTL 从 120d 改为 180d（单 VL 改造），余量=180d-90d=90d。
	// 不写死 90d：用同一解析函数从默认常量推导，改默认值时不会因写死旧值而假红。
	defTTL, defErr := ParseRuntimeTTL(vlruntime.DefaultRetentionPeriod)
	if defErr != nil {
		t.Fatalf("默认 retention 常量必须可解析: %v", defErr)
	}
	wantMargin := defTTL - hot
	if err != nil || margin != wantMargin {
		t.Fatalf("默认值应同源且有限余量应为 native-hot=%s: %s %v", wantMargin, margin, err)
	}
	_, err = ValidateRuntimeHeadroom("120d", hot, RuntimeHeadroom{CopyVerifyBudget: time.Duration(math.MaxInt64)})
	if err == nil {
		t.Fatal("余量求和溢出必须拒绝")
	}
	_, err = ValidateRuntimeHeadroom("120d", hot, RuntimeHeadroom{RecoveryBudget: -1})
	if err == nil {
		t.Fatal("负余量不能绕闸")
	}
}

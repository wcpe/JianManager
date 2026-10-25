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
	if err != nil || margin != 30*24*time.Hour {
		t.Fatalf("默认值应同源且有限余量=30d: %s %v", margin, err)
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

package proxy

import (
	"testing"
	"time"
)

func TestAggregateCheckResults(t *testing.T) {
	now := time.Now()
	rec := func(id int, success bool, kind CheckResultKind) CheckResultRecord {
		return CheckResultRecord{ProxyID: id, Success: success, Kind: kind, LastError: "boom", Timestamp: now}
	}

	t.Run("only fails below threshold", func(t *testing.T) {
		order, aggs := aggregateCheckResults([]CheckResultRecord{
			rec(1, false, KindPeriodic),
			rec(1, false, KindPeriodic),
		})
		if len(order) != 1 || order[0] != 1 {
			t.Fatalf("order = %v, want [1]", order)
		}
		a := aggs[1]
		if a.lastIsSuccess {
			t.Error("lastIsSuccess = true, want false")
		}
		if a.hadSuccess {
			t.Error("hadSuccess = true, want false")
		}
		if a.trailingFails != 2 {
			t.Errorf("trailingFails = %d, want 2", a.trailingFails)
		}
		if a.maxRun != 2 {
			t.Errorf("maxRun = %d, want 2", a.maxRun)
		}
		if a.lastImmediateFail || a.hasImmediateFail {
			t.Error("immediate flags set for all-periodic window")
		}
		if a.lastError != "boom" {
			t.Errorf("lastError = %q, want boom", a.lastError)
		}
	})

	t.Run("success in middle of window", func(t *testing.T) {
		// fail, fail, SUCCESS, fail, fail: the success resets the counter, so
		// only the two trailing failures accumulate.
		order, aggs := aggregateCheckResults([]CheckResultRecord{
			rec(1, false, KindPeriodic),
			rec(1, false, KindPeriodic),
			rec(1, true, KindPeriodic),
			rec(1, false, KindPeriodic),
			rec(1, false, KindPeriodic),
		})
		if len(order) != 1 || order[0] != 1 {
			t.Fatalf("order = %v, want [1]", order)
		}
		a := aggs[1]
		if !a.hadSuccess {
			t.Error("hadSuccess = false, want true")
		}
		if a.lastIsSuccess {
			t.Error("lastIsSuccess = true, want false")
		}
		if a.trailingFails != 2 {
			t.Errorf("trailingFails = %d, want 2", a.trailingFails)
		}
		if a.maxRun != 2 {
			t.Errorf("maxRun = %d, want 2", a.maxRun)
		}
		if a.lastError != "boom" {
			t.Errorf("lastError = %q, want boom (last record is a failure)", a.lastError)
		}
	})

	t.Run("last record is success", func(t *testing.T) {
		_, aggs := aggregateCheckResults([]CheckResultRecord{
			rec(1, false, KindPeriodic),
			rec(1, true, KindManual),
		})
		a := aggs[1]
		if !a.lastIsSuccess || !a.hadSuccess {
			t.Errorf("lastIsSuccess/hadSuccess = %v/%v, want true/true", a.lastIsSuccess, a.hadSuccess)
		}
		if a.trailingFails != 0 {
			t.Errorf("trailingFails = %d, want 0", a.trailingFails)
		}
		if a.lastError != "" {
			t.Errorf("lastError = %q, want empty", a.lastError)
		}
	})

	t.Run("mixed manual and periodic", func(t *testing.T) {
		// periodic fail, MANUAL fail, periodic fail: an immediate failure in
		// the window marks it; the last record is periodic.
		_, aggs := aggregateCheckResults([]CheckResultRecord{
			rec(1, false, KindPeriodic),
			rec(1, false, KindManual),
			rec(1, false, KindPeriodic),
		})
		a := aggs[1]
		if !a.hasImmediateFail {
			t.Error("hasImmediateFail = false, want true")
		}
		if a.lastImmediateFail {
			t.Error("lastImmediateFail = true, want false (last is periodic)")
		}
		if a.trailingFails != 3 || a.maxRun != 3 {
			t.Errorf("trailingFails/maxRun = %d/%d, want 3/3", a.trailingFails, a.maxRun)
		}
	})

	t.Run("last record is pool failure", func(t *testing.T) {
		_, aggs := aggregateCheckResults([]CheckResultRecord{
			rec(1, false, KindPeriodic),
			rec(1, false, KindPool),
		})
		a := aggs[1]
		if !a.lastImmediateFail || !a.hasImmediateFail {
			t.Errorf("lastImmediateFail/hasImmediateFail = %v/%v, want true/true", a.lastImmediateFail, a.hasImmediateFail)
		}
	})

	t.Run("multiple proxies keep arrival order", func(t *testing.T) {
		order, aggs := aggregateCheckResults([]CheckResultRecord{
			rec(2, false, KindPeriodic),
			rec(1, false, KindPeriodic),
			rec(2, true, KindPeriodic),
			rec(3, false, KindPeriodic),
		})
		if len(order) != 3 || order[0] != 2 || order[1] != 1 || order[2] != 3 {
			t.Fatalf("order = %v, want [2 1 3]", order)
		}
		if len(aggs) != 3 {
			t.Fatalf("aggs size = %d, want 3", len(aggs))
		}
	})
}

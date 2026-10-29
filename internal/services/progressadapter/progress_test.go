package progressadapter

import "testing"

func ptr[T any](v T) *T { return &v }

func TestResolvePreservesManualZeroAndLowerValues(t *testing.T) {
	manual := Metrics{ptr(int64(0)), ptr(0), ptr(10), ptr(0.0)}
	observed := Metrics{ptr(int64(7200)), ptr(12), ptr(20), ptr(60.0)}
	got := Resolve(manual, observed)
	if *got.PlaytimeSeconds != 0 || *got.AchievementsUnlocked != 0 || *got.AchievementsTotal != 10 || *got.CompletionPercent != 0 {
		t.Fatalf("manual fields were replaced: %+v", got)
	}
	*got.PlaytimeSeconds = 99
	if *manual.PlaytimeSeconds != 0 {
		t.Fatal("result aliases manual input")
	}
}

func TestResolveFallsBackPerFieldAndIsIdempotent(t *testing.T) {
	manual := Metrics{AchievementsUnlocked: ptr(2)}
	observed := Metrics{PlaytimeSeconds: ptr(int64(3600)), AchievementsUnlocked: ptr(5), AchievementsTotal: ptr(10)}
	for range 2 {
		got := Resolve(manual, observed)
		if *got.PlaytimeSeconds != 3600 || *got.AchievementsUnlocked != 2 || *got.AchievementsTotal != 10 || got.CompletionPercent != nil {
			t.Fatalf("incorrect field selection: %+v", got)
		}
		*got.PlaytimeSeconds = 0
		if *observed.PlaytimeSeconds != 3600 {
			t.Fatal("result aliases observation")
		}
	}
}

func TestResolveUnknownRemainsUnknown(t *testing.T) {
	if got := Resolve(Metrics{}, Metrics{}); got != (Metrics{}) {
		t.Fatalf("unknown became known: %+v", got)
	}
}

package publichostname

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestQuotaRedeployReleaseAndIndependentUsers(t *testing.T) {
	store := NewMemoryStore()
	service := Service{Store: store, Limit: 3, Now: func() time.Time { return time.Unix(1, 0) }}
	ctx := context.Background()
	for i, hostname := range []string{"one.test", "two.test", "three.test"} {
		if _, reused, err := service.Reserve(ctx, ReserveRequest{Hostname: hostname, OwnerUserID: "u1", ProjectID: "p" + string(rune('1'+i)), EnvironmentID: "env"}); err != nil || reused {
			t.Fatalf("reserve %s reused=%v err=%v", hostname, reused, err)
		}
	}
	if _, _, err := service.Reserve(ctx, ReserveRequest{Hostname: "four.test", OwnerUserID: "u1", ProjectID: "p4", EnvironmentID: "env"}); err == nil {
		t.Fatal("fourth hostname was accepted")
	}
	if _, reused, err := service.Reserve(ctx, ReserveRequest{Hostname: "one.test", OwnerUserID: "u2", ProjectID: "p1", EnvironmentID: "env"}); err != nil || !reused {
		t.Fatalf("same scope redeploy reused=%v err=%v", reused, err)
	}
	other, _, err := service.Reserve(ctx, ReserveRequest{Hostname: "other.test", OwnerUserID: "u2", ProjectID: "p9", EnvironmentID: "env"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.ReleasePending(ctx, other.ID); err != nil {
		t.Fatal(err)
	}
	quota, _ := service.Quota(ctx, "u2")
	if quota.Used != 1 {
		t.Fatalf("release_pending used=%d", quota.Used)
	}
	if _, err = service.Released(ctx, other.ID); err != nil {
		t.Fatal(err)
	}
	quota, _ = service.Quota(ctx, "u2")
	if quota.Used != 0 {
		t.Fatalf("released used=%d", quota.Used)
	}
}

func TestConcurrentReservationNeverExceedsQuota(t *testing.T) {
	service := Service{Store: NewMemoryStore(), Limit: 3}
	var wg sync.WaitGroup
	var mu sync.Mutex
	succeeded := 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := service.Reserve(context.Background(), ReserveRequest{Hostname: string(rune('a'+i)) + ".test", OwnerUserID: "u", ProjectID: string(rune('a' + i)), EnvironmentID: "env"})
			if err == nil {
				mu.Lock()
				succeeded++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if succeeded != 3 {
		t.Fatalf("succeeded=%d", succeeded)
	}
}

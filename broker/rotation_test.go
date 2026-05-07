package broker

import (
	"context"
	"testing"
	"time"
)

func mkAcc(email string, plan PlanType, primaryPct, secondaryPct float64) *Account {
	a := &Account{
		Email:       email,
		AccountID:   email,
		PlanType:    plan,
		AccessToken: "t",
	}
	a.state.Store(&accountState{
		PrimaryUsedPct:   primaryPct,
		SecondaryUsedPct: secondaryPct,
	})
	return a
}

func TestProOutranksPlus(t *testing.T) {
	pool := NewPool(PoolConfig{})
	pool.Add(mkAcc("plus@x.com", PlanPlus, 0, 0))
	pool.Add(mkAcc("pro@x.com", PlanPro, 0, 0))
	lease, err := pool.Lease(context.Background(), LeaseHint{})
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	defer lease.Release()
	if lease.Account.Email != "pro@x.com" {
		t.Errorf("chose %q, want pro@x.com (Pro must outrank Plus)", lease.Account.Email)
	}
}

func TestDrainPrefersLowerSecondaryUsed(t *testing.T) {
	pool := NewPool(PoolConfig{})
	pool.Add(mkAcc("a@x.com", PlanPro, 0, 0.10))
	pool.Add(mkAcc("b@x.com", PlanPro, 0, 0.80))
	lease, err := pool.Lease(context.Background(), LeaseHint{})
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	defer lease.Release()
	if lease.Account.Email != "a@x.com" {
		t.Errorf("chose %q, want a@x.com (less secondary used)", lease.Account.Email)
	}
}

func TestExcludesOverThreshold(t *testing.T) {
	pool := NewPool(PoolConfig{
		PrimaryUsedPctMax:   0.95,
		SecondaryUsedPctMax: 0.99,
	})
	hot := mkAcc("hot@x.com", PlanPro, 0.96, 0)
	cool := mkAcc("cool@x.com", PlanPlus, 0.10, 0)
	pool.Add(hot)
	pool.Add(cool)
	lease, err := pool.Lease(context.Background(), LeaseHint{})
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	defer lease.Release()
	if lease.Account.Email == "hot@x.com" {
		t.Errorf("hot account should be excluded above primary threshold")
	}
}

func TestExcludesDisabledAndDead(t *testing.T) {
	pool := NewPool(PoolConfig{})
	dead := mkAcc("dead@x.com", PlanPro, 0, 0)
	dead.MarkDead("auth_failed")
	disabled := mkAcc("dis@x.com", PlanPro, 0, 0)
	disabled.SetDisabled(true)
	live := mkAcc("live@x.com", PlanPlus, 0, 0)
	pool.Add(dead)
	pool.Add(disabled)
	pool.Add(live)
	lease, err := pool.Lease(context.Background(), LeaseHint{})
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	defer lease.Release()
	if lease.Account.Email != "live@x.com" {
		t.Errorf("chose %q, want live@x.com", lease.Account.Email)
	}
}

func TestExcludesCooldown(t *testing.T) {
	pool := NewPool(PoolConfig{})
	cooled := mkAcc("cooled@x.com", PlanPro, 0, 0)
	cooled.MarkCooldown(time.Now().Add(30 * time.Second))
	open := mkAcc("open@x.com", PlanPlus, 0, 0)
	pool.Add(cooled)
	pool.Add(open)
	lease, err := pool.Lease(context.Background(), LeaseHint{})
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	defer lease.Release()
	if lease.Account.Email != "open@x.com" {
		t.Errorf("chose %q, want open@x.com", lease.Account.Email)
	}
}

func TestAllExhaustedReturnsError(t *testing.T) {
	pool := NewPool(PoolConfig{})
	dead := mkAcc("dead@x.com", PlanPro, 0, 0)
	dead.MarkDead("x")
	pool.Add(dead)
	if _, err := pool.Lease(context.Background(), LeaseHint{}); err != ErrAllExhausted {
		t.Errorf("err = %v, want ErrAllExhausted", err)
	}
}

func TestNoAccountsReturnsError(t *testing.T) {
	pool := NewPool(PoolConfig{})
	if _, err := pool.Lease(context.Background(), LeaseHint{}); err != ErrNoAccounts {
		t.Errorf("err = %v, want ErrNoAccounts", err)
	}
}

func TestInflightPenaltyRotates(t *testing.T) {
	pool := NewPool(PoolConfig{})
	pool.Add(mkAcc("a@x.com", PlanPro, 0, 0))
	pool.Add(mkAcc("b@x.com", PlanPro, 0, 0))

	first, err := pool.Lease(context.Background(), LeaseHint{})
	if err != nil {
		t.Fatalf("first lease: %v", err)
	}
	second, err := pool.Lease(context.Background(), LeaseHint{})
	if err != nil {
		t.Fatalf("second lease: %v", err)
	}
	if first.Account.Email == second.Account.Email {
		t.Errorf("both leases gave %q — inflight penalty did not rotate", first.Account.Email)
	}
	first.Release()
	second.Release()
}

type fakePinStore struct {
	pins map[string]string
}

func (s *fakePinStore) Put(prev, email string, _ time.Time) error {
	s.pins[prev] = email
	return nil
}
func (s *fakePinStore) Lookup(prev string) (string, bool) {
	v, ok := s.pins[prev]
	return v, ok
}

func TestPinReusesAccount(t *testing.T) {
	pool := NewPool(PoolConfig{})
	pool.Add(mkAcc("a@x.com", PlanPro, 0, 0))
	pool.Add(mkAcc("b@x.com", PlanPro, 0, 0))

	pin := &fakePinStore{pins: map[string]string{"resp_x": "b@x.com"}}
	pool.SetPinStore(pin, time.Hour)

	for i := 0; i < 3; i++ {
		lease, err := pool.Lease(context.Background(), LeaseHint{PreviousResponseID: "resp_x"})
		if err != nil {
			t.Fatalf("lease %d: %v", i, err)
		}
		if lease.Account.Email != "b@x.com" {
			t.Errorf("attempt %d chose %q, want pinned b@x.com", i, lease.Account.Email)
		}
		lease.Release()
	}
}

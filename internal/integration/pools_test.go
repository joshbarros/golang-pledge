package integration

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/joshbarros/golang-pledge/internal/platform/messaging"
	"github.com/joshbarros/golang-pledge/internal/pools"
)

// rawPool inserts a pool directly (below the API) and returns its id.
func (e *env) rawPool(cents int64) uuid.UUID {
	e.t.Helper()
	id := uuid.New()
	_, err := e.pool.Exec(context.Background(), `INSERT INTO pools.pools (id, code, sponsor, currency, cause, opened_at, opening_cents, available_cents, created_at)
		VALUES ($1,$2,'s','USD','c',now(),$3,$3,now())`, id, code(), cents)
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

func reserve(e *env, pool uuid.UUID, cents int64) (pools.ReserveOutcome, int64) {
	d := uuid.New()
	out, err := e.app.Pools.Reserve(context.Background(), uuid.New(), messaging.ReserveMatch{DonationID: d, OwnerID: uuid.New(), PoolID: pool, AmountCents: cents, Reference: "R"})
	if err != nil {
		e.t.Fatal(err)
	}
	var matched int64
	_ = e.pool.QueryRow(context.Background(), `SELECT matched_cents FROM pools.match_holds WHERE donation_id = $1`, d).Scan(&matched)
	return out, matched
}

func TestConcurrentDonationsMatchExactlyThePoolBetweenThem(t *testing.T) {
	e := newEnv(t)
	pool := e.rawPool(1000)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var total int64
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, matched := reserve(e, pool, 200)
			mu.Lock()
			total += matched
			mu.Unlock()
		}()
	}
	wg.Wait()
	var left int64
	_ = e.pool.QueryRow(context.Background(), `SELECT available_cents FROM pools.pools WHERE id = $1`, pool).Scan(&left)
	if total != 1000 || left != 0 {
		t.Fatalf("twelve donations of 200 against 1000 match exactly 1000 between them: matched %d, left %d", total, left)
	}
}

func TestTwoDonorsShareTheLastCentsInEitherOrder(t *testing.T) {
	e := newEnv(t)
	pool := e.rawPool(100)
	a, ma := reserve(e, pool, 70)
	b, mb := reserve(e, pool, 70)
	if ma != 70 || mb != 30 || a != pools.Full || b != pools.Partial {
		t.Fatalf("70 then 70 out of 100: %d (%v) and %d (%v)", ma, a, mb, b)
	}
	pool = e.rawPool(100)
	_, m1 := reserve(e, pool, 60)
	_, m2 := reserve(e, pool, 60)
	_, m3 := reserve(e, pool, 60)
	if m1 != 60 || m2 != 40 || m3 != 0 {
		t.Fatalf("60, 40, then nothing: %d %d %d", m1, m2, m3)
	}
}

func TestAnUnknownPoolMatchesNothingAndStillAnswers(t *testing.T) {
	e := newEnv(t)
	out, matched := reserve(e, uuid.New(), 500)
	if out != pools.None || matched != 0 {
		t.Fatalf("nothing to match against: %v %d", out, matched)
	}
}

func TestReleaseGivesBackWhatWasMatchedExactlyOnce(t *testing.T) {
	e := newEnv(t)
	pool := e.rawPool(100)
	donation := uuid.New()
	if _, err := e.app.Pools.Reserve(context.Background(), uuid.New(), messaging.ReserveMatch{DonationID: donation, OwnerID: uuid.New(), PoolID: pool, AmountCents: 250, Reference: "R"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := e.app.Pools.Release(context.Background(), uuid.New(), messaging.ReleaseMatch{DonationID: donation, Reason: "refund"}); err != nil {
			t.Fatal(err)
		}
	}
	var left int64
	_ = e.pool.QueryRow(context.Background(), `SELECT available_cents FROM pools.pools WHERE id = $1`, pool).Scan(&left)
	if left != 100 || e.outbox("pools", messaging.ReleasedKey) != 1 {
		t.Fatalf("100 matched, given back once, not three times: %d left", left)
	}
}

func TestOpeningAPoolValidatesIt(t *testing.T) {
	e := newEnv(t)
	adm := e.admin()
	good := map[string]any{"code": code(), "sponsor": "Acme", "currency": "USD", "cause": "Main", "openingCents": 100000}
	if r := e.post("/api/pools", good, adm.Token); r.Status != 201 || r.Body["availableCents"] != float64(100000) {
		t.Fatalf("a pool opens full: %d %s", r.Status, r.Raw)
	}
	for name, mut := range map[string]func(map[string]any){
		"no sponsor":  func(m map[string]any) { m["sponsor"] = "" },
		"zero cents":  func(m map[string]any) { m["openingCents"] = 0 },
		"too much":    func(m map[string]any) { m["openingCents"] = 10_000_000_001 },
		"no code":     func(m map[string]any) { m["code"] = "" },
		"long code":   func(m map[string]any) { m["code"] = "ABCDEFGHIJKLMNOPQRSTU" },
		"no currency": func(m map[string]any) { m["currency"] = " " },
	} {
		bad := map[string]any{"code": code(), "sponsor": "Acme", "currency": "USD", "cause": "Main", "openingCents": 100000}
		mut(bad)
		if r := e.post("/api/pools", bad, adm.Token); r.Status != 400 {
			t.Errorf("%s: %d", name, r.Status)
		}
	}
	if r := e.post("/api/pools", good, adm.Token); r.Status != 409 {
		t.Fatalf("the same code twice: %d", r.Status)
	}
	if r := e.post("/api/pools", good, e.register().Token); r.Status != 403 {
		t.Fatalf("a customer cannot open a pool: %d", r.Status)
	}
}

package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/joshbarros/golang-pledge/internal/platform/messaging"
	"github.com/joshbarros/golang-pledge/internal/receipts"
)

func (e *env) outbox(schema, key string) int {
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM `+schema+`.outbox_messages WHERE routing_key = $1`, key).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) count(sql string, args ...any) int {
	var n int
	if err := e.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) available(poolCode string) float64 {
	e.t.Helper()
	v, _ := e.get("/api/pools/"+poolCode, e.admin().Token).Body["availableCents"].(float64)
	return v
}

func status(e *env, s session, id string) string { return e.donation(s, id).str("status") }

func TestTheWholeSagaFromDonationToCompletedAndTheReceipt(t *testing.T) {
	e := newEnv(t)
	pool := e.openPool(100000)
	ana := e.register()
	created := e.enter(ana, pool, "INV-1", 30000)
	id := created.str("id")
	if created.Status != 202 || created.str("status") != "Requested" || created.Body["feeCents"] != float64(200) || created.Body["matchedCents"] != float64(0) {
		t.Fatalf("accepted as Requested with the fee (50 + 0.5%%): %s", created.Raw)
	}
	if dead := e.drain(); dead != 0 {
		t.Fatalf("nothing should dead-letter: %d", dead)
	}
	o := e.donation(ana, id)
	if o.str("status") != "Completed" || !strings.HasPrefix(o.str("receiptNumber"), "RC-") || o.Body["reason"] != nil || o.Body["matchedCents"] != float64(30000) {
		t.Fatalf("completed, matched in full, with a receipt number: %s", o.Raw)
	}
	if e.available(pool) != 70000 {
		t.Fatal("30000 of 100000 cents are matched")
	}
	c := e.get("/api/receipts/"+id, ana.Token)
	if c.Status != 200 || c.str("status") != "Issued" || c.str("number") != o.str("receiptNumber") {
		t.Fatalf("the receipt: %d %s", c.Status, c.Raw)
	}
}

func TestADonationIsMatchedInPartWhenThePoolRunsLowAndNotAtAllWhenItIsEmpty(t *testing.T) {
	e := newEnv(t)
	pool := e.openPool(10000)
	ana, bob, cy := e.register(), e.register(), e.register()
	a := e.enter(ana, pool, "INV-A", 6000).str("id")
	e.drain()
	b := e.enter(bob, pool, "INV-B", 6000).str("id")
	e.drain()
	c := e.enter(cy, pool, "INV-C", 500).str("id")
	e.drain()
	for id, s := range map[string]session{a: ana, b: bob, c: cy} {
		if status(e, s, id) != "Completed" {
			t.Fatalf("a donation always goes through, matched or not: %s", e.donation(s, id).Raw)
		}
	}
	if m := e.donation(ana, a).Body["matchedCents"]; m != float64(6000) {
		t.Fatalf("first: matched in full, got %v", m)
	}
	if m := e.donation(bob, b).Body["matchedCents"]; m != float64(4000) {
		t.Fatalf("second: only the last 4000 cents, got %v", m)
	}
	if m := e.donation(cy, c).Body["matchedCents"]; m != float64(0) {
		t.Fatalf("third: the pool is empty, got %v", m)
	}
	if e.available(pool) != 0 {
		t.Fatal("the pool is empty and never negative")
	}
}

func TestPoisonIsRejectedAndTheMatchComesBack(t *testing.T) {
	e := newEnv(t)
	pool := e.openPool(50000)
	ana := e.register()
	id := e.enter(ana, pool, "INV-POISON-1", 20000).str("id")
	if dead := e.drain(); dead != 1 {
		t.Fatalf("the poison receipt is dead-lettered once: %d", dead)
	}
	o := e.donation(ana, id)
	if o.str("status") != "Rejected" || !strings.Contains(o.str("reason"), "poison") || o.Body["matchReleasedAt"] == nil {
		t.Fatalf("rejected, with the reason, and the release recorded: %s", o.Raw)
	}
	if e.available(pool) != 50000 {
		t.Fatal("the matched cents are back")
	}
	if c := e.get("/api/receipts/"+id, ana.Token); c.str("status") != "Rejected" {
		t.Fatalf("the rejection is committed on the receipt: %s", c.Raw)
	}
}

func TestRefundGivesBackExactlyWhatWasMatchedAndKeepsTheReceipt(t *testing.T) {
	e := newEnv(t)
	pool := e.openPool(5000)
	ana := e.register()
	id := e.enter(ana, pool, "INV-1", 8000).str("id") // matched only 5000
	e.drain()
	if e.available(pool) != 0 {
		t.Fatal("the pool gave all it had")
	}
	if r := e.post("/api/donations/"+id+"/refund", nil, ana.Token); r.Status != 202 {
		t.Fatalf("refund: %d %s", r.Status, r.Raw)
	}
	e.drain()
	if status(e, ana, id) != "Refunded" || e.available(pool) != 5000 {
		t.Fatalf("refunded, and exactly the 5000 that were matched are back (not 8000): %v", e.available(pool))
	}
	if c := e.get("/api/receipts/"+id, ana.Token); c.str("status") != "Issued" {
		t.Fatal("a refund does not roll back the receipt that was issued")
	}
	if r := e.post("/api/donations/"+id+"/refund", nil, ana.Token); r.Status != 409 {
		t.Fatalf("a second refund is a conflict: %d", r.Status)
	}
}

func TestRefundBeatsAFinalMarkThatArrivesLate(t *testing.T) {
	e := newEnv(t)
	pool := e.openPool(50000)
	ana := e.register()
	id := uuid.MustParse(e.enter(ana, pool, "INV-1", 1000).str("id"))
	for pass := 0; pass < 4; pass++ {
		for _, row := range e.unsent() {
			if row.d.RoutingKey == messaging.IssuedKey {
				continue
			}
			e.markSent(row)
			_, _ = e.deliver(row.d.RoutingKey, row.d)
		}
	}
	if got := status(e, ana, id.String()); got != "Held" {
		t.Fatalf("held, receipt issued, final mark not yet landed: %s", got)
	}
	if r := e.post("/api/donations/"+id.String()+"/refund", nil, ana.Token); r.Status != 202 {
		t.Fatal(r.Status)
	}
	e.drain()
	o := e.donation(ana, id.String())
	if o.str("status") != "Refunded" || o.Body["receiptNumber"] != nil {
		t.Fatalf("the late final mark must not undo the refund: %s", o.Raw)
	}
	if e.available(pool) != 50000 {
		t.Fatal("the match is back")
	}
}

func TestADeliveryRepeatedByTheBrokerHasNoEffectTheSecondTime(t *testing.T) {
	e := newEnv(t)
	pool := e.openPool(50000)
	ana := e.register()
	e.enter(ana, pool, "INV-1", 1000)
	rows := e.unsent()
	if len(rows) != 1 {
		t.Fatal("one reserve command")
	}
	d := rows[0].d
	for i := 0; i < 3; i++ {
		if _, err := e.deliver(d.RoutingKey, d); err != nil {
			t.Fatal(err)
		}
	}
	if e.count(`SELECT count(*) FROM pools.match_holds`) != 1 || e.outbox("pools", messaging.HeldKey) != 1 || e.available(pool) != 49000 {
		t.Fatal("at-least-once delivery, exactly-once effect: the inbox absorbs the repeats")
	}
}

func TestAFinalMarkBeforeTheHeldMarkIsRetriedNotDropped(t *testing.T) {
	e := newEnv(t)
	pool := e.openPool(50000)
	ana := e.register()
	id := uuid.MustParse(e.enter(ana, pool, "INV-1", 1000).str("id"))
	_, err := e.app.Donations.MarkCompleted(context.Background(), uuid.New(), messaging.ReceiptIssued{DonationID: id, Number: "RC-TEST"}, 0)
	var ooo *messaging.OutOfOrderError
	if err == nil || !errorsAs(err, &ooo) {
		t.Fatalf("the final mark must ask to be retried, got %v", err)
	}
	if status(e, ana, id.String()) != "Requested" {
		t.Fatal("and change nothing")
	}
	e.drain()
	if status(e, ana, id.String()) != "Completed" {
		t.Fatal("the normal flow still completes")
	}
}

func TestIssueIsIdempotentAndPoisonIsAFlagNotAnException(t *testing.T) {
	e := newEnv(t)
	id, owner := uuid.New(), uuid.New()
	msg := uuid.New()
	held := messaging.MatchHeld{DonationID: id, OwnerID: owner, PoolID: uuid.New(), MatchedCents: 100, Reference: "INV-1"}
	first, err := e.app.Receipts.Issue(context.Background(), msg, held)
	again, err2 := e.app.Receipts.Issue(context.Background(), msg, held)
	if err != nil || err2 != nil || first != receipts.Issued || again != receipts.Duplicate {
		t.Fatalf("issue then duplicate: %v %v %v %v", first, err, again, err2)
	}
	poison := messaging.MatchHeld{DonationID: uuid.New(), OwnerID: owner, PoolID: uuid.New(), MatchedCents: 100, Reference: "xPOISONx"}
	if out, _ := e.app.Receipts.Issue(context.Background(), uuid.New(), poison); out != receipts.Rejected {
		t.Fatal("a poison reference is rejected")
	}
	if e.outbox("receipts", messaging.ReleaseKey) != 1 || e.outbox("receipts", messaging.RejectedKey) != 1 || e.outbox("receipts", messaging.IssuedKey) != 1 {
		t.Fatal("one issued, one release command and one rejection")
	}
}

// Package receipts issues the receipt once the match is held. A poison reference is committed as rejected.
package receipts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joshbarros/golang-pledge/internal/platform/httpx"
	"github.com/joshbarros/golang-pledge/internal/platform/messaging"
	"github.com/joshbarros/golang-pledge/internal/platform/pgdb"
)

// Schema and consumer names.
const (
	Schema       = "receipts"
	ConsumerName = "receipts"
)

// IssueOutcome is what issuing did.
type IssueOutcome int

// The ways issuing can end.
const (
	Issued IssueOutcome = iota
	Rejected
	Duplicate
)

// Service is the receipts module's logic.
type Service struct {
	Pool   *pgxpool.Pool
	Outbox messaging.Outbox
	Log    *slog.Logger
}

// NewService builds a Service with its outbox.
func NewService(pool *pgxpool.Pool, log *slog.Logger) *Service {
	return &Service{Pool: pool, Outbox: messaging.Outbox{Schema: Schema, Module: "receipts"}, Log: log}
}

// Handle implements messaging.Handler. A rejected receipt returns ErrPoison after its rejection is committed, so the
// message is dead-lettered instead of retried.
func (s *Service) Handle(ctx context.Context, d messaging.Delivery) error {
	if d.RoutingKey != messaging.HeldKey {
		return fmt.Errorf("receipts does not handle %q", d.RoutingKey)
	}
	held, err := messaging.Parse[messaging.MatchHeld](d.Body)
	if err != nil {
		return fmt.Errorf("%w: %v", messaging.ErrPoison, err)
	}
	outcome, err := s.Issue(ctx, d.MessageID, held)
	if err != nil {
		return err
	}
	if outcome == Rejected {
		return fmt.Errorf("%w: the reference carries the poison token; the rejection is committed", messaging.ErrPoison)
	}
	return nil
}

// Issue issues the receipt, or rejects it when the reference is poison, in one transaction with the inbox receipt.
func (s *Service) Issue(ctx context.Context, messageID uuid.UUID, held messaging.MatchHeld) (IssueOutcome, error) {
	outcome := Issued
	err := pgdb.Tx(ctx, s.Pool, func(tx pgx.Tx) error {
		first, err := s.Outbox.Claim(ctx, tx, ConsumerName, messageID)
		if err != nil {
			return err
		}
		if !first {
			outcome = Duplicate
			return nil
		}
		if messaging.IsPoison(held.Reference) {
			const reason = "Reference contains the poison token."
			outcome = Rejected
			if _, err := tx.Exec(ctx, `INSERT INTO receipts.receipts (donation_id, owner_id, status, reason, created_at) VALUES ($1,$2,'Rejected',$3,now())`,
				held.DonationID, held.OwnerID, reason); err != nil {
				return err
			}
			if err := s.Outbox.Enqueue(ctx, tx, messaging.ReleaseKey, held.DonationID, messaging.ReleaseMatch{DonationID: held.DonationID, Reason: "poison"}); err != nil {
				return err
			}
			return s.Outbox.Enqueue(ctx, tx, messaging.RejectedKey, held.DonationID, messaging.ReceiptRejected{DonationID: held.DonationID, Reason: reason})
		}
		id := strings.ReplaceAll(held.DonationID.String(), "-", "")
		number := "RC-" + strings.ToUpper(id[len(id)-12:])
		if _, err := tx.Exec(ctx, `INSERT INTO receipts.receipts (donation_id, owner_id, number, status, created_at) VALUES ($1,$2,$3,'Issued',now())`,
			held.DonationID, held.OwnerID, number); err != nil {
			return err
		}
		return s.Outbox.Enqueue(ctx, tx, messaging.IssuedKey, held.DonationID, messaging.ReceiptIssued{DonationID: held.DonationID, Number: number})
	})
	return outcome, err
}

// Receipt is the public view of a receipt.
type Receipt struct {
	DonationID uuid.UUID `json:"donationId"`
	Number     *string   `json:"number"`
	Status     string    `json:"status"`
	Reason     *string   `json:"reason"`
	CreatedAt  time.Time `json:"createdAt"`
}

// API is the receipts HTTP surface.
type API struct {
	Pool *pgxpool.Pool
	Auth httpx.Authenticator
	Log  *slog.Logger
}

// Routes registers GET /api/receipts/{donationId}: the owner or staff only; anyone else gets a 404.
func (a *API) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/receipts/{donationId}", httpx.Authenticated(a.Log, a.Auth, httpx.Wrap(a.Log, a.get)))
}

func (a *API) get(w http.ResponseWriter, r *http.Request) error {
	id, err := uuid.Parse(r.PathValue("donationId"))
	if err != nil {
		return httpx.Fail(http.StatusNotFound, "Not found.")
	}
	var c Receipt
	var owner uuid.UUID
	err = a.Pool.QueryRow(r.Context(), `SELECT donation_id, owner_id, number, status, reason, created_at FROM receipts.receipts WHERE donation_id = $1`, id).
		Scan(&c.DonationID, &owner, &c.Number, &c.Status, &c.Reason, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !httpx.PrincipalOf(r.Context()).CanAccess(owner)) {
		return httpx.Fail(http.StatusNotFound, "Not found.")
	}
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, c)
}

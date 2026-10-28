package donations

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joshbarros/golang-pledge/internal/platform/httpx"
	"github.com/joshbarros/golang-pledge/internal/platform/messaging"
	"github.com/joshbarros/golang-pledge/internal/platform/pgdb"
	"github.com/joshbarros/golang-pledge/internal/pools"
)

// Schema and consumer names.
const (
	Schema       = "donations"
	ConsumerName = "donations"
)

// Donation statuses.
const (
	Requested = "Requested"
	Held      = "Held"
	Completed = "Completed"
	Refunded  = "Refunded"
	Rejected  = "Rejected"
)

// Donation is the public view of a donation.
type Donation struct {
	ID              uuid.UUID  `json:"id"`
	OwnerID         uuid.UUID  `json:"ownerId"`
	PoolCode        string     `json:"poolCode"`
	Reference       string     `json:"reference"`
	AmountCents     int64      `json:"amountCents"`
	FeeCents        int64      `json:"feeCents"`
	MatchedCents    int64      `json:"matchedCents"`
	Status          string     `json:"status"`
	Reason          *string    `json:"reason"`
	ReceiptNumber   *string    `json:"receiptNumber"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
	MatchReleasedAt *time.Time `json:"matchReleasedAt"`
}

// PoolCatalog is the one thing Donations may ask the pools module.
type PoolCatalog interface {
	Find(ctx context.Context, code string) (*pools.Info, error)
}

// Service is the HTTP side of the saga: it writes the donation and its first outbox row in one local transaction.
type Service struct {
	Pool   *pgxpool.Pool
	Outbox messaging.Outbox
	Pools  PoolCatalog
	Log    *slog.Logger

	// FinalMarkDelay is a latency knob for the final mark: Kubernetes sets it so the refund race window is wide enough to hit on purpose.
	FinalMarkDelay time.Duration
}

// NewService builds a Service with its outbox.
func NewService(pool *pgxpool.Pool, catalog PoolCatalog, log *slog.Logger) *Service {
	return &Service{Pool: pool, Outbox: messaging.Outbox{Schema: Schema, Module: "donations"}, Pools: catalog, Log: log}
}

const donationColumns = `id, owner_id, pool_code, reference, amount_cents, fee_cents, matched_cents, status, reason, receipt_number, created_at, updated_at, match_released_at`

func scanDonation(row pgx.Row) (Donation, error) {
	var o Donation
	err := row.Scan(&o.ID, &o.OwnerID, &o.PoolCode, &o.Reference, &o.AmountCents, &o.FeeCents, &o.MatchedCents, &o.Status, &o.Reason, &o.ReceiptNumber, &o.CreatedAt, &o.UpdatedAt, &o.MatchReleasedAt)
	return o, err
}

// CreateRequest is the body of POST /api/donations.
type CreateRequest struct {
	PoolCode    string `json:"poolCode"`
	Reference   string `json:"reference"`
	AmountCents int64  `json:"amountCents"`
}

// Create sends a donation to the pool for the caller: validates, writes the donation and its reserve command in one transaction.
func (s *Service) Create(ctx context.Context, caller *httpx.Principal, req CreateRequest) (Donation, error) {
	if caller == nil {
		return Donation{}, httpx.Fail(http.StatusUnauthorized, "Sign in to send.")
	}
	if strings.TrimSpace(req.PoolCode) == "" {
		return Donation{}, httpx.Fail(http.StatusBadRequest, "Pool code is required.")
	}
	if msg := Validate(req.Reference, req.AmountCents); msg != "" {
		return Donation{}, httpx.Fail(http.StatusBadRequest, "%s", msg)
	}
	pool, err := s.Pools.Find(ctx, strings.TrimSpace(req.PoolCode))
	if err != nil {
		return Donation{}, err
	}
	if pool == nil {
		return Donation{}, httpx.Fail(http.StatusNotFound, "Pool '%s' does not exist.", req.PoolCode)
	}
	now := time.Now().UTC()
	reference := strings.TrimSpace(req.Reference)
	fee, err := Fee(req.AmountCents)
	if err != nil {
		return Donation{}, httpx.Fail(http.StatusBadRequest, "The amount is too large.")
	}
	id, _ := uuid.NewV7()
	o := Donation{ID: id, OwnerID: caller.ID, PoolCode: pool.Code, Reference: reference, AmountCents: req.AmountCents, FeeCents: fee, Status: Requested, CreatedAt: now, UpdatedAt: now}
	err = pgdb.Tx(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO donations.donations (id, owner_id, pool_id, pool_code, reference, amount_cents, fee_cents, status, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,'Requested',$8,$8)`, o.ID, o.OwnerID, pool.ID, o.PoolCode, o.Reference, o.AmountCents, o.FeeCents, now); err != nil {
			return err
		}
		return s.Outbox.Enqueue(ctx, tx, messaging.ReserveKey, o.ID,
			messaging.ReserveMatch{DonationID: o.ID, OwnerID: caller.ID, PoolID: pool.ID, AmountCents: req.AmountCents, Reference: reference})
	})
	if err != nil {
		if pgdb.IsUniqueViolation(err) {
			return Donation{}, httpx.Fail(http.StatusConflict, "Reference '%s' is already used on %s.", reference, pool.Code)
		}
		return Donation{}, err
	}
	return o, nil
}

// Get returns a donation to its owner or to staff. Anyone else gets a 404, so ids cannot be probed.
func (s *Service) Get(ctx context.Context, caller *httpx.Principal, id uuid.UUID) (Donation, error) {
	o, err := scanDonation(s.Pool.QueryRow(ctx, `SELECT `+donationColumns+` FROM donations.donations WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !caller.CanAccess(o.OwnerID)) {
		return Donation{}, httpx.Fail(http.StatusNotFound, "Donation not found.")
	}
	return o, err
}

// List returns the caller's donations (staff: everyone's), newest first, a page at a time, with the total.
func (s *Service) List(ctx context.Context, caller *httpx.Principal, poolCode string, page, pageSize int) ([]Donation, int, error) {
	if caller == nil {
		return nil, 0, httpx.Fail(http.StatusUnauthorized, "Sign in to see donations.")
	}
	where := []string{"true"}
	args := []any{}
	if !caller.IsStaff() {
		args = append(args, caller.ID)
		where = append(where, "owner_id = $"+strconv.Itoa(len(args)))
	}
	if strings.TrimSpace(poolCode) != "" {
		args = append(args, poolCode)
		where = append(where, "pool_code = $"+strconv.Itoa(len(args)))
	}
	cond := strings.Join(where, " AND ")
	var total int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM donations.donations WHERE `+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, pageSize, (page-1)*pageSize)
	rows, err := s.Pool.Query(ctx, fmt.Sprintf(`SELECT %s FROM donations.donations WHERE %s ORDER BY created_at DESC, id DESC LIMIT $%d OFFSET $%d`,
		donationColumns, cond, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []Donation{}
	for rows.Next() {
		o, err := scanDonation(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, o)
	}
	return items, total, rows.Err()
}

// Refund moves Held or Completed to Refunded and asks Pools to give the match back. The receipt is not touched.
func (s *Service) Refund(ctx context.Context, caller *httpx.Principal, id uuid.UUID) (Donation, error) {
	if _, err := s.Get(ctx, caller, id); err != nil { // 404 unless the caller may see it
		return Donation{}, err
	}
	err := pgdb.Tx(ctx, s.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE donations.donations SET status = 'Refunded', reason = 'Refunded by request.', updated_at = now()
			WHERE id = $1 AND status IN ('Held','Completed')`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			var current string
			if err := tx.QueryRow(ctx, `SELECT status FROM donations.donations WHERE id = $1`, id).Scan(&current); err != nil {
				return err
			}
			return httpx.Fail(http.StatusConflict, "A donation in status %s cannot be refunded.", current)
		}
		return s.Outbox.Enqueue(ctx, tx, messaging.ReleaseKey, id, messaging.ReleaseMatch{DonationID: id, Reason: "refund"})
	})
	if err != nil {
		return Donation{}, err
	}
	return scanDonation(s.Pool.QueryRow(ctx, `SELECT `+donationColumns+` FROM donations.donations WHERE id = $1`, id))
}

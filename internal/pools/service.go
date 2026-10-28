package pools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joshbarros/golang-pledge/internal/platform/messaging"
	"github.com/joshbarros/golang-pledge/internal/platform/pgdb"
)

// Schema and consumer names.
const (
	Schema       = "pools"
	ConsumerName = "pools"

	HoldHeld     = "Held"
	HoldReleased = "Released"
)

// ReserveOutcome says how much of the donation the pool matched.
type ReserveOutcome int

// Full: the whole donation was matched. Partial: the pool had less left. None: the pool was empty (the donation still goes through).
const (
	Full ReserveOutcome = iota
	Partial
	None
	Duplicate
)

// Service is the pools module's logic. It is the only writer of available_cents and of match holds.
type Service struct {
	Pool   *pgxpool.Pool
	Outbox messaging.Outbox
	Log    *slog.Logger
}

// NewService builds a Service with its outbox.
func NewService(pool *pgxpool.Pool, log *slog.Logger) *Service {
	return &Service{Pool: pool, Outbox: messaging.Outbox{Schema: Schema, Module: "pools"}, Log: log}
}

// Handle implements messaging.Handler for the pools queue.
func (s *Service) Handle(ctx context.Context, d messaging.Delivery) error {
	switch d.RoutingKey {
	case messaging.ReserveKey:
		cmd, err := messaging.Parse[messaging.ReserveMatch](d.Body)
		if err != nil {
			return fmt.Errorf("%w: %v", messaging.ErrPoison, err)
		}
		_, err = s.Reserve(ctx, d.MessageID, cmd)
		return err
	case messaging.ReleaseKey:
		cmd, err := messaging.Parse[messaging.ReleaseMatch](d.Body)
		if err != nil {
			return fmt.Errorf("%w: %v", messaging.ErrPoison, err)
		}
		_, err = s.Release(ctx, d.MessageID, cmd)
		return err
	}
	return fmt.Errorf("pools does not handle %q", d.RoutingKey)
}

// Reserve matches a donation with LEAST(available, donation) in one locking statement: the donation is matched in full, in
// part or not at all, never beyond what the pool has, and the donation itself always goes through. Two concurrent donations
// queue on the row lock, so together they can never take more than the pool had.
func (s *Service) Reserve(ctx context.Context, messageID uuid.UUID, cmd messaging.ReserveMatch) (ReserveOutcome, error) {
	outcome := Full
	err := pgdb.Tx(ctx, s.Pool, func(tx pgx.Tx) error {
		first, err := s.Outbox.Claim(ctx, tx, ConsumerName, messageID)
		if err != nil {
			return err
		}
		if !first {
			outcome = Duplicate
			return nil
		}
		var matched int64
		err = tx.QueryRow(ctx, `
			WITH cur AS (SELECT id, available_cents FROM pools.pools WHERE id = $1 FOR UPDATE)
			UPDATE pools.pools p SET available_cents = p.available_cents - LEAST(cur.available_cents, $2)
			FROM cur WHERE p.id = cur.id
			RETURNING LEAST(cur.available_cents, $2)`, cmd.PoolID, cmd.AmountCents).Scan(&matched)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO pools.match_holds (donation_id, pool_id, matched_cents, status, created_at) VALUES ($1,$2,$3,'Held',now())`,
			cmd.DonationID, cmd.PoolID, matched); err != nil {
			return err
		}
		switch {
		case matched == cmd.AmountCents:
			outcome = Full
		case matched > 0:
			outcome = Partial
		default:
			outcome = None
		}
		return s.Outbox.Enqueue(ctx, tx, messaging.HeldKey, cmd.DonationID,
			messaging.MatchHeld{DonationID: cmd.DonationID, OwnerID: cmd.OwnerID, PoolID: cmd.PoolID, MatchedCents: matched, Reference: cmd.Reference})
	})
	return outcome, err
}

// Release gives held match back exactly once. A second release for the same donation changes nothing.
func (s *Service) Release(ctx context.Context, messageID uuid.UUID, cmd messaging.ReleaseMatch) (bool, error) {
	released := false
	err := pgdb.Tx(ctx, s.Pool, func(tx pgx.Tx) error {
		first, err := s.Outbox.Claim(ctx, tx, ConsumerName, messageID)
		if err != nil || !first {
			return err
		}
		var poolID uuid.UUID
		var matched int64
		err = tx.QueryRow(ctx, `
			UPDATE pools.match_holds SET status = 'Released', released_at = now(), release_reason = $2
			WHERE donation_id = $1 AND status = 'Held' RETURNING pool_id, matched_cents`, cmd.DonationID, cmd.Reason).Scan(&poolID, &matched)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // nothing held, or already released
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE pools.pools SET available_cents = available_cents + $2 WHERE id = $1 AND available_cents + $2 <= opening_cents`, poolID, matched); err != nil {
			return err
		}
		released = true
		return s.Outbox.Enqueue(ctx, tx, messaging.ReleasedKey, cmd.DonationID, messaging.MatchReleased{DonationID: cmd.DonationID, MatchedCents: matched, Reason: cmd.Reason})
	})
	return released, err
}

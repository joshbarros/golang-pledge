package donations

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/joshbarros/golang-pledge/internal/platform/messaging"
	"github.com/joshbarros/golang-pledge/internal/platform/pgdb"
)

// MarkOutcome is what a mark did.
type MarkOutcome int

// The ways a mark can end.
const (
	Marked MarkOutcome = iota
	Skipped
	Duplicate
)

// Handle consumes the events that move a donation forward. Each step runs only from the status it expects.
func (s *Service) Handle(ctx context.Context, d messaging.Delivery) error {
	poison := func(err error) error { return fmt.Errorf("%w: %v", messaging.ErrPoison, err) }
	switch d.RoutingKey {
	case messaging.HeldKey:
		m, err := messaging.Parse[messaging.MatchHeld](d.Body)
		if err != nil {
			return poison(err)
		}
		_, err = s.MarkHeld(ctx, d.MessageID, m)
		return err
	case messaging.IssuedKey:
		m, err := messaging.Parse[messaging.ReceiptIssued](d.Body)
		if err != nil {
			return poison(err)
		}
		_, err = s.MarkCompleted(ctx, d.MessageID, m, s.FinalMarkDelay)
		return err
	case messaging.RejectedKey:
		m, err := messaging.Parse[messaging.ReceiptRejected](d.Body)
		if err != nil {
			return poison(err)
		}
		_, err = s.MarkRejected(ctx, d.MessageID, m)
		return err
	case messaging.ReleasedKey:
		m, err := messaging.Parse[messaging.MatchReleased](d.Body)
		if err != nil {
			return poison(err)
		}
		_, err = s.MarkReleased(ctx, d.MessageID, m)
		return err
	}
	return fmt.Errorf("donations does not handle %q", d.RoutingKey)
}

// step runs update inside one transaction with the inbox receipt. When update changes nothing and the donation is still waiting
// for an earlier mark (Requested), the message arrived too early: it is retried, not dropped.
func (s *Service) step(ctx context.Context, messageID uuid.UUID, donationForDonationCheck *uuid.UUID, update func(pgx.Tx) (int64, error)) (MarkOutcome, error) {
	outcome := Marked
	err := pgdb.Tx(ctx, s.Pool, func(tx pgx.Tx) error {
		first, err := s.Outbox.Claim(ctx, tx, ConsumerName, messageID)
		if err != nil {
			return err
		}
		if !first {
			outcome = Duplicate
			return nil
		}
		changed, err := update(tx)
		if err != nil {
			return err
		}
		if changed == 0 && donationForDonationCheck != nil {
			var status string
			err := tx.QueryRow(ctx, `SELECT status FROM donations.donations WHERE id = $1`, *donationForDonationCheck).Scan(&status)
			if err == nil && status == Requested {
				return &messaging.OutOfOrderError{Reason: "The final mark arrived before the Held mark."}
			}
		}
		if changed == 0 {
			outcome = Skipped
		}
		return nil
	})
	return outcome, err
}

// MarkHeld: the pool matched the donation (in full, in part or not at all) and holds that match.
func (s *Service) MarkHeld(ctx context.Context, messageID uuid.UUID, m messaging.MatchHeld) (MarkOutcome, error) {
	return s.step(ctx, messageID, nil, func(tx pgx.Tx) (int64, error) {
		tag, err := tx.Exec(ctx, `UPDATE donations.donations SET status = 'Held', matched_cents = $2, reason = NULL, updated_at = now() WHERE id = $1 AND status = 'Requested'`, m.DonationID, m.MatchedCents)
		return tag.RowsAffected(), err
	})
}

// MarkCompleted is the final mark. If a refund got there first the row is no longer Held and nothing changes.
func (s *Service) MarkCompleted(ctx context.Context, messageID uuid.UUID, m messaging.ReceiptIssued, delay time.Duration) (MarkOutcome, error) {
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return Skipped, ctx.Err()
		}
	}
	return s.step(ctx, messageID, &m.DonationID, func(tx pgx.Tx) (int64, error) {
		tag, err := tx.Exec(ctx, `UPDATE donations.donations SET status = 'Completed', receipt_number = $2, updated_at = now() WHERE id = $1 AND status = 'Held'`, m.DonationID, m.Number)
		return tag.RowsAffected(), err
	})
}

// MarkRejected: the receipt was refused (poison).
func (s *Service) MarkRejected(ctx context.Context, messageID uuid.UUID, m messaging.ReceiptRejected) (MarkOutcome, error) {
	return s.step(ctx, messageID, nil, func(tx pgx.Tx) (int64, error) {
		tag, err := tx.Exec(ctx, `UPDATE donations.donations SET status = 'Rejected', reason = $2, updated_at = now() WHERE id = $1 AND status IN ('Held','Requested')`, m.DonationID, m.Reason)
		return tag.RowsAffected(), err
	})
}

// MarkReleased records that the match came back.
func (s *Service) MarkReleased(ctx context.Context, messageID uuid.UUID, m messaging.MatchReleased) (MarkOutcome, error) {
	return s.step(ctx, messageID, nil, func(tx pgx.Tx) (int64, error) {
		tag, err := tx.Exec(ctx, `UPDATE donations.donations SET match_released_at = now() WHERE id = $1 AND match_released_at IS NULL`, m.DonationID)
		return tag.RowsAffected(), err
	})
}

var _ = errors.New

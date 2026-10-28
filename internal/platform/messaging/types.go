// Package messaging is the transactional outbox, the inbox, and the RabbitMQ and Kafka plumbing around them.
package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Names of the exchanges, the Kafka topic and the routing keys. They are the contract between the modules.
const (
	Exchange        = "pledge.work"
	DeadLetterExch  = "pledge.dlx"
	RequeueExchange = "pledge.requeue"
	KafkaTopic      = "pledge.events"
	RetryDelayMs    = 2000
	MaxAttempts     = 3
	ReserveKey      = "match.reserve"
	ReleaseKey      = "match.release"
	HeldKey         = "match.held"
	ReleasedKey     = "match.released"
	IssuedKey       = "receipt.issued"
	RejectedKey     = "receipt.rejected"
	PoolsQueue      = "pools.commands"
	ReceiptsQueue   = "receipts.work"
	DonationsQueue  = "donations.events"
	PoisonToken     = "POISON"
	attemptHeader   = "x-attempt"
	outboxBatchSize = 50
	relayIdleDelay  = 200 * time.Millisecond
	relayErrorDelay = time.Second
	trimEvery       = 10 * time.Minute
)

// Delivery is one message handed to a Handler.
type Delivery struct {
	MessageID  uuid.UUID
	RoutingKey string
	Body       []byte
}

// Handler consumes one message. Returning ErrPoison dead-letters it at once; any other error retries it.
type Handler interface {
	Handle(ctx context.Context, d Delivery) error
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(ctx context.Context, d Delivery) error

// Handle implements Handler.
func (f HandlerFunc) Handle(ctx context.Context, d Delivery) error { return f(ctx, d) }

// ErrPoison marks a message whose rejection is already committed: it must be dead-lettered, not retried.
var ErrPoison = errors.New("poison message")

// OutOfOrderError means the message arrived before the state it depends on; it is retried later.
type OutOfOrderError struct{ Reason string }

func (e *OutOfOrderError) Error() string { return e.Reason }

// IsPoison reports whether the reference carries the poison token (a case-sensitive substring).
func IsPoison(reference string) bool { return strings.Contains(reference, PoisonToken) }

// FailureAction is what a consumer does with a message whose handler failed.
type FailureAction int

// The two things a failed message can meet.
const (
	ActionRetry FailureAction = iota
	ActionDeadLetter
)

// Decide: a poison message is dead-lettered at once; anything else is retried until it has been tried MaxAttempts times.
func Decide(priorAttempts int, err error) FailureAction {
	if errors.Is(err, ErrPoison) {
		return ActionDeadLetter
	}
	if priorAttempts+1 >= MaxAttempts {
		return ActionDeadLetter
	}
	return ActionRetry
}

// ---- the messages ----

// ReserveMatch asks the pool to match a donation.
type ReserveMatch struct {
	DonationID  uuid.UUID `json:"donationId"`
	OwnerID     uuid.UUID `json:"ownerId"`
	PoolID      uuid.UUID `json:"poolId"`
	AmountCents int64     `json:"amountCents"`
	Reference   string    `json:"reference"`
}

// MatchHeld says the pool matched the donation (possibly in part, possibly with nothing) and holds that match.
type MatchHeld struct {
	DonationID   uuid.UUID `json:"donationId"`
	OwnerID      uuid.UUID `json:"ownerId"`
	PoolID       uuid.UUID `json:"poolId"`
	MatchedCents int64     `json:"matchedCents"`
	Reference    string    `json:"reference"`
}

// ReleaseMatch asks the pool to give a donation's match back.
type ReleaseMatch struct {
	DonationID uuid.UUID `json:"donationId"`
	Reason     string    `json:"reason"`
}

// MatchReleased says the match (possibly nothing) was given back.
type MatchReleased struct {
	DonationID   uuid.UUID `json:"donationId"`
	MatchedCents int64     `json:"matchedCents"`
	Reason       string    `json:"reason"`
}

// ReceiptIssued says the receipt exists.
type ReceiptIssued struct {
	DonationID uuid.UUID `json:"donationId"`
	Number     string    `json:"number"`
}

// ReceiptRejected says the receipt was refused (the poison rule).
type ReceiptRejected struct {
	DonationID uuid.UUID `json:"donationId"`
	Reason     string    `json:"reason"`
}

// Parse decodes a message body.
func Parse[T any](body []byte) (T, error) {
	var v T
	if err := json.Unmarshal(body, &v); err != nil {
		return v, err
	}
	return v, nil
}

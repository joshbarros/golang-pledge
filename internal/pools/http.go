package pools

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joshbarros/golang-pledge/internal/platform/httpx"
)

// MaxOpeningCents is $100,000,000.00.
const MaxOpeningCents = 10_000_000_000

// Pool is the public view of a matching pool.
type Pool struct {
	ID             uuid.UUID `json:"id"`
	Code           string    `json:"code"`
	Sponsor        string    `json:"sponsor"`
	Currency       string    `json:"currency"`
	Cause          string    `json:"cause"`
	OpenedAt       time.Time `json:"openedAt"`
	OpeningCents   int64     `json:"openingCents"`
	AvailableCents int64     `json:"availableCents"`
}

// Info is what other modules may ask the pools module (Donations never reads this schema).
type Info struct {
	ID   uuid.UUID
	Code string
}

// Catalog answers the one cross-module read: a pool's existence.
type Catalog struct{ Pool *pgxpool.Pool }

// Find looks a pool up by code. It returns nil when there is none.
func (c Catalog) Find(ctx context.Context, code string) (*Info, error) {
	var i Info
	err := c.Pool.QueryRow(ctx, `SELECT id, code FROM pools.pools WHERE code = $1`, code).Scan(&i.ID, &i.Code)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &i, err
}

// API is the pools HTTP surface.
type API struct {
	Pool *pgxpool.Pool
	Auth httpx.Authenticator
	Log  *slog.Logger
}

// Routes registers the pool endpoints; every one needs a signed-in user.
func (a *API) Routes(mux *http.ServeMux) {
	authed := func(h httpx.HandlerFunc) http.HandlerFunc {
		return httpx.Authenticated(a.Log, a.Auth, httpx.Wrap(a.Log, h))
	}
	mux.HandleFunc("GET /api/pools", authed(a.list))
	mux.HandleFunc("GET /api/pools/{code}", authed(a.get))
	mux.HandleFunc("POST /api/pools", httpx.Authenticated(a.Log, a.Auth, httpx.Staff(httpx.Wrap(a.Log, a.create))))
}

const poolSelect = `SELECT id, code, sponsor, currency, cause, opened_at, opening_cents, available_cents FROM pools.pools`

func scanPool(row pgx.Row) (Pool, error) {
	var p Pool
	err := row.Scan(&p.ID, &p.Code, &p.Sponsor, &p.Currency, &p.Cause, &p.OpenedAt, &p.OpeningCents, &p.AvailableCents)
	return p, err
}

func (a *API) list(w http.ResponseWriter, r *http.Request) error {
	rows, err := a.Pool.Query(r.Context(), poolSelect+` ORDER BY opened_at, code`)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []Pool{}
	for rows.Next() {
		p, err := scanPool(rows)
		if err != nil {
			return err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, out)
}

func (a *API) get(w http.ResponseWriter, r *http.Request) error {
	p, err := scanPool(a.Pool.QueryRow(r.Context(), poolSelect+` WHERE code = $1`, r.PathValue("code")))
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.Fail(http.StatusNotFound, "Not found.")
	}
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, p)
}

type createRequest struct {
	Code         string     `json:"code"`
	Sponsor      string     `json:"sponsor"`
	Currency     string     `json:"currency"`
	Cause        string     `json:"cause"`
	OpenedAt     *time.Time `json:"openedAt"`
	OpeningCents int64      `json:"openingCents"`
}

// create opens a pool. Staff only. This is the only place available_cents is initialised; after that only the reserve and
// release handlers write it.
func (a *API) create(w http.ResponseWriter, r *http.Request) error {
	var req createRequest
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	code := strings.TrimSpace(req.Code)
	if code == "" || len(code) > 20 {
		return httpx.Fail(http.StatusBadRequest, "Code is required and at most 20 characters.")
	}
	if strings.TrimSpace(req.Sponsor) == "" || strings.TrimSpace(req.Currency) == "" || strings.TrimSpace(req.Cause) == "" {
		return httpx.Fail(http.StatusBadRequest, "Sponsor, currency and cause are required.")
	}
	if req.OpeningCents < 1 || req.OpeningCents > MaxOpeningCents {
		return httpx.Fail(http.StatusBadRequest, "Opening balance must be between 1 and %d cents.", int64(MaxOpeningCents))
	}
	opened := time.Now().AddDate(0, 0, 14)
	if req.OpenedAt != nil {
		opened = *req.OpenedAt
	}
	id, _ := uuid.NewV7()
	_, err := a.Pool.Exec(r.Context(), `
		INSERT INTO pools.pools (id, code, sponsor, currency, cause, opened_at, opening_cents, available_cents, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$7,now())`,
		id, code, strings.TrimSpace(req.Sponsor), strings.TrimSpace(req.Currency), strings.TrimSpace(req.Cause), opened, req.OpeningCents)
	if err != nil {
		var coder interface{ SQLState() string }
		if errors.As(err, &coder) && coder.SQLState() == "23505" {
			return httpx.Fail(http.StatusConflict, "Pool '%s' already exists.", code)
		}
		return err
	}
	p, err := scanPool(a.Pool.QueryRow(r.Context(), poolSelect+` WHERE id = $1`, id))
	if err != nil {
		return err
	}
	w.Header().Set("Location", "/api/pools/"+code)
	return httpx.JSON(w, http.StatusCreated, p)
}

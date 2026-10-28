package donations

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/joshbarros/golang-pledge/internal/platform/httpx"
	"github.com/joshbarros/golang-pledge/internal/platform/messaging"
)

// API is the donations HTTP surface.
type API struct {
	Svc   *Service
	Kafka *messaging.Kafka // nil when messaging is off
	Auth  httpx.Authenticator
	Log   *slog.Logger
}

// Routes registers the donation endpoints; every one needs a signed-in user.
func (a *API) Routes(mux *http.ServeMux) {
	authed := func(h httpx.HandlerFunc) http.HandlerFunc {
		return httpx.Authenticated(a.Log, a.Auth, httpx.Wrap(a.Log, h))
	}
	mux.HandleFunc("POST /api/donations", authed(a.create))
	mux.HandleFunc("GET /api/donations", authed(a.list))
	mux.HandleFunc("GET /api/donations/{id}", authed(a.get))
	mux.HandleFunc("POST /api/donations/{id}/refund", authed(a.refund))
	mux.HandleFunc("GET /api/donations/{id}/events", httpx.Authenticated(a.Log, a.Auth, httpx.Staff(httpx.Wrap(a.Log, a.events))))
}

func donationID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return uuid.Nil, httpx.Fail(http.StatusNotFound, "Donation not found.")
	}
	return id, nil
}

// create accepts the donation for the signed-in user. Pools and the receipt follow asynchronously; poll GET to see them.
func (a *API) create(w http.ResponseWriter, r *http.Request) error {
	var req CreateRequest
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	o, err := a.Svc.Create(r.Context(), httpx.PrincipalOf(r.Context()), req)
	if err != nil {
		return err
	}
	w.Header().Set("Location", "/api/donations/"+o.ID.String())
	return httpx.JSON(w, http.StatusAccepted, o)
}

// list returns your donations, newest first, a page at a time (default 100, at most 100). Staff see everyone's. The total is in X-Total-Count.
func (a *API) list(w http.ResponseWriter, r *http.Request) error {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	size := 100
	if v, err := strconv.Atoi(r.URL.Query().Get("pageSize")); err == nil {
		size = min(max(v, 1), 100)
	}
	items, total, err := a.Svc.List(r.Context(), httpx.PrincipalOf(r.Context()), r.URL.Query().Get("poolCode"), page, size)
	if err != nil {
		return err
	}
	w.Header().Set("X-Total-Count", strconv.Itoa(total))
	return httpx.JSON(w, http.StatusOK, items)
}

func (a *API) get(w http.ResponseWriter, r *http.Request) error {
	id, err := donationID(r)
	if err != nil {
		return err
	}
	o, err := a.Svc.Get(r.Context(), httpx.PrincipalOf(r.Context()), id)
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, o)
}

func (a *API) refund(w http.ResponseWriter, r *http.Request) error {
	id, err := donationID(r)
	if err != nil {
		return err
	}
	o, err := a.Svc.Refund(r.Context(), httpx.PrincipalOf(r.Context()), id)
	if err != nil {
		return err
	}
	w.Header().Set("Location", "/api/donations/"+id.String())
	return httpx.JSON(w, http.StatusAccepted, o)
}

// events is the Kafka event log for one donation, read from the start of the topic. Staff only.
func (a *API) events(w http.ResponseWriter, r *http.Request) error {
	id, err := donationID(r)
	if err != nil {
		return err
	}
	if a.Kafka == nil {
		return httpx.Fail(http.StatusServiceUnavailable, "Messaging is switched off in this process.")
	}
	values, err := a.Kafka.Read(r.Context(), id.String(), 10*time.Second)
	if err != nil {
		return err
	}
	out := make([]json.RawMessage, 0, len(values))
	for _, v := range values {
		out = append(out, json.RawMessage(v))
	}
	return httpx.JSON(w, http.StatusOK, out)
}

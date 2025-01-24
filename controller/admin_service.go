package controller

import (
	"billing3/database"
	"billing3/database/types"
	"billing3/service"
	"billing3/service/extension"
	"errors"
	"fmt"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"
	"github.com/shopspring/decimal"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"strconv"
	"time"
)

func adminServiceList(w http.ResponseWriter, r *http.Request) {
	page, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || page < 1 {
		page = 1
	}

	userId, err := strconv.Atoi(r.URL.Query().Get("user_id"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	serverId, err := strconv.Atoi(r.URL.Query().Get("server_id"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	status := r.URL.Query().Get("status")
	label := r.URL.Query().Get("label")

	count, err := database.Q.CountServicesPaged(r.Context(), database.CountServicesPagedParams{
		Label: label,
		// Server: int32(serverId),
		UserID: int32(userId),
		Status: status,
	})
	if err != nil {
		slog.Error("admin list services", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	totalPages := int(math.Ceil(float64(count) / float64(itemPerPage)))

	services, err := database.Q.SearchServicesPaged(r.Context(), database.SearchServicesPagedParams{
		Limit:  itemPerPage,
		Offset: int32((page - 1) * itemPerPage),
		Label:  label,
		Server: int32(serverId),
		UserID: int32(userId),
		Status: status,
	})
	if err != nil {
		slog.Error("admin list services", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	writeResp(w, http.StatusOK, D{"services": services, "total_pages": totalPages})
}

func adminServiceGet(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	s, err := database.Q.FindServiceByIdWithName(r.Context(), int32(id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		slog.Error("admin get service", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	writeResp(w, http.StatusOK, D{"service": s})
}

func adminServiceUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	type reqStruct struct {
		Label        string          `json:"label" validate:"required"`
		BillingCycle int             `json:"billing_cycle" validate:"min=1"`
		Price        decimal.Decimal `json:"price"`
		ExpiresAt    types.Timestamp `json:"expires_at" validate:"required"`
	}
	req, err := decode[reqStruct](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// validation

	if !req.ExpiresAt.Valid {
		writeError(w, http.StatusBadRequest, "ExpiresAt is required")
		return
	}
	if req.Price.LessThan(decimal.Zero) {
		writeError(w, http.StatusBadRequest, "Price must not be negative")
		return
	}

	err = database.Q.UpdateService(r.Context(), database.UpdateServiceParams{
		Label:        req.Label,
		BillingCycle: int32(req.BillingCycle),
		Price:        req.Price,
		ExpiresAt:    req.ExpiresAt,
		ID:           int32(id),
	})
	if err != nil {
		slog.Error("admin update service", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	writeResp(w, http.StatusOK, D{})
}

func adminServiceGenerateInvoice(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	tx, err := database.Conn.Begin(r.Context())
	if err != nil {
		slog.Error("begin tx", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer rollbackTx(r.Context(), tx)

	qtx := database.New(tx)
	invoiceId, err := service.CreateRenewalInvoice(r.Context(), qtx, int32(id), decimal.NewFromInt(0))
	if err != nil {
		if errors.Is(err, service.ErrServiceCancelled) || errors.Is(err, service.ErrUnpaidInvoiceExists) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		slog.Error("create invoice", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	err = tx.Commit(r.Context())
	if err != nil {
		slog.Error("commit tx", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	writeResp(w, http.StatusOK, D{"invoice": invoiceId})
}

func adminServiceActions(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	_, actions, err := service.ServiceAdminActions(r.Context(), int32(id))
	if err != nil {
		if errors.Is(err, service.ErrInternalError) {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeResp(w, http.StatusOK, D{"actions": actions})
}

func adminServicePerformAction(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	type reqStruct struct {
		Action string `json:"action" validate:"required"`
	}
	req, err := decode[reqStruct](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	s, actions, err := service.ServiceAdminActions(r.Context(), int32(id))
	if err != nil {
		if errors.Is(err, service.ErrInternalError) {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if !slices.Contains(actions, req.Action) {
		writeError(w, http.StatusBadRequest, "Invalid action")
		return
	}

	slog.Info("service action", "extension", s.Extension, "service id", s.ID, "action", req.Action, "label", s.Label, "status", s.Status)
	err = extension.DoActionAsync(r.Context(), s.Extension, s.ID, req.Action, "")
	if err != nil {
		slog.Error("service action", "err", err, "extension", s.Extension, "service id", s.ID, "action", req.Action, "label", s.Label, "status", s.Status)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeResp(w, http.StatusOK, D{})
}

func adminServiceInfoPage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	s, err := database.Q.FindServiceById(r.Context(), int32(id))
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		slog.Error("find service", "err", err)
		return
	}

	ext, ok := extension.Extensions[s.Extension]
	if !ok {
		writeError(w, http.StatusInternalServerError, "extension \""+s.Extension+"\" not found")
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	err = ext.AdminPage(w, s.ID)
	if err != nil {
		slog.Error("service admin page", "err", err, "service id", s.ID, "extension", s.Extension)
	}
}

func adminServiceUpdateStatus(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	type reqStruct struct {
		Status             string      `json:"status" validate:"required,oneof=PENDING ACTIVE CANCELLED SUSPENDED UNPAID"`
		Action             bool        `json:"action"`
		CancellationReason pgtype.Text `json:"cancellation_reason"`
	}

	req, err := decode[reqStruct](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	s, err := database.Q.FindServiceById(r.Context(), int32(id))
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		slog.Error("find service", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// mark the service as cancelled
	if req.Status == "CANCELLED" {
		slog.Info("admin cancel service", "service id", s.ID)

		req.CancellationReason.Valid = true

		// update cancellation reason and cancel time
		err := database.Q.UpdateServiceCancelled(r.Context(), database.UpdateServiceCancelledParams{
			CancellationReason: req.CancellationReason,
			CancelledAt:        types.Timestamp{Timestamp: pgtype.Timestamp{Time: time.Now(), Valid: true}},
			ID:                 s.ID,
		})
		if err != nil {
			slog.Error("admin cancel service", "err", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
	}

	// performing the corresponding action
	if req.Action && (req.Status == "SUSPENDED" || req.Status == "CANCELLED" || req.Status == "ACTIVE") {

		action := ""
		switch req.Status {
		case "SUSPENDED":
			action = "suspend"
		case "CANCELLED":
			action = "terminate"
		case "ACTIVE":
			action = "create"
		default:
			panic("unreachable")
		}
		err = extension.DoActionAsync(r.Context(), s.Extension, s.ID, action, req.Status)
		if err != nil {
			slog.Error("service action", "err", err, "extension", s.Extension, "service id", s.ID, "action", req.Action, "label", s.Label, "status", s.Status)
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		writeResp(w, http.StatusOK, D{})

	} else {

		err := database.Q.UpdateServiceStatus(r.Context(), database.UpdateServiceStatusParams{
			Status: req.Status,
			ID:     s.ID,
		})
		if err != nil {
			slog.Error("admin update status", "err", err)
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

	}
}

func adminServiceAction(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	resp := database.RedisClient.Get(r.Context(), fmt.Sprintf("service_%d_action_lock", id))
	if resp.Err() != nil && !errors.Is(resp.Err(), redis.Nil) {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	pending := ""
	if !errors.Is(resp.Err(), redis.Nil) {
		pending = resp.Val()
	}

	resp = database.RedisClient.GetDel(r.Context(), fmt.Sprintf("service_%d_action_info", id))
	if resp.Err() != nil && !errors.Is(resp.Err(), redis.Nil) {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	info := ""
	if !errors.Is(resp.Err(), redis.Nil) {
		info = resp.Val()
	}

	resp = database.RedisClient.GetDel(r.Context(), fmt.Sprintf("service_%d_action_error", id))
	if resp.Err() != nil && !errors.Is(resp.Err(), redis.Nil) {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	actionError := ""
	if !errors.Is(resp.Err(), redis.Nil) {
		actionError = resp.Val()
	}

	writeResp(w, http.StatusOK, D{
		"pending":      pending,
		"info":         info,
		"action_error": actionError,
	})
}

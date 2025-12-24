package controller

import (
	"billing3/database"
	"billing3/database/types"
	"billing3/service"
	"billing3/service/extension"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/riverqueue/river"
	"github.com/shopspring/decimal"
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

func adminServiceUpdateSettings(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	req, err := decode[types.ServiceSettings](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	err = database.Q.UpdateServiceSettings(r.Context(), database.UpdateServiceSettingsParams{
		ID:       int32(id),
		Settings: *req,
	})
	if err != nil {
		slog.Error("update service settings", "err", err)
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

func adminServiceGetJobs(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	params := river.NewJobListParams().
		OrderBy(river.JobListOrderByID, river.SortOrderDesc).
		Kinds("extension_action").
		Metadata("{\"service_id\": " + strconv.Itoa(id) + "}")

	jobsListResp, err := database.River.JobList(r.Context(), params)
	if err != nil {
		slog.Error("admin get service jobs", "err", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	type jobRespStruct struct {
		ID          int64      `json:"id"`
		Kind        string     `json:"kind"`
		State       string     `json:"state"`
		ScheduledAt time.Time  `json:"scheduled_at"`
		AttemptedAt *time.Time `json:"attempted_at"`
		FinalizedAt *time.Time `json:"finalized_at"`
		Args        string     `json:"args"`
		Error       string     `json:"error"`
	}

	var jobsResp = make([]jobRespStruct, 0)
	for _, job := range jobsListResp.Jobs {
		jobsResp = append(jobsResp, jobRespStruct{
			ID:          job.ID,
			Kind:        job.Kind,
			State:       string(job.State),
			ScheduledAt: job.ScheduledAt,
			AttemptedAt: job.AttemptedAt,
			FinalizedAt: job.FinalizedAt,
			Args:        string(job.EncodedArgs),
			Error:       "",
		})

		for _, e := range job.Errors {
			jobsResp[len(jobsResp)-1].Error += e.Error + "; "
		}
	}

	writeResp(w, http.StatusOK, D{"jobs": jobsResp})
}

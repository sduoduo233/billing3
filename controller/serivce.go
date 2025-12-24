package controller

import (
	"billing3/controller/middlewares"
	"billing3/database"
	"billing3/database/types"
	"billing3/service"
	"billing3/service/extension"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/riverqueue/river"
	"github.com/shopspring/decimal"
)

func getServices(w http.ResponseWriter, r *http.Request) {
	user := middlewares.MustGetUser(r)

	services, err := database.Q.FindServiceByUser(r.Context(), user.ID)
	if err != nil {
		slog.Error("get services", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	type respStruct struct {
		ID                 int32           `json:"id"`
		Label              string          `json:"label"`
		UserID             int32           `json:"user_id"`
		Status             string          `json:"status"`
		CancellationReason pgtype.Text     `json:"cancellation_reason"`
		BillingCycle       int32           `json:"billing_cycle"`
		Price              decimal.Decimal `json:"price"`
		ExpiresAt          types.Timestamp `json:"expires_at"`
		CreatedAt          types.Timestamp `json:"created_at"`
		CancelledAt        types.Timestamp `json:"cancelled_at"`
	}

	resp := make([]respStruct, len(services))
	for i, s := range services {
		resp[i] = respStruct{
			ID:                 s.ID,
			Label:              s.Label,
			UserID:             s.UserID,
			Status:             s.Status,
			CancellationReason: s.CancellationReason,
			BillingCycle:       s.BillingCycle,
			Price:              s.Price,
			ExpiresAt:          s.ExpiresAt,
			CreatedAt:          s.CreatedAt,
			CancelledAt:        s.CancelledAt,
		}
	}
	writeResp(w, http.StatusOK, D{"services": resp})
}

func getService(w http.ResponseWriter, r *http.Request) {
	user := middlewares.MustGetUser(r)

	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	s, err := database.Q.FindServiceById(r.Context(), int32(id))
	if err != nil {
		slog.Error("get s", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	if s.UserID != user.ID {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	writeResp(w, http.StatusOK, D{"service": D{
		"id":                  s.ID,
		"label":               s.Label,
		"user_id":             s.UserID,
		"status":              s.Status,
		"cancellation_reason": s.CancellationReason,
		"billing_cycle":       s.BillingCycle,
		"price":               s.Price,
		"expires_at":          s.ExpiresAt,
		"created_at":          s.CreatedAt,
		"cancelled_at":        s.CancelledAt,
	}})
}

func serviceClientActions(w http.ResponseWriter, r *http.Request) {
	user := middlewares.MustGetUser(r)
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	s, err := database.Q.FindServiceById(r.Context(), int32(id))
	if err != nil {
		slog.Error("get service", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	if s.UserID != user.ID {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	if s.Status != service.ServiceActive {
		writeError(w, http.StatusBadRequest, "service is not active")
		return
	}

	actions, err := service.ServiceClientActions(r.Context(), s.ID)
	if err != nil {
		slog.Error("service client actions", "err", err, "id", id)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	writeResp(w, http.StatusOK, D{"actions": actions})
}

func serviceInfoPage(w http.ResponseWriter, r *http.Request) {
	user := middlewares.MustGetUser(r)
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	s, err := database.Q.FindServiceById(r.Context(), int32(id))
	if err != nil {
		slog.Error("get service", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	if s.UserID != user.ID {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	if s.Status != service.ServiceActive {
		writeError(w, http.StatusBadRequest, "service is not active")
		return
	}

	ext, ok := extension.Extensions[s.Extension]
	if !ok {
		writeError(w, http.StatusInternalServerError, "extension \""+s.Extension+"\" not found")
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	err = ext.ClientPage(w, s.ID)
	if err != nil {
		slog.Error("service admin page", "err", err, "service id", s.ID, "extension", s.Extension)
	}
}

func servicePerformAction(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	user := middlewares.MustGetUser(r)

	s, err := database.Q.FindServiceById(r.Context(), int32(id))
	if err != nil {
		slog.Error("get service", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	if s.UserID != user.ID {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	if s.Status != service.ServiceActive {
		writeError(w, http.StatusBadRequest, "service is not active")
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

	actions, err := service.ServiceClientActions(r.Context(), int32(id))
	if err != nil {
		slog.Error("service perform action", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	if !slices.Contains(actions, req.Action) {
		writeError(w, http.StatusBadRequest, "invalid action")
		return
	}

	slog.Info("service client action", "id", id, "action", req.Action, "user id", user.ID)

	err = extension.DoActionAsync(r.Context(), s.Extension, s.ID, req.Action, "")
	if err != nil {
		if errors.Is(err, extension.ErrActionRunning) {
			writeError(w, http.StatusInternalServerError, "another action is running")
			return
		}
		slog.Error("service client do action async", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	writeResp(w, http.StatusOK, D{})
}

func serviceGetJobs(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	user := middlewares.MustGetUser(r)

	s, err := database.Q.FindServiceById(r.Context(), int32(id))
	if err != nil {
		slog.Error("get service", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	if s.UserID != user.ID {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	if s.Status != service.ServiceActive {
		writeError(w, http.StatusBadRequest, "service is not active")
		return
	}

	params := river.NewJobListParams().
		First(10).
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
		FinalizedAt *time.Time `json:"finalized_at"`
		Action      string     `json:"action"`
	}

	type arg struct {
		Action string `json:"action"`
	}

	var jobsResp = make([]jobRespStruct, 0)
	for _, job := range jobsListResp.Jobs {
		jobsResp = append(jobsResp, jobRespStruct{
			ID:          job.ID,
			Kind:        job.Kind,
			State:       string(job.State),
			ScheduledAt: job.ScheduledAt,
			FinalizedAt: job.FinalizedAt,
		})
		var a arg
		err := json.Unmarshal(job.EncodedArgs, &a)
		if err == nil {
			jobsResp[len(jobsResp)-1].Action = a.Action
		}
	}

	writeResp(w, http.StatusOK, D{"jobs": jobsResp})
}

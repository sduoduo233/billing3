package controller

import (
	"billing3/controller/middlewares"
	"billing3/database"
	"billing3/database/types"
	"billing3/service"
	"billing3/service/gateways"
	"errors"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func getInvoice(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	user := middlewares.MustGetUser(r)

	invoice, err := database.Q.FindInvoiceById(r.Context(), int32(id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		slog.Error("get invoice", "err", err)
		return
	}

	if invoice.UserID != user.ID {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	items, err := database.Q.ListInvoiceItems(r.Context(), invoice.ID)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		slog.Error("get invoice", "err", err)
		return
	}

	writeResp(w, http.StatusOK, D{"invoice": invoice, "items": items})
}

func listInvoices(w http.ResponseWriter, r *http.Request) {
	user := middlewares.MustGetUser(r)

	page, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || page < 1 {
		page = 1
	}

	totalPages, invoices, err := service.SearchInvoice(r.Context(), "", int(user.ID), page, itemPerPage)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		slog.Error("get invoice", "err", err)
		return
	}

	writeResp(w, http.StatusOK, D{"total_pages": totalPages, "invoices": invoices})
}

func getAvailablePaymentGateways(w http.ResponseWriter, r *http.Request) {
	dbGateways, err := database.Q.ListEnabledGateways(r.Context())
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		slog.Error("get available gateways", "err", err)
		return
	}

	writeResp(w, http.StatusOK, D{"gateways": dbGateways})
}

func makePayment(w http.ResponseWriter, r *http.Request) {
	user := middlewares.MustGetUser(r)

	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	// check whether the gateway exists
	type reqStruct struct {
		Gateway string `json:"gateway"`
	}
	req, err := decode[reqStruct](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	gatewayName := req.Gateway

	gateway, ok := gateways.Gateways[gatewayName]
	if !ok {
		writeError(w, http.StatusBadRequest, "gateway not found")
		return
	}

	dbGateway, err := database.Q.FindGatewayByName(r.Context(), gatewayName)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusBadRequest, "gateway not found")
			return
		}
		slog.Error("make payment", "err", err)
		return
	}

	// gateway must be enabled and fee must not be null
	if !dbGateway.Enabled || !dbGateway.Fee.Valid {
		w.WriteHeader(http.StatusForbidden)
		return
	}

	invoice, err := database.Q.FindInvoiceById(r.Context(), int32(id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		slog.Error("make payment", "err", err)
		return
	}

	// user must own the invoice
	if invoice.UserID != user.ID {
		w.WriteHeader(http.StatusForbidden)
		return
	}

	// invoice must be unpaid and not overdue
	if invoice.Status != service.InvoiceUnpaid || invoice.DueAt.Time.Before(time.Now()) {
		writeError(w, http.StatusBadRequest, "invoice is not payable")
		return
	}

	// calculate total amount
	total := invoice.Amount
	percentage := strings.HasSuffix(dbGateway.Fee.String, "%")
	fee, err := decimal.NewFromString(strings.TrimSuffix(dbGateway.Fee.String, "%"))
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		slog.Error("invalid gateway fee", "err", err, "fee", fee, "gateway", gatewayName)
		return
	}

	if !percentage {
		total = total.Add(fee)
	} else {
		total = total.Add(total.Mul(fee.Div(decimal.NewFromInt(100))))
	}
	total = total.RoundUp(2)

	// start payment
	paymentUrl, err := gateway.Pay(&invoice, user, total)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		slog.Error("make payment", "err", err, "gateway", gatewayName, "invoice_id", invoice.ID, "total", total.String(), "user_id", user.ID)
		return
	}

	slog.Info("payment start", "invoice_id", invoice.ID, "total", total.String(), "user_id", user.ID, "payment_url", paymentUrl, "gateway", gatewayName, "gateway_fee", dbGateway.Fee.String)

	writeResp(w, http.StatusOK, D{"payment_url": paymentUrl})
}

func getInvoicePayments(w http.ResponseWriter, r *http.Request) {
	user := middlewares.MustGetUser(r)

	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	invoice, err := database.Q.FindInvoiceById(r.Context(), int32(id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		slog.Error("get invoice payments", "err", err)
		return
	}

	if user.ID != invoice.UserID {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	type payment struct {
		Id          int32           `json:"id"`
		Gateway     string          `json:"gateway"`
		Description string          `json:"description"`
		Amount      string          `json:"amount"`
		CreatedAt   types.Timestamp `json:"created_at"`
	}

	dbPayments, err := database.Q.ListInvoicePayments(r.Context(), invoice.ID)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		slog.Error("get invoice payments", "err", err)
		return
	}

	payments := make([]payment, 0)
	for _, p := range dbPayments {
		payments = append(payments, payment{
			Id:          p.ID,
			Gateway:     p.Gateway,
			Description: p.Description,
			Amount:      p.Amount.String(),
			CreatedAt:   p.CreatedAt,
		})
	}

	writeResp(w, http.StatusOK, D{"payments": payments})
}

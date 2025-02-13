package service

import (
	"billing3/database"
	"billing3/database/types"
	"billing3/service/extension"
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/shopspring/decimal"
	"log/slog"
	"math"
	"slices"
	"time"
)

const (
	InvoiceUnpaid    = "UNPAID"
	InvoicePaid      = "PAID"
	InvoiceCancelled = "CANCELLED"

	InvoiceItemService = "service"
	InvoiceItemNone    = ""
)

// SearchInvoice returns a list of invoice matching the searching criteria.
//
// status, userId is ignored if it has empty value
func SearchInvoice(ctx context.Context, status string, userId int, page int, itemPerPage int32) (int, []database.Invoice, error) {
	totalCount, err := database.Q.SearchInvoicesCount(ctx, database.SearchInvoicesCountParams{
		Status: status,
		UserID: int32(userId),
	})
	if err != nil {
		return 0, nil, err
	}

	totalPages := int(math.Ceil(float64(totalCount) / float64(itemPerPage)))

	invoices, err := database.Q.SearchInvoicesPaged(ctx, database.SearchInvoicesPagedParams{
		Limit:  itemPerPage,
		Offset: int32(page-1) * itemPerPage,
		Status: status,
		UserID: int32(userId),
	})
	if err != nil {
		return 0, nil, err
	}

	return totalPages, invoices, nil
}

// CreateRenewalInvoice creates a renewal invoice for the service, using the price stored
// in the service model.
//
// Setup fee is added if setupFee is positive. Setup fee must not be negative.
//
// qtx should be a transaction. qtx is not commited.
func CreateRenewalInvoice(ctx context.Context, qtx *database.Queries, serviceId int32, setupFee decimal.Decimal) (int32, error) {
	if setupFee.LessThan(decimal.Zero) {
		return 0, fmt.Errorf("setup fee must not be negative")
	}

	slog.Debug("begin create renewal invoice", "service", serviceId, "setup fee", setupFee)

	// lock the service row
	service, err := qtx.FindServiceByIdForUpdate(ctx, serviceId)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, fmt.Errorf("service %d does not exist", serviceId)
		}
		return 0, fmt.Errorf("find service: %w", err)
	}

	// service must not be cancelled
	if service.Status == ServiceCancelled {
		slog.Debug("abort create renewal invoice", "err", ErrServiceCancelled, "service", serviceId)
		return 0, ErrServiceCancelled
	}

	// there must not be unpaid invoice for the service
	existingInvoices, err := qtx.CountUnpaidInvoiceForService(ctx, pgtype.Int4{Valid: true, Int32: serviceId})
	if err != nil {
		return 0, fmt.Errorf("count unpaid invoices: %w", err)
	}

	if existingInvoices > 0 {
		slog.Debug("abort create renewal invoice", "err", ErrUnpaidInvoiceExists, "service", serviceId)
		return 0, ErrUnpaidInvoiceExists
	}

	slog.Debug("create renewal invoice pass", "service", serviceId)

	// create the invoice
	invoiceId, err := qtx.CreateInvoice(ctx, database.CreateInvoiceParams{
		UserID:             service.UserID,
		Status:             InvoiceUnpaid,
		CancellationReason: pgtype.Text{Valid: false},
		PaidAt:             types.Timestamp{Timestamp: pgtype.Timestamp{Valid: false}},
		DueAt:              types.Timestamp{Timestamp: pgtype.Timestamp{Valid: true, Time: time.Now().UTC().Add(time.Hour * 168)}},
		Amount:             decimal.Sum(service.Price, setupFee),
	})
	if err != nil {
		return 0, fmt.Errorf("create invoice: %w", err)
	}

	// create invoice items
	description := fmt.Sprintf(
		"#%d - %s (%s - %s)",
		serviceId,
		service.Label,
		service.ExpiresAt.Time.Format("2006-01-02 MST"),
		service.ExpiresAt.Time.Add(time.Duration(service.BillingCycle)*time.Second).Format("2006-01-02 MST"),
	)
	err = qtx.CreateInvoiceItem(ctx, database.CreateInvoiceItemParams{
		InvoiceID:   invoiceId,
		Description: description,
		Amount:      service.Price,
		Type:        InvoiceItemService,
		ItemID:      pgtype.Int4{Valid: true, Int32: serviceId},
	})
	if err != nil {
		return 0, fmt.Errorf("create invoice item: %w", err)
	}

	// create invoice item for setup fee
	if setupFee.GreaterThan(decimal.Zero) {
		err = qtx.CreateInvoiceItem(ctx, database.CreateInvoiceItemParams{
			InvoiceID:   invoiceId,
			Description: fmt.Sprintf("#%d - %s - Setup Fee", serviceId, service.Label),
			Amount:      setupFee,
			Type:        InvoiceItemNone,
			ItemID:      pgtype.Int4{Valid: false},
		})
		if err != nil {
			return 0, fmt.Errorf("create invoice item: %w", err)
		}
	}

	slog.Info("create renewal invoice", "service", serviceId, "setup fee", setupFee, "service price", service.Price, "user", service.UserID, "label", service.Label, "service status", service.Status, "service expire", service.ExpiresAt.Time, "service billing cycle", service.BillingCycle)

	return invoiceId, nil
}

// InvoiceAddPayment adds payment to invoice. The invoice is marked as PAID if total payment exceeds invoice amount.
func InvoiceAddPayment(ctx context.Context, invoiceId int32, description string, amount decimal.Decimal, referenceId string, gateway string) error {
	slog.Info("add payment", "invoice_id", invoiceId, "description", description, "amount", amount, "reference_id", referenceId, "gateway", gateway)

	invoice, err := database.Q.FindInvoiceById(ctx, invoiceId)
	if err != nil {
		return fmt.Errorf("db: %w", err)
	}

	invoiceAmount := invoice.Amount

	_, err = database.Q.AddInvoicePayment(ctx, database.AddInvoicePaymentParams{
		InvoiceID:   invoiceId,
		Description: description,
		Amount:      amount,
		ReferenceID: referenceId,
		Gateway:     gateway,
	})
	if err != nil {
		return fmt.Errorf("db: %w", err)
	}

	totalPayment, err := database.Q.TotalInvoicePayment(ctx, invoiceId)
	if err != nil {
		return fmt.Errorf("db: %w", err)
	}

	// mark the invoice is PAID if the invoice is UNPAID and not overdue
	if totalPayment.GreaterThanOrEqual(invoiceAmount) && invoice.Status == "UNPAID" && invoice.DueAt.Time.After(time.Now()) {
		err := database.Q.UpdateInvoicePaid(ctx, invoiceId)
		if err != nil {
			return fmt.Errorf("db: %w", err)
		}
		slog.Info("updated invoice paid", "invoice_id", invoiceId, "amount", amount.String(), "total_payment", totalPayment.String())

		OnInvoicePaid(invoiceId)
	}

	return nil
}

// OnInvoicePaid does the following things to services in the invoice:
// - extend expiry date by billing cycle
// - mark the service as PENDING if the service is previously UNPAID
// - call the extension's create action if possible
func OnInvoicePaid(invoiceId int32) {
	slog.Info("on invoice paid", "invoice_id", invoiceId)

	ctx := context.Background()

	tx, err := database.Conn.Begin(ctx)
	if err != nil {
		slog.Error("begin tx", "err", err)
		return
	}
	defer tx.Rollback(ctx)

	invoice, err := database.Q.SelectInvoiceForUpdate(ctx, invoiceId)
	if err != nil {
		slog.Error("on invoice paid", "err", err)
		return
	}

	if invoice.Status != InvoicePaid {
		return
	}

	items, err := database.Q.ListInvoiceItems(ctx, invoiceId)
	if err != nil {
		slog.Error("on invoice paid", "err", err)
		return
	}

	for _, item := range items {
		if item.Type == InvoiceItemService && item.ItemID.Valid {

			itemId := item.ItemID.Int32
			slog.Info("on invoice paid", "invoice_id", invoiceId, "service_id", itemId, "description", item.Description)

			s, err := database.Q.FindServiceById(ctx, itemId)
			if err != nil {
				slog.Error("on invoice paid", "err", err)
				continue
			}

			if s.Status == ServiceCancelled {
				continue
			}

			// extend expiry time when invoice is paid
			slog.Info("extend service expiry time", "service_id", itemId, "expires_at", s.ExpiresAt, "billing_cycle", s.BillingCycle)

			expiryTime := s.ExpiresAt.Time.Add(time.Duration(s.BillingCycle) * time.Second)
			err = database.Q.UpdateServiceExpiryTime(ctx, database.UpdateServiceExpiryTimeParams{
				ID:        itemId,
				ExpiresAt: types.Timestamp{Timestamp: pgtype.Timestamp{Valid: true, Time: expiryTime}},
			})
			if err != nil {
				slog.Error("on invoice paid", "err", err)
				continue
			}

			// change status to PAID
			if s.Status == ServiceUnpaid {
				slog.Info("service pending", "service_id", itemId)
				err = database.Q.UpdateServiceStatus(ctx, database.UpdateServiceStatusParams{
					Status: ServicePending,
					ID:     itemId,
				})
				if err != nil {
					slog.Error("on invoice paid", "err", err)
					continue
				}
			}

			// create service
			ext, ok := extension.Extensions[s.Extension]
			if !ok {
				slog.Error("invalid extension", "service id", itemId, "extension", s.Extension)
				continue
			}

			actions, err := ext.AdminActions(itemId)
			if err != nil {
				slog.Error("extension admin actions", "err", err, "service_id", itemId, "extension", s.Extension)
				continue
			}

			if !slices.Contains(actions, "create") {
				continue
			}

			slog.Info("create service", "service_id", itemId)

			err = extension.DoActionAsync(ctx, s.Extension, itemId, "create", ServiceActive)
			if err != nil {
				slog.Error("do action async", "err", err)
			}
		}
	}

	err = tx.Commit(ctx)
	if err != nil {
		slog.Error("commit tx", "err", err)
		return
	}
}

func CloseOverdueInvoices() error {
	ctx := context.Background()
	invoices, err := database.Q.FindOverdueInvoices(ctx)
	if err != nil {
		return fmt.Errorf("db: %w", err)
	}

	for _, invoice := range invoices {
		slog.Info("cancel overdue invoice", "id", invoice.ID)

		// cancel the invoice
		err := database.Q.UpdateInvoiceCancelled(ctx, database.UpdateInvoiceCancelledParams{
			CancellationReason: pgtype.Text{Valid: true, String: "overdue"},
			ID:                 invoice.ID,
		})
		if err != nil {
			return fmt.Errorf("db: %w", err)
		}

		// cancel the service if the service is unpaid
		items, err := database.Q.ListInvoiceItems(ctx, invoice.ID)
		if err != nil {
			return fmt.Errorf("db: %w", err)
		}

		for _, item := range items {
			if item.Type == InvoiceItemService && item.ItemID.Valid {

				service, err := database.Q.FindServiceById(ctx, item.ID)
				if err != nil {
					return fmt.Errorf("db: %w", err)
				}

				if service.Status != ServiceUnpaid {
					continue
				}

				slog.Info("cancel overdue unpaid service", "id", item.ID, "invoice id", invoice.ID)

				err = database.Q.UpdateServiceCancelled(ctx, database.UpdateServiceCancelledParams{
					CancellationReason: pgtype.Text{Valid: true, String: "invoice overdue"},
					ID:                 item.ItemID.Int32,
					CancelledAt:        types.Timestamp{Timestamp: pgtype.Timestamp{Valid: true, Time: time.Now()}},
				})
				if err != nil {
					return fmt.Errorf("db: %w", err)
				}
			}
		}

	}

	return nil
}

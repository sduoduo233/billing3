package service

import (
	"billing3/database"
	"billing3/service/extension"
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"log/slog"
)

const (
	ServicePending   = "PENDING"
	ServiceActive    = "ACTIVE"
	ServiceCancelled = "CANCELLED"
	ServiceSuspended = "SUSPENDED"
	ServiceUnpaid    = "UNPAID"
)

// ServiceAdminActions returns a list of action that can be preformed on this service, by an admin
func ServiceAdminActions(ctx context.Context, serviceId int32) (*database.Service, []string, error) {
	s, err := database.Q.FindServiceById(ctx, serviceId)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, ErrNotFound
		}
		slog.Error("service actions", "err", err)
		return nil, nil, ErrInternalError
	}

	if s.Status == ServiceCancelled {
		return nil, nil, ErrServiceCancelled
	}

	ext, ok := extension.Extensions[s.Extension]
	if !ok {
		slog.Error("extension not found", "service id", serviceId, "extension", s.Extension)
		return nil, nil, ErrInternalError
	}

	actions, err := ext.AdminActions(s.ID)
	if err != nil {
		slog.Error("service actions", "err", err, "service id", serviceId)
		return nil, nil, ErrInternalError
	}

	return &s, actions, nil
}

// ServiceClientActions returns a list of action that can be preformed on this service, by a client
func ServiceClientActions(ctx context.Context, serviceId int32) ([]string, error) {
	s, err := database.Q.FindServiceById(ctx, serviceId)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		slog.Error("service actions", "err", err)
		return nil, ErrInternalError
	}

	if s.Status == ServiceCancelled {
		return nil, ErrServiceCancelled
	}

	ext, ok := extension.Extensions[s.Extension]
	if !ok {
		slog.Error("extension not found", "service id", serviceId, "extension", s.Extension)
		return nil, ErrNotFound
	}

	actions, err := ext.ClientActions(s.ID)
	if err != nil {
		slog.Error("service actions", "err", err, "service id", serviceId)
		return nil, ErrInternalError
	}

	return actions, nil
}

// CancelOverdueServices terminates overdue services
func CancelOverdueServices() error {
	ctx := context.Background()

	services, err := database.Q.FindOverdueServices(ctx)
	if err != nil {
		return fmt.Errorf("db: %w", err)
	}

	for _, service := range services {
		slog.Info("terminate overdue service", "id", service.ID)

		err := extension.DoActionAsync(ctx, service.Extension, service.ID, "terminate", ServiceCancelled)
		if err != nil {
			return fmt.Errorf("db: %w", err)
		}
	}

	return nil
}

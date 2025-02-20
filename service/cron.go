package service

import (
	"billing3/database"
	"billing3/utils"
	"context"
	"time"
)

func InitCron() {
	utils.NewCronJob(time.Hour, func() error {
		return CloseOverdueInvoices()
	}, "close overdue invoices")

	utils.NewCronJob(time.Hour, func() error {
		return CancelOverdueServices()
	}, "cancel overdue services")

	utils.NewCronJob(time.Hour, func() error {
		return database.Q.DeleteExpiredSessions(context.Background())
	}, "delete expired sessions")
}

package service

import (
	"billing3/utils"
	"time"
)

func InitCron() {
	utils.NewCronJob(time.Hour, func() error {
		return CloseOverdueInvoices()
	}, "close overdue invoices")

	utils.NewCronJob(time.Hour, func() error {
		return CancelOverdueServices()
	}, "cancel overdue services")
}

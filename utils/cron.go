package utils

import (
	"log/slog"
	"sync"
	"time"
)

var cronJobs []*CronJob
var initCronJobOnce sync.Once

type CronJob struct {
	lastRun  time.Time
	duration time.Duration
	name     string
	fn       func() error
}

func NewCronJob(d time.Duration, fn func() error, name string) *CronJob {
	slog.Info("new cron job", "name", name)

	initCronJobOnce.Do(initCronJob)

	j := &CronJob{
		lastRun:  time.Unix(0, 0),
		name:     name,
		fn:       fn,
		duration: d,
	}
	cronJobs = append(cronJobs, j)
	return j
}

func initCronJob() {
	go func() {
		time.Sleep(time.Second * 5)

		for _, j := range cronJobs {
			if j.lastRun.Add(j.duration).Before(time.Now()) {
				slog.Info("cron job", "name", j.name)

				j.lastRun = time.Now()
				go func() {
					err := j.fn()
					if err != nil {
						slog.Error("cron job", "err", err, "name", j.name)
					}
				}()

			}
		}
	}()
}

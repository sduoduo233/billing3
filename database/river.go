package database

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

var (
	River   *river.Client[pgx.Tx] = nil
	Workers                       = river.NewWorkers()
)

func InitRiver() {

	var err error
	River, err = river.NewClient(riverpgxv5.New(Conn), &river.Config{
		Queues: map[string]river.QueueConfig{
			river.QueueDefault: {MaxWorkers: 2},
		},
		Workers:    Workers,
		JobTimeout: time.Minute * 5,
	})
	if err != nil {
		slog.Error("init river", "err", err)
		panic(err)
	}

	if err := River.Start(context.Background()); err != nil {
		slog.Error("start river", "err", err)
		panic(err)
	}
}

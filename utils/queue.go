package utils

import (
	"billing3/database"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/redis/go-redis/v9"
	"log/slog"
	"time"
)

type WorkerFunc func(args json.RawMessage) error

type Queue struct {
	name   string
	ctx    context.Context
	cancel context.CancelFunc
	worker WorkerFunc
}

type Task struct {
	CreatedAt   int64           `json:"created_at"`
	Args        json.RawMessage `json:"args"`
	Description string          `json:"description"`
	Attempts    int             `json:"attempts"`
}

// NewQueue returns a new queue and starts n worker goroutines.
func NewQueue(name string, n int, worker WorkerFunc) *Queue {

	ctx, cancel := context.WithCancel(context.Background())
	q := &Queue{
		name:   name,
		ctx:    ctx,
		cancel: cancel,
		worker: worker,
	}
	for range n {
		go q.doWork()
	}
	return q
}

func (q *Queue) Name() string {
	return q.name
}

func (q *Queue) doWork() {
	defer slog.Debug("redis worker stopped", "name", q.name)
	slog.Debug("redis worker started", "name", q.name)

	for {
		// dequeue

		resp := database.RedisClient.BRPop(q.ctx, time.Second*10, "queue_"+q.name)

		if resp.Err() != nil {
			if errors.Is(resp.Err(), context.Canceled) {
				slog.Debug("redis worker cancelled", "name", q.name)
				return
			} else if errors.Is(resp.Err(), redis.Nil) {
				continue
			} else {
				slog.Error("redis dequeue", "err", resp.Err(), "name", q.name)
				return
			}
		}

		result, err := resp.Result()
		if err != nil {
			slog.Error("redis dequeue", "err", err, "name", q.name)
			continue
		}

		// run the task

		var task Task
		err = json.Unmarshal([]byte(result[1]), &task)
		if err != nil {
			slog.Error("redis dequeue decode json", "err", err, "name", q.name)
			continue
		}

		task.Attempts -= 1

		slog.Info("do work", "name", q.name, "created at", task.CreatedAt, "description", task.Description)

		err = q.worker(task.Args)
		if err != nil {
			slog.Error("work failed", "err", err, "name", q.name, "created_at", task.CreatedAt, "description", task.Description)

			if task.Attempts > 0 {
				slog.Info("retry enqueue", "name", q.name, "description", task.Description, "attempts", task.Attempts)

				err := q.Enqueue(context.Background(), task.Description, task.Args, task.Attempts)
				if err != nil {
					slog.Error("retry enqueue failed", "err", err, "name", q.name, "description", task.Description, "attempts", task.Attempts)
					continue
				}
			}
		}
	}
}

// Enqueue enqueues a task. v must be a json serializable.
func (q *Queue) Enqueue(ctx context.Context, description string, args json.RawMessage, attempts int) error {
	task := Task{
		CreatedAt:   time.Now().Unix(),
		Args:        args,
		Description: description,
		Attempts:    attempts,
	}

	taskJson, err := json.Marshal(&task)
	if err != nil {
		return fmt.Errorf("redis enqueue: json encode: %w", err)
	}

	err = database.RedisClient.LPush(ctx, "queue_"+q.name, taskJson).Err()
	if err != nil {
		return fmt.Errorf("redis enqueue: %w", err)
	}
	return nil
}

func (q *Queue) Shutdown() {
	q.cancel()
}

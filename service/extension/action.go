package extension

import (
	"billing3/database"
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

type ExtensionActionArgs struct {
	ServiceId int32  `json:"service_id" river:"unique"`
	Action    string `json:"action"`
	NewStatus string `json:"new_status"`
	Extension string `json:"extension"`
}

func (ExtensionActionArgs) Kind() string { return "extension_action" }

// InsertOpts returns custom insert options that every job of this type will
// inherit, including unique options.
func (ExtensionActionArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		UniqueOpts: river.UniqueOpts{
			ByArgs: true,
			ByState: []rivertype.JobState{
				rivertype.JobStateAvailable,
				rivertype.JobStatePending,
				rivertype.JobStateRunning,
				rivertype.JobStateRetryable,
				rivertype.JobStateScheduled,
			},
		},
	}
}

type ExtensionActionWorker struct {
	// An embedded WorkerDefaults sets up default methods to fulfill the rest of
	// the Worker interface:
	river.WorkerDefaults[ExtensionActionArgs]
}

func (w *ExtensionActionWorker) Work(ctx context.Context, job *river.Job[ExtensionActionArgs]) error {
	slog.Info("extension action work start", "service_id", job.Args.ServiceId, "action", job.Args.Action, "new_status", job.Args.NewStatus)

	// find the extension

	ext, ok := Extensions[job.Args.Extension]
	if !ok {
		return fmt.Errorf("extension %s not found", job.Args.Extension)
	}

	// perform the action

	err := ext.Action(job.Args.ServiceId, job.Args.Action)
	if err != nil {
		slog.Error("extension action failed", "action", job.Args.Action, "service id", job.Args.ServiceId, "new status", job.Args.NewStatus, "err", err)
		return fmt.Errorf("action %s on service #%d failed: %w", job.Args.Action, job.Args.ServiceId, err)
	}

	// update the service status if needed

	if job.Args.NewStatus != "" {
		err = database.Q.UpdateServiceStatus(ctx, database.UpdateServiceStatusParams{
			Status: job.Args.NewStatus,
			ID:     job.Args.ServiceId,
		})
		if err != nil {
			slog.Error("update service status", "err", err, "id", job.Args.ServiceId, "status", job.Args.NewStatus)
		}

		slog.Info("extension action set new status", "service id", job.Args.ServiceId, "new status", job.Args.NewStatus)
	}

	slog.Info("extension action done", "service_id", job.Args.ServiceId, "action", job.Args.Action)

	return nil
}

var ErrActionRunning = errors.New("another action is running for this service")

// DoActionAsync enqueues a task that executes the action, and change the status of the service to new status if
// and only if the operation succeeds. ErrActionRunning is returned if the service already has a pending action.
func DoActionAsync(ctx context.Context, ext string, serviceId int32, action string, newStatus string) error {
	resp, err := database.River.Insert(ctx, ExtensionActionArgs{
		ServiceId: serviceId,
		Action:    action,
		NewStatus: newStatus,
		Extension: ext,
	}, &river.InsertOpts{
		MaxAttempts: 1,
		Metadata:    []byte(fmt.Sprintf("{\"service_id\": %d}", serviceId)),
	})
	if err != nil {
		return fmt.Errorf("insert job: %w", err)
	}
	if resp.UniqueSkippedAsDuplicate {
		return ErrActionRunning
	}
	return nil
}

func init() {
	river.AddWorker(database.Workers, &ExtensionActionWorker{})
}

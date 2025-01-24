package extension

import (
	"billing3/database"
	"billing3/utils"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/redis/go-redis/v9"
	"log/slog"
	"strings"
	"time"
)

var ErrActionRunning = errors.New("an action is already running")

type taskArgs struct {
	ServiceId int32  `json:"service_id"`
	NewStatus string `json:"new_status"`
	Action    string `json:"action"`
}

func initQueue(name string, extension Extension) *utils.Queue {
	return utils.NewQueue("extension_action_"+strings.ToLower(name), 2, func(rawArgs json.RawMessage) error {
		ctx := context.Background()

		var args taskArgs
		err := json.Unmarshal(rawArgs, &args)
		if err != nil {
			return fmt.Errorf("decode args: %w", err)
		}

		slog.Info("extension action", "action", args.Action, "service id", args.ServiceId, "new status", args.NewStatus)

		defer func() {
			// release lock
			resp := database.RedisClient.Del(ctx, fmt.Sprintf("service_%d_action_lock", args.ServiceId))
			if resp.Err() != nil {
				slog.Error("redis release lock", "err", resp.Err(), "service id", args.ServiceId)
			}
		}()

		// do action
		err = extension.Action(args.ServiceId, args.Action)
		if err != nil {
			slog.Error("extension action failed", "action", args.Action, "service id", args.ServiceId, "new status", args.NewStatus, "err", err)

			// flash message
			resp := database.RedisClient.Set(ctx, fmt.Sprintf("service_%d_action_error", args.ServiceId), fmt.Sprintf("Action %s failed", strings.ToUpper(args.Action)), time.Minute)
			if resp.Err() != nil {
				slog.Error("redis set", "err", err)
			}

			return fmt.Errorf("action %s: %w", args.Action, err)
		}

		if args.NewStatus != "" {
			err = database.Q.UpdateServiceStatus(ctx, database.UpdateServiceStatusParams{
				Status: args.NewStatus,
				ID:     args.ServiceId,
			})
			if err != nil {
				slog.Error("update service status", "err", err, "id", args.ServiceId, "status", args.NewStatus)
			}
		}

		// flash message
		resp := database.RedisClient.Set(ctx, fmt.Sprintf("service_%d_action_info", args.ServiceId), fmt.Sprintf("Action %s succeeded", strings.ToUpper(args.Action)), time.Minute)
		if resp.Err() != nil {
			slog.Error("redis set", "err", err)
		}

		return nil
	})
}

// DoActionAsync enqueues a task that executes the action, and change the status of the service to new status if
// and only if the operation succeeds. ErrActionRunning is returned if the service already has a pending action.
func DoActionAsync(ctx context.Context, ext string, serviceId int32, action string, newStatus string) error {
	q, ok := Queues[ext]
	if !ok {
		return fmt.Errorf("extension action %s not found", ext)
	}

	// acquire lock
	resp := database.RedisClient.SetArgs(ctx, fmt.Sprintf("service_%d_action_lock", serviceId), fmt.Sprintf("Pending action: %s", strings.ToUpper(action)), redis.SetArgs{
		Mode: "NX",
	})
	if errors.Is(resp.Err(), redis.Nil) {
		return ErrActionRunning
	}

	args := taskArgs{
		ServiceId: serviceId,
		NewStatus: newStatus,
		Action:    action,
	}

	j, err := json.Marshal(args)
	if err != nil {
		return fmt.Errorf("encode args: %w", err)
	}

	err = q.Enqueue(ctx, fmt.Sprintf("Extension action %s on service %d", action, serviceId), j, 1)
	if err != nil {
		return err
	}

	return nil
}

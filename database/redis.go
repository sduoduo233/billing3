package database

import (
	"github.com/redis/go-redis/v9"
	"log/slog"
	"os"
)

var RedisClient *redis.Client

func InitRedis() {
	url, err := redis.ParseURL(os.Getenv("REDIS"))
	if err != nil {
		slog.Error("redis init", "err", err)
		panic(err)
	}
	RedisClient = redis.NewClient(url)
}

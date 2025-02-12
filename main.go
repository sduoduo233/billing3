package main

import (
	"billing3/controller"
	"billing3/database"
	"billing3/service"
	"billing3/service/email"
	"billing3/service/extension"
	"billing3/service/gateways"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/joho/godotenv"
)

func main() {
	slog.Warn("billing3")

	err := godotenv.Load()
	if err != nil {
		slog.Warn("error loading .env file", "err", err)
	}

	// logging
	level := slog.LevelInfo
	if os.Getenv("DEBUG") == "true" {
		slog.Debug("debug logging enabled")
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		AddSource: true,
		Level:     &level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			//if a.Key == slog.SourceKey {
			//	source, _ := a.Value.Any().(*slog.Source)
			//	if source != nil {
			//		source.File = filepath.Base(source.File)
			//	}
			//}
			return a
		},
	})))

	// database
	database.Init()

	// redis
	database.InitRedis()

	// gateway
	err = gateways.InitDatabase()
	if err != nil {
		slog.Error("init gateway", "err", err)
		panic(err)
	}

	// extensions
	err = extension.Init()
	if err != nil {
		slog.Error("init extension", "err", err)
		panic(err)
	}

	// email
	service.InitJWT()
	email.Init()

	// cron jobs
	service.InitCron()

	// router
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(middleware.Logger)
	controller.Route(r)

	httpServer := http.Server{
		Handler: r,
		Addr:    ":3000",
	}
	go func() {
		err := httpServer.ListenAndServe()
		slog.Debug("http server closed", "err", err)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("serve http", "err", err)
			panic(err)
		}
	}()

	// wait for shutdown signals
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)

	sig := <-signalChan
	slog.Warn("shutting down", "sig", sig)

	// shutdown web server
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*30)
	defer cancel()
	err = httpServer.Shutdown(ctx)
	if err != nil {
		slog.Error("close http server", "err", err)
	}

	// close database connection
	ctx, cancel = context.WithTimeout(context.Background(), time.Second*30)
	defer cancel()
	database.Close()
	slog.Debug("close database")

}

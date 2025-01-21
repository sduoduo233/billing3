package database

import (
	"context"
	"embed"
	"fmt"
	"github.com/jackc/pgx/v5"
	"log/slog"
	"os"

	pgxdecimal "github.com/jackc/pgx-shopspring-decimal"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema
var sqls embed.FS

var Q *Queries
var Conn *pgxpool.Pool

func Init() {
	ctx := context.Background()

	config, err := pgxpool.ParseConfig(os.Getenv("DATABASE"))
	if err != nil {
		panic(fmt.Sprintf("pgx parse config: %s", err.Error()))
	}

	config.AfterConnect = func(ctx context.Context, p *pgx.Conn) error {
		pgxdecimal.Register(p.TypeMap())
		return nil
	}

	Conn, err = pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		panic(fmt.Sprintf("pgx connect: %s", err))
	}

	Q = New(Conn)

	// create tables

	bytes, err := sqls.ReadFile("schema/000-schema.sql")
	if err != nil {
		slog.Error("create tables", "err", err)
		panic(err)
	}

	_, err = Conn.Exec(context.Background(), string(bytes))
	if err != nil {
		slog.Error("create tables", "err", err)
		panic(err)
	}
}

func Close() {
	Conn.Close()
}

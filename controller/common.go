package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/go-playground/validator/v10"
)

const PGErrorUniqueViolation = "23505"

const itemPerPage = 20

var publicDomain string

type D map[string]any

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	type resp struct {
		Error string `json:"error"`
	}
	json.NewEncoder(w).Encode(resp{
		Error: msg,
	})
}

func writeResp(w http.ResponseWriter, code int, msg D) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	msg["ok"] = true
	json.NewEncoder(w).Encode(msg)
}

// decode decodes and validates request body to type T. Request body must be in JSON.
// Validation is skipped if T is a map.
func decode[T any](r *http.Request) (*T, error) {
	var t T

	if r.Header.Get("Content-Type") != "application/json" {
		return nil, fmt.Errorf("content type not application/json")
	}

	err := json.NewDecoder(r.Body).Decode(&t)
	if err != nil {
		slog.Debug("decode json", "err", err)
		return nil, fmt.Errorf("invalid json: %w", err)
	}

	// skip validation if t is a map
	// because validate.Struct does not accept map
	if reflect.ValueOf(t).Kind() == reflect.Map {
		return &t, nil
	}

	validate := validator.New(validator.WithRequiredStructEnabled())
	err = validate.Struct(&t)
	if err != nil {
		var invalidValidationErr *validator.InvalidValidationError
		if errors.As(err, &invalidValidationErr) {
			slog.Error("invalid validation", "err", invalidValidationErr)
			return nil, fmt.Errorf("invalid validation rrror")
		}

		sb := strings.Builder{}
		sb.WriteString("Invalid input: ")
		for _, err := range err.(validator.ValidationErrors) {
			if err.Tag() == "required" {
				sb.WriteString(err.Field())
				sb.WriteString(" is required")
			} else if err.Tag() == "oneof" {
				sb.WriteString(err.Field())
				sb.WriteString(" is invalid")
			} else {
				sb.WriteString(err.Field())
				sb.WriteString(" failed validation: ")
				sb.WriteString(err.Tag())
			}
			sb.WriteString("; ")
		}
		return nil, fmt.Errorf("%s", sb.String())
	}

	return &t, nil
}

func rollbackTx(ctx context.Context, tx pgx.Tx) {
	err := tx.Rollback(ctx)
	if err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		slog.Error("rollback tx", "err", err)
	}
}

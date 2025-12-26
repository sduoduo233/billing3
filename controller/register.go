package controller

import (
	"billing3/database"
	"billing3/service"
	"billing3/utils"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func register(w http.ResponseWriter, r *http.Request) {
	type reqStruct struct {
		Email string `json:"email" validate:"required,email"`
	}
	req, err := decode[reqStruct](r)
	if err != nil {
		writeError(w, http.StatusOK, err.Error())
		return
	}

	email := strings.ToLower(req.Email)

	_, err = database.Q.FindUserByEmail(r.Context(), email)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.Error("register", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	if err == nil {
		// user with same email already exists
		writeError(w, http.StatusOK, "Email already exists")
		return
	}

	err = service.SendVerificationEmail(r.Context(), email)
	if err != nil {
		slog.Error("register send verification email", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	writeResp(w, http.StatusOK, D{})
}

func registerStep2(w http.ResponseWriter, r *http.Request) {
	type reqStruct struct {
		Token    string `json:"token" validate:"required"`
		Name     string `json:"name" validate:"required,max=100"`
		Password string `json:"password" validate:"required,printascii,max=32"`
	}
	req, err := decode[reqStruct](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	email := service.DecodeEmailVerificationToken(req.Token)
	if email == "" {
		writeError(w, http.StatusForbidden, "Invalid token")
		return
	}

	slog.Info("register", "email", email, "name", req.Name)

	userId, err := database.Q.CreateUser(r.Context(), database.CreateUserParams{
		Email:    email,
		Name:     req.Name,
		Role:     "user",
		Password: utils.HashPassword(req.Password),
	})
	if err != nil {
		if err, ok := err.(*pgconn.PgError); ok && err.Code == PGErrorUniqueViolation {
			// user with same email already exists
			writeError(w, http.StatusForbidden, "Invalid token")
			return
		}
		slog.Error("create user", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	authToken, err := service.NewSessionToken(r.Context(), userId)
	if err != nil {
		slog.Error("register", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	writeResp(w, http.StatusOK, D{
		"token": authToken,
	})
}

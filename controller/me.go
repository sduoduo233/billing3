package controller

import (
	"billing3/controller/middlewares"
	"billing3/database"
	"fmt"
	"github.com/jackc/pgx/v5/pgtype"
	"log/slog"
	"net/http"
)

// returns the authenticated user
func me(w http.ResponseWriter, r *http.Request) {
	user := middlewares.MustGetUser(r)

	address := fmt.Sprintf("%s, %s, %s, %s, %s", user.Address.String, user.City.String, user.State.String, user.Country.String, user.ZipCode.String)
	if !user.Address.Valid && !user.City.Valid && !user.State.Valid && !user.Country.Valid && !user.ZipCode.Valid {
		address = "unknown"
	}

	writeResp(w, http.StatusOK, D{
		"email":        user.Email,
		"name":         user.Name,
		"role":         user.Role,
		"full_address": address,
		"address":      user.Address,
		"city":         user.City,
		"state":        user.State,
		"country":      user.Country,
		"zip_code":     user.ZipCode,
	})
}

func logout(w http.ResponseWriter, r *http.Request) {
	token := middlewares.MustGetToken(r)

	err := database.Q.DeleteSession(r.Context(), token)
	if err != nil {
		slog.Error("delete session", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	writeResp(w, http.StatusOK, D{})
}

func updateProfile(w http.ResponseWriter, r *http.Request) {
	user := middlewares.MustGetUser(r)

	type reqStruct struct {
		Name    string `json:"name" valid:"required"`
		Address string `json:"address"`
		City    string `json:"city"`
		State   string `json:"state"`
		Country string `json:"country"`
		ZipCode string `json:"zip_code"`
	}
	req, err := decode[reqStruct](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	err = database.Q.UpdateUserProfile(r.Context(), database.UpdateUserProfileParams{
		Name:    req.Name,
		Address: pgtype.Text{String: req.Address, Valid: req.Address != ""},
		City:    pgtype.Text{String: req.City, Valid: req.City != ""},
		State:   pgtype.Text{String: req.State, Valid: req.State != ""},
		Country: pgtype.Text{String: req.Country, Valid: req.Country != ""},
		ZipCode: pgtype.Text{String: req.ZipCode, Valid: req.ZipCode != ""},
		ID:      user.ID,
	})
	if err != nil {
		slog.Error("update user profile", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	writeResp(w, http.StatusOK, D{})
}

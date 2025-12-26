package controller

import (
	"billing3/database"
	"billing3/service"
	"billing3/service/email"
	"billing3/utils"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
)

func login(w http.ResponseWriter, r *http.Request) {
	type reqStruct struct {
		Email    string `json:"email" validate:"required"`
		Password string `json:"password" validate:"required"`
	}

	req, err := decode[reqStruct](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	user, err := database.Q.FindUserByEmail(r.Context(), req.Email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// user does not exist
			writeError(w, http.StatusOK, "Wrong email or password")
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		slog.Error("login", "err", err)
		return
	}

	if !utils.ComparePassword(user.Password, req.Password) {
		// wrong password
		writeError(w, http.StatusOK, "Wrong email or password")
		return
	}

	token, err := service.NewSessionToken(r.Context(), user.ID)
	if err != nil {
		slog.Error("login", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	writeResp(w, http.StatusOK, D{
		"token": token,
	})
}

func resetPassword(w http.ResponseWriter, r *http.Request) {
	type reqStruct struct {
		Email string `json:"email" validate:"required,email"`
	}
	req, err := decode[reqStruct](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	user, err := database.Q.FindUserByEmail(r.Context(), req.Email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// user does not exist, return 200 to avoid enumeration
			writeResp(w, http.StatusOK, D{})
			return
		}
		slog.Error("resetPassword", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	token := utils.JWTSign(jwt.MapClaims{
		"sub": strconv.Itoa(int(user.ID)),
		"aud": "reset_password",
	}, 30*time.Minute)

	link := publicDomain + "/auth/reset-password2?token=" + token
	err = email.SendMailAsync(r.Context(), user.Email, "Reset Password", "Click here to reset your password: "+link)
	if err != nil {
		slog.Error("reset password: send mail async", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	writeResp(w, http.StatusOK, D{})
}

func resetPassword2(w http.ResponseWriter, r *http.Request) {
	type reqStruct struct {
		Token    string `json:"token" validate:"required"`
		Password string `json:"password" validate:"required,printascii,max=32"`
	}
	req, err := decode[reqStruct](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	claims, err := utils.JWTVerify(req.Token)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid token")
		return
	}

	if claims["aud"] != "reset_password" {
		writeError(w, http.StatusBadRequest, "Invalid token")
		return
	}

	userIDStr, ok := claims["sub"].(string)
	if !ok {
		writeError(w, http.StatusBadRequest, "Invalid token")
		return
	}
	userID, err := strconv.Atoi(userIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid token")
		return
	}

	hashedPassword := utils.HashPassword(req.Password)

	err = database.Q.UpdateUserPassword(r.Context(), database.UpdateUserPasswordParams{
		ID:       int32(userID),
		Password: hashedPassword,
	})
	if err != nil {
		slog.Error("reset password 2", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	writeResp(w, http.StatusOK, D{})
}

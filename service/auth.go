package service

import (
	"billing3/database"
	"billing3/database/types"
	"billing3/service/email"
	"billing3/utils"
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var PUBLIC_DOMAIN string

// SendVerificationEmail sends a verification email, which contains the link
// to continue registration process
func SendVerificationEmail(ctx context.Context, emailAddr string) error {
	jwtSign := utils.JWTSign(jwt.MapClaims{
		"aud": "register",
		"sub": emailAddr,
	}, time.Minute*30)

	if PUBLIC_DOMAIN == "" {
		PUBLIC_DOMAIN = os.Getenv("PUBLIC_DOMAIN")
	}

	link := PUBLIC_DOMAIN + "/auth/register2?token=" + jwtSign
	subject := "Verify email"
	body := fmt.Sprintf(`
	<p>Click the following link to continue registration:<br><br><a href=\"%s\">%s</a></p>
	`, link, link)

	err := email.SendMailAsync(ctx, emailAddr, subject, body)
	if err != nil {
		return err
	}
	return nil
}

// DecodeEmailVerificationToken decodes the token for email verification and return the email.
// return empty string if token in invalid
func DecodeEmailVerificationToken(token string) string {
	claims, err := utils.JWTVerify(token)
	if err != nil {
		slog.Debug("jwt verify error", "err", err)
		return ""
	}

	if claims["aud"].(string) != "register" {
		slog.Debug("jwt wrong aud", "aud", claims["aud"])
		return ""
	}

	return claims["sub"].(string)
}

// NewSessionToken returns a new session token for user
func NewSessionToken(ctx context.Context, user int32) (string, error) {
	token := utils.RandomToken(32)
	err := database.Q.CreateSession(ctx, database.CreateSessionParams{
		Token:  token,
		UserID: user,
		ExpiresAt: types.Timestamp{Timestamp: pgtype.Timestamp{
			Valid: true,
			Time:  time.Now().Add(time.Hour * 24 * 30),
		}},
	})
	if err != nil {
		return "", fmt.Errorf("new auth token: %w", err)
	}
	return token, nil
}

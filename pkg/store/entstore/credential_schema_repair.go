package entstore

import (
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

func isMissingCredentialTableError(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "42P01" {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, `relation "credentials" does not exist`) ||
		strings.Contains(msg, "no such table: credentials")
}

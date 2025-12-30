package middleware

import (
	"net/http"

	chimid "github.com/go-chi/chi/v5/middleware"
)

func Recover(next http.Handler) http.Handler {
	return chimid.Recoverer(next)
}

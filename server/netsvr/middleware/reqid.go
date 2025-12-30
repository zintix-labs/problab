package middleware

import (
	"net/http"
	"strings"

	chimid "github.com/go-chi/chi/v5/middleware"
)

func RequestID(next http.Handler) http.Handler {
	return chimid.RequestID(next)
}

func GetReqId(r *http.Request) string {
	return chimid.GetReqID(r.Context())
}

func GetReqIdNumPart(r *http.Request) string {
	str := chimid.GetReqID(r.Context())
	if len(str) == 0 {
		return ""
	}
	i := strings.LastIndex(str, "-")
	if i < 0 || i+1 >= len(str) {
		return str
	}
	return str[i+1:]
}

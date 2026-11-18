package handlers

import (
	"encoding/json"
	"net/http"

	"faqbot/backend/internal/handlers/dto"
)

type apiError = dto.APIError

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, apiError{Code: code, Message: message, RequestID: w.Header().Get("X-Request-ID")})
}

// Package httpjson reads and writes the JSON bodies of the HTTP APIs. Errors
// go out as {"error": "..."}, starting with the field name when there is one,
// like the messages of the contract.
package httpjson

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
)

// Write sends v as JSON with the given status.
func Write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// Error sends {"error": err.Error()} with the given status.
func Error(w http.ResponseWriter, status int, err error) {
	Write(w, status, map[string]string{"error": err.Error()})
}

// InternalError logs the cause and answers 500 without it, so database
// details stay out of the response.
func InternalError(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	Error(w, http.StatusInternalServerError, errors.New("internal error"))
}

// Decode reads a JSON body of up to maxBytes into dst. Unlike the message
// contract, the APIs reject unknown fields, so a typo such as "duration" fails
// instead of being ignored. The error starts with "invalid JSON".
func Decode(w http.ResponseWriter, r *http.Request, maxBytes int64, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return nil
}

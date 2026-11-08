package httpjson

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWrite(t *testing.T) {
	rec := httptest.NewRecorder()
	Error(rec, http.StatusNotFound, errors.New("machine_id: unknown machine"))
	if rec.Code != http.StatusNotFound || rec.Header().Get("Content-Type") != "application/json" ||
		rec.Body.String() != `{"error":"machine_id: unknown machine"}`+"\n" {
		t.Errorf("got %d %q %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
}

func TestInternalError(t *testing.T) {
	rec := httptest.NewRecorder()
	InternalError(rec, httptest.NewRequest(http.MethodGet, "/", nil), errors.New("password authentication failed"))
	if rec.Code != http.StatusInternalServerError || rec.Body.String() != `{"error":"internal error"}`+"\n" {
		t.Errorf("got %d %s, want 500 without the cause", rec.Code, rec.Body)
	}
}

func TestDecode(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string // part of the error, empty for none
	}{
		{"valid", `{"name": "maq-01"}`, ""},
		{"unknown field", `{"name": "maq-01", "nmae": "x"}`, `invalid JSON: json: unknown field "nmae"`},
		{"malformed", `{"name":`, "invalid JSON: unexpected EOF"},
		{"empty", ``, "invalid JSON: EOF"},
		{"too large", `{"name": "` + strings.Repeat("x", 64) + `"}`, "invalid JSON: http: request body too large"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var dst struct {
				Name string `json:"name"`
			}
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tt.body))
			err := Decode(httptest.NewRecorder(), r, 64, &dst)
			if tt.want == "" {
				if err != nil || dst.Name != "maq-01" {
					t.Errorf("got %+v, %v", dst, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("got %v, want %q", err, tt.want)
			}
		})
	}
}

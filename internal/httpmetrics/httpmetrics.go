// Package httpmetrics measures the HTTP requests of a service for Prometheus:
// how many, which ones failed and how long they took (ADR 0017).
package httpmetrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// requestDuration is the same metric in every service; the Prometheus job
// label tells them apart. Its _count gives requests per second, and the code
// label the errors.
var requestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "http_request_duration_seconds",
	Help:    "Time to answer HTTP requests, by method, route pattern and status code.",
	Buckets: prometheus.DefBuckets,
}, []string{"method", "route", "code"})

// Handler measures every request that next serves. next is usually a
// ServeMux: the route label is the pattern it matched, such as
// "GET /work-orders/{id}", so ids in the path do not create new series. A
// request that matched no pattern gets the route "unmatched".
func Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		// The ServeMux sets r.Pattern on the request it received, which is this one.
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		requestDuration.WithLabelValues(r.Method, route, strconv.Itoa(rec.status)).Observe(time.Since(start).Seconds())
	})
}

// statusRecorder remembers the status code a handler wrote.
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wrote {
		s.status, s.wrote = code, true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	s.wrote = true
	return s.ResponseWriter.Write(b)
}

package server

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

var durationBuckets = []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 300, 600}

type metricsRegistry struct {
	mu             sync.Mutex
	started        time.Time
	httpRequests   map[httpRequestKey]uint64
	httpDurations  map[httpDurationKey]*durationHistogram
	httpInFlight   map[httpInFlightKey]int64
	realtimeActive int64
	realtimeOpened uint64
	realtimeItems  uint64
	realtimeEvents map[realtimeEventKey]uint64
	realtimeErrors map[string]uint64
}

type httpRequestKey struct {
	Route  string
	Method string
	Code   int
}

type httpDurationKey struct {
	Route  string
	Method string
}

type httpInFlightKey struct {
	Route  string
	Method string
}

type realtimeEventKey struct {
	Direction string
	Type      string
}

type durationHistogram struct {
	Buckets []uint64
	Count   uint64
	Sum     float64
}

func newMetricsRegistry() *metricsRegistry {
	return &metricsRegistry{
		started:        time.Now(),
		httpRequests:   make(map[httpRequestKey]uint64),
		httpDurations:  make(map[httpDurationKey]*durationHistogram),
		httpInFlight:   make(map[httpInFlightKey]int64),
		realtimeEvents: make(map[realtimeEventKey]uint64),
		realtimeErrors: make(map[string]uint64),
	}
}

func (m *metricsRegistry) beginHTTP(route, method string) func(code int) {
	key := httpInFlightKey{Route: route, Method: method}
	start := time.Now()
	m.mu.Lock()
	m.httpInFlight[key]++
	m.mu.Unlock()
	return func(code int) {
		if code == 0 {
			code = http.StatusOK
		}
		elapsed := time.Since(start).Seconds()
		m.mu.Lock()
		defer m.mu.Unlock()
		m.httpInFlight[key]--
		m.httpRequests[httpRequestKey{Route: route, Method: method, Code: code}]++
		hKey := httpDurationKey{Route: route, Method: method}
		h := m.httpDurations[hKey]
		if h == nil {
			h = &durationHistogram{Buckets: make([]uint64, len(durationBuckets))}
			m.httpDurations[hKey] = h
		}
		h.Count++
		h.Sum += elapsed
		for i, le := range durationBuckets {
			if elapsed <= le {
				h.Buckets[i]++
			}
		}
	}
}

func (m *metricsRegistry) openRealtimeConn() {
	m.mu.Lock()
	m.realtimeActive++
	m.realtimeOpened++
	m.mu.Unlock()
}

func (m *metricsRegistry) closeRealtimeConn() {
	m.mu.Lock()
	m.realtimeActive--
	m.mu.Unlock()
}

func (m *metricsRegistry) newRealtimeItem() {
	m.mu.Lock()
	m.realtimeItems++
	m.mu.Unlock()
}

func (m *metricsRegistry) realtimeEvent(direction, typ string) {
	if typ == "" {
		typ = "unknown"
	}
	m.mu.Lock()
	m.realtimeEvents[realtimeEventKey{Direction: direction, Type: typ}]++
	m.mu.Unlock()
}

func (m *metricsRegistry) realtimeError(code string) {
	if code == "" {
		code = "unknown"
	}
	m.mu.Lock()
	m.realtimeErrors[code]++
	m.mu.Unlock()
}

func (m *metricsRegistry) writePrometheus(w http.ResponseWriter) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = fmt.Fprintln(w, "voxgate_up 1")
	_, _ = fmt.Fprintf(w, "voxgate_uptime_seconds %.0f\n", time.Since(m.started).Seconds())
	_, _ = fmt.Fprintln(w, "# HELP voxgate_http_requests_total HTTP requests by route, method, and status code.")
	_, _ = fmt.Fprintln(w, "# TYPE voxgate_http_requests_total counter")
	for _, key := range sortedHTTPRequests(m.httpRequests) {
		_, _ = fmt.Fprintf(w, "voxgate_http_requests_total{route=%q,method=%q,code=%q} %d\n", key.Route, key.Method, fmt.Sprint(key.Code), m.httpRequests[key])
	}
	_, _ = fmt.Fprintln(w, "# HELP voxgate_http_request_duration_seconds HTTP request duration histogram.")
	_, _ = fmt.Fprintln(w, "# TYPE voxgate_http_request_duration_seconds histogram")
	for _, key := range sortedHTTPDurations(m.httpDurations) {
		h := m.httpDurations[key]
		for i, le := range durationBuckets {
			_, _ = fmt.Fprintf(w, "voxgate_http_request_duration_seconds_bucket{route=%q,method=%q,le=%q} %d\n", key.Route, key.Method, trimFloat(le), h.Buckets[i])
		}
		_, _ = fmt.Fprintf(w, "voxgate_http_request_duration_seconds_bucket{route=%q,method=%q,le=%q} %d\n", key.Route, key.Method, "+Inf", h.Count)
		_, _ = fmt.Fprintf(w, "voxgate_http_request_duration_seconds_sum{route=%q,method=%q} %.6f\n", key.Route, key.Method, h.Sum)
		_, _ = fmt.Fprintf(w, "voxgate_http_request_duration_seconds_count{route=%q,method=%q} %d\n", key.Route, key.Method, h.Count)
	}
	_, _ = fmt.Fprintln(w, "# HELP voxgate_http_in_flight In-flight HTTP requests by route and method.")
	_, _ = fmt.Fprintln(w, "# TYPE voxgate_http_in_flight gauge")
	for _, key := range sortedHTTPInFlight(m.httpInFlight) {
		_, _ = fmt.Fprintf(w, "voxgate_http_in_flight{route=%q,method=%q} %d\n", key.Route, key.Method, m.httpInFlight[key])
	}
	_, _ = fmt.Fprintln(w, "# HELP voxgate_realtime_connections_active Active realtime WebSocket connections.")
	_, _ = fmt.Fprintln(w, "# TYPE voxgate_realtime_connections_active gauge")
	_, _ = fmt.Fprintf(w, "voxgate_realtime_connections_active %d\n", m.realtimeActive)
	_, _ = fmt.Fprintln(w, "# HELP voxgate_realtime_connections_total Total accepted realtime WebSocket connections.")
	_, _ = fmt.Fprintln(w, "# TYPE voxgate_realtime_connections_total counter")
	_, _ = fmt.Fprintf(w, "voxgate_realtime_connections_total %d\n", m.realtimeOpened)
	_, _ = fmt.Fprintln(w, "# HELP voxgate_realtime_items_total Total upstream realtime ASR items created.")
	_, _ = fmt.Fprintln(w, "# TYPE voxgate_realtime_items_total counter")
	_, _ = fmt.Fprintf(w, "voxgate_realtime_items_total %d\n", m.realtimeItems)
	_, _ = fmt.Fprintln(w, "# HELP voxgate_realtime_events_total Realtime client/server events by direction and type.")
	_, _ = fmt.Fprintln(w, "# TYPE voxgate_realtime_events_total counter")
	for _, key := range sortedRealtimeEvents(m.realtimeEvents) {
		_, _ = fmt.Fprintf(w, "voxgate_realtime_events_total{direction=%q,type=%q} %d\n", key.Direction, key.Type, m.realtimeEvents[key])
	}
	_, _ = fmt.Fprintln(w, "# HELP voxgate_realtime_errors_total Realtime protocol or upstream errors by code.")
	_, _ = fmt.Fprintln(w, "# TYPE voxgate_realtime_errors_total counter")
	for _, code := range sortedStringKeys(m.realtimeErrors) {
		_, _ = fmt.Fprintf(w, "voxgate_realtime_errors_total{code=%q} %d\n", code, m.realtimeErrors[code])
	}
}

func sortedHTTPRequests(m map[httpRequestKey]uint64) []httpRequestKey {
	keys := make([]httpRequestKey, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Route != keys[j].Route {
			return keys[i].Route < keys[j].Route
		}
		if keys[i].Method != keys[j].Method {
			return keys[i].Method < keys[j].Method
		}
		return keys[i].Code < keys[j].Code
	})
	return keys
}

func sortedHTTPDurations(m map[httpDurationKey]*durationHistogram) []httpDurationKey {
	keys := make([]httpDurationKey, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Route != keys[j].Route {
			return keys[i].Route < keys[j].Route
		}
		return keys[i].Method < keys[j].Method
	})
	return keys
}

func sortedHTTPInFlight(m map[httpInFlightKey]int64) []httpInFlightKey {
	keys := make([]httpInFlightKey, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Route != keys[j].Route {
			return keys[i].Route < keys[j].Route
		}
		return keys[i].Method < keys[j].Method
	})
	return keys
}

func sortedRealtimeEvents(m map[realtimeEventKey]uint64) []realtimeEventKey {
	keys := make([]realtimeEventKey, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Direction != keys[j].Direction {
			return keys[i].Direction < keys[j].Direction
		}
		return keys[i].Type < keys[j].Type
	})
	return keys
}

func sortedStringKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func trimFloat(v float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", v), "0"), ".")
}

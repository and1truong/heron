package control

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/and1truong/heron/internal/supervisor"
)

func TestRestartEndpoint(t *testing.T) {
	calls := 0
	h := New(func() { calls++ })
	request := func(method, host, command string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://"+host+RestartPath, nil)
		r.Header.Set(commandHeader, command)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, tc := range []struct {
		method, host, command string
		want                  int
	}{
		{http.MethodGet, Hostname, "restart", http.StatusMethodNotAllowed},
		{http.MethodPost, "api.localhost", "restart", http.StatusForbidden},
		{http.MethodPost, Hostname, "", http.StatusForbidden},
	} {
		if got := request(tc.method, tc.host, tc.command).Code; got != tc.want {
			t.Fatalf("status = %d, want %d", got, tc.want)
		}
	}
	if got := request(http.MethodPost, Hostname+":3000", "restart"); got.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", got.Code, got.Body.String())
	}
	if got := request(http.MethodPost, Hostname, "restart"); got.Code != http.StatusAccepted {
		t.Fatalf("duplicate status = %d", got.Code)
	}
	if calls != 1 {
		t.Fatalf("restart calls = %d, want 1", calls)
	}
}

func TestServiceRestartEndpoint(t *testing.T) {
	for _, tc := range []struct {
		method, path, header string
		err                  error
		want                 int
		calls                int
	}{
		{"POST", "/_heron/services/api/restart", "restart", nil, 200, 1},
		{"POST", "/_heron/services/api/restart", "restart", supervisor.ErrUnknownService, 404, 1},
		{"POST", "/_heron/services/api/restart", "restart", errors.New("invalid config"), 409, 1},
		{"GET", "/_heron/services/api/restart", "restart", nil, 405, 0},
		{"POST", "/_heron/services/api/restart", "", nil, 403, 0},
		{"POST", "/_heron/services//restart", "restart", nil, 404, 0},
		{"POST", "/_heron/services/a/b/restart", "restart", nil, 404, 0},
	} {
		h := New(func() { t.Fatal("full restart called") })
		calls := 0
		h.RestartService = func(ctx context.Context, id string) error {
			calls++
			if id != "api" {
				t.Fatal(id)
			}
			return tc.err
		}
		r := httptest.NewRequest(tc.method, "http://"+Hostname+tc.path, nil)
		r.Header.Set(commandHeader, tc.header)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want || calls != tc.calls {
			t.Fatalf("%+v: status=%d calls=%d", tc, w.Code, calls)
		}
	}
}

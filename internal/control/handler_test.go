package control

import (
	"net/http"
	"net/http/httptest"
	"testing"
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

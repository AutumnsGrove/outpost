package hetzner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// withServer points apiBase at a test server for the duration of one test.
func withServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	old := apiBase
	apiBase = srv.URL
	t.Cleanup(func() { apiBase = old })
	return srv
}

func decodeBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	return body
}

func TestCreateSendsFirewallOnlyWhenConfigured(t *testing.T) {
	var bodies []map[string]any
	withServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/servers" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Errorf("auth header = %q", got)
		}
		bodies = append(bodies, decodeBody(t, r))
		_, _ = io.WriteString(w, `{"server":{"id":1,"name":"outpost-eu-abc","status":"running","public_net":{"ipv4":{"ip":"203.0.113.10"}}}}`)
	})

	c := New("token")
	base := CreateRequest{
		Name:       "outpost-eu-abc",
		ServerType: "cx22",
		Location:   "nbg1",
		Image:      "ubuntu-24.04",
		SSHKeyID:   7,
	}

	if _, err := c.Create(context.Background(), base); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, ok := bodies[0]["firewalls"]; ok {
		t.Fatal("a firewall was sent when none was configured")
	}

	withFirewall := base
	withFirewall.FirewallID = 42
	if _, err := c.Create(context.Background(), withFirewall); err != nil {
		t.Fatalf("create with firewall: %v", err)
	}
	fw, ok := bodies[1]["firewalls"].([]any)
	if !ok || len(fw) != 1 {
		t.Fatalf("firewalls = %#v, want one entry", bodies[1]["firewalls"])
	}
	entry, _ := fw[0].(map[string]any)
	if entry["firewall"] != float64(42) {
		t.Fatalf("firewall id = %#v, want 42", entry["firewall"])
	}
}

func TestCreateUsesSnapshotAsTheImage(t *testing.T) {
	var body map[string]any
	withServer(t, func(w http.ResponseWriter, r *http.Request) {
		body = decodeBody(t, r)
		_, _ = io.WriteString(w, `{"server":{"id":2}}`)
	})

	if _, err := New("token").Create(context.Background(), CreateRequest{
		Name: "x", ServerType: "cx22", Location: "nbg1", Image: "ubuntu-24.04", SnapshotID: 99,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if body["image"] != float64(99) {
		t.Fatalf("image = %#v, want the snapshot id", body["image"])
	}
}

func TestGetMapsNotFound(t *testing.T) {
	withServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"server not found"}}`)
	})

	_, err := New("token").Get(context.Background(), "123")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestDeleteTreatsNotFoundAsSuccess(t *testing.T) {
	withServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	if err := New("token").Delete(context.Background(), "123"); err != nil {
		t.Fatalf("deleting a gone server should be a no-op, got %v", err)
	}
}

func TestListFiltersByProjectLabel(t *testing.T) {
	withServer(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("label_selector"); got != "project=outpost" {
			t.Errorf("label selector = %q", got)
		}
		_, _ = io.WriteString(w, `{"servers":[{"id":1,"name":"a"},{"id":2,"name":"b"}]}`)
	})

	servers, err := New("token").List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(servers) != 2 {
		t.Fatalf("servers = %d, want 2", len(servers))
	}
}

func TestErrorBodyIsReported(t *testing.T) {
	withServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, `{"error":{"code":"unavailable","message":"no server type available in this location"}}`)
	})

	_, err := New("token").Create(context.Background(), CreateRequest{Name: "x", ServerType: "cx22", Location: "nbg1"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := err.Error(); !contains(got, "no server type available") {
		t.Fatalf("error lost the API's explanation: %v", err)
	}
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

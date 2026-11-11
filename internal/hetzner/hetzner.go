// Package hetzner is a narrow client for the bits of the Hetzner Cloud API
// Outpost needs: create an ephemeral server, look at it, delete it.
package hetzner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const apiBase = "https://api.hetzner.cloud/v1"

// ErrNotFound is returned when the server no longer exists. It is a normal
// condition: Outpost deletes servers and then asks about them again.
var ErrNotFound = errors.New("hetzner: server not found")

// Server is the subset of the API object Outpost cares about.
type Server struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	ServerType struct {
		Name string `json:"name"`
	} `json:"server_type"`
	PublicNet struct {
		IPv4 struct {
			IP string `json:"ip"`
		} `json:"ipv4"`
	} `json:"public_net"`
}

// CreateRequest describes the VPS to rent.
type CreateRequest struct {
	Name       string
	ServerType string
	Location   string
	Image      string
	SSHKeyID   int
	SnapshotID int // when set, Image is ignored and the snapshot is restored
	UserData   string
}

// Client talks to the Hetzner Cloud API.
type Client struct {
	token string
	http  *http.Client
}

// New returns a client using token.
func New(token string) *Client {
	return &Client{token: token, http: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		payload = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, apiBase+path, payload)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, bytes.TrimSpace(msg))
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// Create rents the server and returns it with its address already assigned.
func (c *Client) Create(ctx context.Context, spec CreateRequest) (*Server, error) {
	body := map[string]any{
		"name":               spec.Name,
		"server_type":        spec.ServerType,
		"location":           spec.Location,
		"start_after_create": true,
		"labels": map[string]string{
			"project": "outpost",
			"managed": "true",
		},
	}
	if spec.SnapshotID > 0 {
		body["image"] = spec.SnapshotID
	} else {
		body["image"] = spec.Image
	}
	if spec.SSHKeyID > 0 {
		body["ssh_keys"] = []string{fmt.Sprint(spec.SSHKeyID)}
	}
	if spec.UserData != "" {
		body["user_data"] = spec.UserData
	}

	var out struct {
		Server Server `json:"server"`
	}
	if err := c.do(ctx, http.MethodPost, "/servers", body, &out); err != nil {
		return nil, fmt.Errorf("create server: %w", err)
	}
	return &out.Server, nil
}

// Get returns the server, or ErrNotFound.
func (c *Client) Get(ctx context.Context, id string) (*Server, error) {
	var out struct {
		Server Server `json:"server"`
	}
	if err := c.do(ctx, http.MethodGet, "/servers/"+id, nil, &out); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get server %s: %w", id, err)
	}
	return &out.Server, nil
}

// Delete destroys the server. Deleting an already-deleted server is not an error.
func (c *Client) Delete(ctx context.Context, id string) error {
	err := c.do(ctx, http.MethodDelete, "/servers/"+id, nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete server %s: %w", id, err)
	}
	return nil
}

// List returns every Outpost-labelled server in the project.
func (c *Client) List(ctx context.Context) ([]Server, error) {
	var out struct {
		Servers []Server `json:"servers"`
	}
	if err := c.do(ctx, http.MethodGet, "/servers?label_selector=project=outpost", nil, &out); err != nil {
		return nil, fmt.Errorf("list servers: %w", err)
	}
	return out.Servers, nil
}

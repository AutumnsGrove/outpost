// Package dns keeps one Cloudflare A record pointed at the current VPS.
//
// Outpost deliberately does not run a proxied hostname: Minecraft is raw TCP on
// 25565 and Cloudflare's proxy does not carry it, so the record is DNS-only and
// the client talks straight to Hetzner.
package dns

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const apiBase = "https://api.cloudflare.com/client/v4"

// Client owns one A record.
type Client struct {
	token  string
	zoneID string
	name   string
	http   *http.Client
}

// New returns a client for name (a fully qualified hostname) in zoneID.
func New(token, zoneID, name string) *Client {
	return &Client{
		token:  token,
		zoneID: zoneID,
		name:   name,
		http:   &http.Client{Timeout: 20 * time.Second},
	}
}

type envelope struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
	Result json.RawMessage `json:"result"`
}

type record struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, apiBase+path, payload)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	var env envelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return fmt.Errorf("decode cloudflare response: %w", err)
	}
	if !env.Success || resp.StatusCode >= 300 {
		for _, e := range env.Errors {
			return fmt.Errorf("cloudflare %s %s: %d %s", method, path, e.Code, e.Message)
		}
		return fmt.Errorf("cloudflare %s %s: %s", method, path, resp.Status)
	}
	if out != nil && len(env.Result) > 0 {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return fmt.Errorf("decode cloudflare result: %w", err)
		}
	}
	return nil
}

// find returns the existing A record, or nil if there is none.
func (c *Client) find(ctx context.Context) (*record, error) {
	path := fmt.Sprintf("/zones/%s/dns_records?type=A&name=%s", c.zoneID, c.name)
	var found []record
	if err := c.do(ctx, http.MethodGet, path, nil, &found); err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, nil
	}
	return &found[0], nil
}

// SetA points the record at ip, creating it if it does not exist.
func (c *Client) SetA(ctx context.Context, ip string) error {
	if ip == "" {
		return fmt.Errorf("refusing to set an empty address")
	}

	existing, err := c.find(ctx)
	if err != nil {
		return err
	}

	body := map[string]any{
		"type":    "A",
		"name":    c.name,
		"content": ip,
		"ttl":     60,
		"proxied": false,
	}

	if existing == nil {
		path := fmt.Sprintf("/zones/%s/dns_records", c.zoneID)
		return c.do(ctx, http.MethodPost, path, body, nil)
	}
	if existing.Content == ip {
		return nil
	}

	path := fmt.Sprintf("/zones/%s/dns_records/%s", c.zoneID, existing.ID)
	return c.do(ctx, http.MethodPatch, path, body, nil)
}

// GetA returns the record's current address, or "" if it does not exist.
func (c *Client) GetA(ctx context.Context) (string, error) {
	r, err := c.find(ctx)
	if err != nil || r == nil {
		return "", err
	}
	return r.Content, nil
}

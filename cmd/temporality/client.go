package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// client is a minimal read-only client for the workspace skill API.
type client struct {
	base  string
	token string
	http  *http.Client
}

func newClient(base, token string) *client {
	return &client{
		base:  strings.TrimRight(base, "/"),
		token: token,
		http:  &http.Client{Timeout: 30 * time.Second},
	}
}

// getRaw fetches path with optional query parameters and returns the response
// body. Non-200 responses are decoded into an error.
func (c *client) getRaw(path string, query url.Values) ([]byte, error) {
	target := c.base + path
	if len(query) != 0 {
		target += "?" + query.Encode()
	}
	request, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", target, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		var payload struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &payload) == nil && payload.Error != "" {
			return nil, fmt.Errorf("HTTP %d: %s", response.StatusCode, payload.Error)
		}
		return nil, fmt.Errorf("HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

// get fetches path and decodes the JSON body into out.
func (c *client) get(path string, query url.Values, out any) error {
	body, err := c.getRaw(path, query)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

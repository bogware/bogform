// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package ci

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Names of the variables that connect pipelines to a Bogform server.
const (
	ServerURLEnvName   = "BOG_SERVER_URL"
	ServerTokenEnvName = "BOG_SERVER_TOKEN"
)

// NewPublisher returns a function that sends reports to a Bogform server. It refuses to send the token
// over an unencrypted connection, except to this machine.
func NewPublisher(serverURL, token string, client *http.Client) (func(context.Context, *Report) error, error) {
	parsed, err := url.Parse(strings.TrimSuffix(serverURL, "/"))
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("%s isn't a valid URL", ServerURLEnvName)
	}
	if parsed.Scheme != "https" && !isLoopbackHost(parsed.Hostname()) {
		return nil, fmt.Errorf("%s must use https, so that the token isn't sent unencrypted", ServerURLEnvName)
	}
	if token == "" {
		return nil, fmt.Errorf("%s isn't set", ServerTokenEnvName)
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	target := parsed.String() + "/api/v1/reports"
	return func(ctx context.Context, report *Report) error {
		body, err := json.Marshal(report)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "bogform-ci")
		res, err := client.Do(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		if res.StatusCode >= 300 {
			var problem struct {
				Error string `json:"error"`
			}
			raw, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
			_ = json.Unmarshal(raw, &problem)
			return fmt.Errorf("the server returned %d: %s", res.StatusCode, problem.Error)
		}
		return nil
	}, nil
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

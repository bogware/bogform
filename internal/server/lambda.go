// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"
)

// RunLambda serves requests using AWS Lambda's runtime API, which is a small HTTP protocol. Speaking it
// directly avoids a dependency, and handler is the same one used by the standalone server. It returns
// only if the runtime API stops working. afterResponse runs once each response has been sent, before
// asking for the next event, which is the last chance for work in the background to finish before
// Lambda freezes the function.
func RunLambda(ctx context.Context, runtimeAPI string, handler http.Handler, afterResponse ...func()) error {
	base := "http://" + runtimeAPI + "/2018-06-01/runtime/invocation/"
	// Waiting for the next event can take any length of time, so there's no timeout.
	client := &http.Client{}
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"next", nil)
		if err != nil {
			return err
		}
		res, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("getting the next event: %w", err)
		}
		event, readErr := io.ReadAll(res.Body)
		res.Body.Close()
		if readErr != nil {
			return readErr
		}
		id := res.Header.Get("Lambda-Runtime-Aws-Request-Id")

		requestCtx := ctx
		cancel := func() {}
		if deadline, err := time.Parse(time.RFC3339, res.Header.Get("Lambda-Runtime-Deadline-Rfc3339")); err == nil {
			requestCtx, cancel = context.WithDeadline(ctx, deadline)
		} else if ms := res.Header.Get("Lambda-Runtime-Deadline-Ms"); ms != "" {
			var millis int64
			if _, err := fmt.Sscan(ms, &millis); err == nil {
				requestCtx, cancel = context.WithDeadline(ctx, time.UnixMilli(millis))
			}
		}
		response, err := handleLambdaEvent(requestCtx, handler, event)
		cancel()

		suffix, body := id+"/response", response
		if err != nil {
			suffix = id + "/error"
			body, _ = json.Marshal(map[string]string{"errorMessage": err.Error(), "errorType": "BogformError"})
		}
		post, postErr := http.NewRequestWithContext(ctx, http.MethodPost, base+suffix, bytes.NewReader(body))
		if postErr != nil {
			return postErr
		}
		result, postErr := client.Do(post)
		if postErr != nil {
			return fmt.Errorf("sending a response: %w", postErr)
		}
		io.Copy(io.Discard, result.Body) //nolint:errcheck // Only draining the connection.
		result.Body.Close()
		for _, fn := range afterResponse {
			fn()
		}
	}
}

// functionURLEvent is the format that Lambda function URLs use for requests (payload format 2.0).
type functionURLEvent struct {
	RawPath         string            `json:"rawPath"`
	RawQueryString  string            `json:"rawQueryString"`
	Cookies         []string          `json:"cookies"`
	Headers         map[string]string `json:"headers"`
	Body            string            `json:"body"`
	IsBase64Encoded bool              `json:"isBase64Encoded"`
	RequestContext  struct {
		HTTP struct {
			Method   string `json:"method"`
			SourceIP string `json:"sourceIp"`
		} `json:"http"`
	} `json:"requestContext"`
}

type functionURLResponse struct {
	StatusCode      int               `json:"statusCode"`
	Headers         map[string]string `json:"headers"`
	Cookies         []string          `json:"cookies"`
	Body            string            `json:"body"`
	IsBase64Encoded bool              `json:"isBase64Encoded"`
}

func handleLambdaEvent(ctx context.Context, handler http.Handler, raw []byte) ([]byte, error) {
	var event functionURLEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return nil, fmt.Errorf("reading the event: %w", err)
	}
	body := []byte(event.Body)
	if event.IsBase64Encoded {
		var err error
		if body, err = base64.StdEncoding.DecodeString(event.Body); err != nil {
			return nil, fmt.Errorf("decoding the body: %w", err)
		}
	}
	target := url.URL{Path: event.RawPath, RawQuery: event.RawQueryString}
	req, err := http.NewRequestWithContext(ctx, event.RequestContext.HTTP.Method, target.RequestURI(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("building the request: %w", err)
	}
	for name, value := range event.Headers {
		req.Header.Set(name, value)
	}
	if len(event.Cookies) > 0 {
		req.Header.Set("Cookie", strings.Join(event.Cookies, "; "))
	}
	req.Host = event.Headers["host"]
	req.RemoteAddr = event.RequestContext.HTTP.SourceIP + ":0"
	req.ContentLength = int64(len(body))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	result := recorder.Result()
	defer result.Body.Close()

	response := functionURLResponse{StatusCode: result.StatusCode, Headers: map[string]string{}, Cookies: []string{}}
	for name, values := range result.Header {
		if name == "Set-Cookie" {
			response.Cookies = append(response.Cookies, values...)
			continue
		}
		response.Headers[name] = strings.Join(values, ", ")
	}
	response.Body = recorder.Body.String()
	return json.Marshal(response)
}

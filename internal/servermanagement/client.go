// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package servermanagement

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type client struct {
	httpClient HTTPClient
	parsedURL  *url.URL
	username   string
	password   string
	token      string
}

// ClientOptions represents the options for the client.
type ClientOptions struct {
	Endpoint           string
	HTTPClient         HTTPClient
	InsecureSkipVerify bool
	Username           string
	Password           string
	Token              string
	Agent              string
	Version            string
}

// HTTPClient provides the interface for a client making HTTP requests with the
// API.
type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

func NewClient(options ClientOptions) (c *client, err error) {
	if options.Endpoint == "" {
		return c, errors.New("the baseURL is mandatory")
	}
	c = &client{}
	c.parsedURL, err = url.Parse(options.Endpoint)
	if err != nil {
		err = fmt.Errorf("the URL '%s' isn't valid: %s", options.Endpoint, err.Error())
		return nil, err
	}
	if options.HTTPClient == nil {
		options.HTTPClient = createClient(options)
	}
	if options.Agent == "" {
		options.Agent = "maintenance-operator"
	}
	c.httpClient = options.HTTPClient
	c.token = options.Token
	c.password = options.Password
	c.username = options.Username
	return c, nil
}

// DoRequest performs an HTTP request to the AWX API.
func (c *client) DoRequest(req *http.Request, okCodes []int) ([]byte, error) {
	if c.httpClient == nil {
		return nil, errors.New("the HTTP client is mandatory")
	}
	if c.parsedURL == nil {
		return nil, errors.New("the URL is mandatory")
	}
	if c.token == "" {
		if err := c.getAuthToken(); err != nil {
			return nil, err
		}
	}
	req.Header = http.Header{
		"Authorization": []string{"Bearer " + c.token},
		// "User-Agent":    []string{c.agent},
		"Content-Type": []string{"application/json"},
	}
	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}

	defer res.Body.Close() //nolint:errcheck // We can't do anything if closing the body fails.
	ok := false
	for _, code := range okCodes {
		if res.StatusCode == code {
			ok = true
			break
		}
	}
	if !ok {
		output := ErrorResponse{}
		body, err := io.ReadAll(res.Body)
		if err != nil {
			return nil, fmt.Errorf("unexpected error response: %s", err.Error())
		}
		dec := json.NewDecoder(bytes.NewReader(body)).Decode(&output)
		if dec != nil {
			return nil, NewError(res.StatusCode, []string{
				"unexpected error response: " + string(body),
			})
		}
		if output.All == nil {
			return nil, NewError(res.StatusCode, []string{
				"unexpected error response: " + string(body),
			})
		}
		return nil, NewError(res.StatusCode, output.All)
	}
	body, err := io.ReadAll(res.Body)
	return body, err
}

func (c *client) getAuthToken() error {
	return nil
}

func createClient(options ClientOptions) (client *http.Client) {
	// Create the HTTP client:
	client = &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: options.InsecureSkipVerify, // #nosec G402
			},
		},
	}
	return
}

// Error represents the output of the Error method.
type Error struct {
	StatusCode int
	Msg        []string
}

// Error returns the error message.
func (e *Error) Error() string {
	return "status: " + strconv.Itoa(e.StatusCode) + ", messages: " + strings.Join(e.Msg, ", ")
}

// NewError creates a new error.
func NewError(statusCode int, msg []string) *Error {
	return &Error{statusCode, msg}
}

// ErrorResponse represents the awx error response.
type ErrorResponse struct {
	All []string `json:"__all__"`
}

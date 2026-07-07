// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package hwmgr

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// TODO: client has two sets of fields: the legacy ones (httpClient, parsedURL, username,
// password, token) used by DellClient and LenovoClient via NewClient/DoRequest, and the
// newer ones (Config, Auth) used by firmware upgrade methods. Once DellClient and LenovoClient are
// migrated to use Config/Auth, the legacy fields and their helpers (DoRequest, getAuthToken,
// createClient, Error, NewError, ErrorResponse) should be removed.
type client struct {
	httpClient HTTPClient
	parsedURL  *neturl.URL
	username   string
	password   string
	token      string
	Config     *MgrConfig
	Auth       *AuthToken
}

// ClientOptions represents the options for the client.
type ClientOptions struct {
	Endpoint           string
	HTTPClient         HTTPClient
	InsecureSkipVerify bool
	Username           string
	Password           string
	Domain             string
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
	c.parsedURL, err = neturl.Parse(options.Endpoint)
	if err != nil {
		err = fmt.Errorf("the URL '%s' isn't valid: %s", options.Endpoint, err.Error())
		return nil, err
	}
	if options.HTTPClient == nil {
		options.HTTPClient = createClient(options)
	}
	if options.Agent == "" {
		options.Agent = "metal-maintenance-operator"
	}
	c.httpClient = options.HTTPClient
	c.token = options.Token
	c.password = options.Password
	c.username = options.Username
	return c, nil
}

// DoRequest performs an HTTP request, injecting Bearer auth headers, and returns the response body.
// Legacy callers (DellClient, LenovoClient) use this signature. New code should call MgrDoRequest directly.
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
	resp, err := c.MgrDoRequest(context.Background(), req, okCodes)
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close() //nolint:errcheck
	}
	if err != nil {
		return nil, err
	}
	return io.ReadAll(resp.Body)
}

func (c *client) getAuthToken() error {
	return nil
}

func createClient(options ClientOptions) (client *http.Client) {
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

// ResponseError is returned when an HTTP request gets an unexpected status code.
type ResponseError struct {
	StatusCode int
	Body       []byte
	URL        string
	Method     string
}

func (e *ResponseError) Error() string {
	return fmt.Sprintf("error from '%s' URL: %s \nstatus code: %d, \nbody: %s",
		e.Method,
		e.URL,
		e.StatusCode,
		string(e.Body),
	)
}

// MgrConfig holds connection settings for the OME/vendor-console HTTP client.
type MgrConfig struct {
	URL                 *neturl.URL
	InsecureSkipVerify  bool
	TLSHandshakeTimeout time.Duration
	ReuseConnections    bool
	HeaderCustomization map[string]string
}

// AuthToken holds credential and session state for the client.
type AuthToken struct {
	Token     string
	Session   string
	Username  string
	Password  string
	AuthType  AuthMethod
	SessionId string
}

// CreateManagerClient creates an *http.Client from a MgrConfig.
func CreateManagerClient(config *MgrConfig) *http.Client {
	transport := &http.Transport{
		TLSHandshakeTimeout: config.TLSHandshakeTimeout,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: config.InsecureSkipVerify, // #nosec G402
		},
	}
	if config.ReuseConnections {
		transport.DisableKeepAlives = false
		transport.IdleConnTimeout = 0
	}
	return &http.Client{Timeout: 30 * time.Second, Transport: transport}
}

// CreateSession creates an authenticated session.
func (c *client) CreateSession(
	ctx context.Context,
	url *neturl.URL,
	body map[string]string,
	authMtd AuthMethod,
	okCodes []int,
) error {
	bodyByte, err := json.Marshal(body)
	if err != nil {
		return err
	}
	if authMtd == BasicAuth {
		c.Auth = &AuthToken{AuthType: authMtd}
		c.Auth.Token = "Basic " + base64.StdEncoding.EncodeToString([]byte(body["Username"]+":"+body["Password"]))
		return nil
	}
	req, err := c.CreateRequestWithAuth(url, http.MethodPost, strings.NewReader(string(bodyByte)), nil)
	if err != nil {
		return err
	}
	resp, err := c.MgrDoRequest(ctx, req, okCodes)
	if err != nil {
		return err
	}
	defer resp.Body.Close() // nolint: errcheck

	c.Auth = &AuthToken{AuthType: authMtd}
	c.Auth.Token = resp.Header.Get("X-Auth-Token")
	c.Auth.Session = resp.Header.Get("Location")

	if urlParser, err := neturl.ParseRequestURI(c.Auth.Session); err == nil {
		c.Auth.Session = urlParser.RequestURI()
	}
	return err
}

// MgrDoRequest performs an HTTP request and returns the response. The caller must close the body.
func (c *client) MgrDoRequest(ctx context.Context, req *http.Request, okCodes []int) (*http.Response, error) {
	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if len(okCodes) > 0 && !slices.Contains(okCodes, res.StatusCode) {
		resBody := []byte{}
		if res.Body != nil {
			resBody, _ = io.ReadAll(res.Body)
		}
		res.Body.Close() // nolint: errcheck
		return res, &ResponseError{
			StatusCode: res.StatusCode,
			Body:       resBody,
			URL:        req.URL.String(),
			Method:     req.Method,
		}
	}
	return res, nil
}

// GetBodyFromRequest executes a request and returns the response body bytes.
func (c *client) GetBodyFromRequest(ctx context.Context, req *http.Request, okCodes []int) ([]byte, error) {
	var resBody []byte
	response, err := c.MgrDoRequest(ctx, req, okCodes)
	if response != nil && response.Body != nil {
		defer response.Body.Close() // nolint: errcheck
		resBody, _ = io.ReadAll(response.Body)
	}
	return resBody, err
}

// Get performs a GET and JSON-decodes the response into returnData.
func (c *client) Get(ctx context.Context, url *neturl.URL, returnData any, okCodes []int) error {
	resBody, err := c.GetResponseBody(ctx, url, okCodes)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(resBody, returnData); err != nil {
		return fmt.Errorf("failed to decode response from: %v. \nresponse: %v \twith error: %v",
			url.String(), string(resBody), err)
	}
	return err
}

// GetResponseBody performs a GET and returns the raw response body.
func (c *client) GetResponseBody(ctx context.Context, url *neturl.URL, okCodes []int) ([]byte, error) {
	req, err := c.CreateRequestWithAuth(url, http.MethodGet, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request %v \twith error: %v", url.String(), err)
	}
	return c.GetBodyFromRequest(ctx, req, okCodes)
}

// Post performs a POST and JSON-decodes the response into returnData.
func (c *client) Post(
	ctx context.Context,
	url *neturl.URL,
	body io.Reader,
	returnData any,
	okCodes []int,
) error {
	response, err := c.PostWithResponse(ctx, url, body, okCodes)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(response, returnData); err != nil {
		return fmt.Errorf("failed to decode response from: %v \nresponse: %v \nwith error: %v", url.String(), response, err)
	}
	return err
}

// PostWithResponse performs a POST and returns the raw response body.
func (c *client) PostWithResponse(
	ctx context.Context,
	url *neturl.URL,
	body io.Reader,
	okCodes []int,
) ([]byte, error) {
	req, err := c.CreateRequestWithAuth(url, http.MethodPost, body, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request %v \twith error: %v", url.String(), err)
	}
	return c.GetBodyFromRequest(ctx, req, okCodes)
}

// Put performs a PUT and JSON-decodes the response into returnData.
func (c *client) Put(ctx context.Context, url *neturl.URL, body io.Reader, returnData any, okCodes []int) error {
	response, err := c.PutWithResponse(ctx, url, body, okCodes)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(response, returnData); err != nil {
		return fmt.Errorf("failed to decode response from: %v. \nresponse: %v \twith error: %v", url.String(), response, err)
	}
	return err
}

// PutWithResponse performs a PUT and returns the raw response body.
func (c *client) PutWithResponse(
	ctx context.Context,
	url *neturl.URL,
	body io.Reader,
	okCodes []int,
) ([]byte, error) {
	req, err := c.CreateRequestWithAuth(url, http.MethodPut, body, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request %v \twith error: %v", url.String(), err)
	}
	return c.GetBodyFromRequest(ctx, req, okCodes)
}

// CreateRequestWithAuth builds an HTTP request with auth headers applied.
func (c *client) CreateRequestWithAuth(
	url *neturl.URL,
	httpMethod string,
	body io.Reader,
	header http.Header,
) (*http.Request, error) {
	req, err := http.NewRequest(httpMethod, url.String(), body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request for: %v. due to err %v", url.String(), err)
	}
	if c.Config.ReuseConnections {
		req.Close = false
		req.Header.Add("Connection", "keep-alive")
	}
	if c.Config.HeaderCustomization == nil && header == nil {
		req.Header = http.Header{
			"Content-Type": []string{"application/json"},
		}
	} else if header != nil {
		req.Header = header
	} else {
		req.Header = http.Header{}
		for k, v := range c.Config.HeaderCustomization {
			req.Header.Add(k, v)
		}
		if req.Header.Get("Content-Type") == "" {
			req.Header.Add("Content-Type", "application/json")
		}
	}
	if req.Header.Get("Content-Type") == "" {
		req.Header.Add("Content-Type", "application/json")
	}
	if c.Auth != nil && c.Auth.Token != "" {
		req.Header.Add(string(c.Auth.AuthType), c.Auth.Token)
	}
	return req, nil
}

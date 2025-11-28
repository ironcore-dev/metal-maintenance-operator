// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
//
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"slices"
	"strings"
	"time"
)

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

// Client represents the VendorConsole client.
type ManagerClient struct {
	Auth   *AuthToken
	Client *http.Client
	Config *Config
}

type Config struct {
	URL                 *neturl.URL
	InsecureSkipVerify  bool
	TLSHandshakeTimeout time.Duration
	ReuseConnections    bool
	HeaderCustomization map[string]string
}

type AuthToken struct {
	Token     string
	Session   string
	Username  string
	Password  string
	AuthType  AuthMethod
	SessionId string
}

func CreateClient(config *Config) *http.Client {
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

func (c *ManagerClient) CreateSession(
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
	resp, err := c.DoRequest(ctx, req, okCodes)
	if err != nil {
		return err
	}
	defer resp.Body.Close() // nolint: errcheck

	switch authMtd {
	case DellToken:
		c.Auth = &AuthToken{AuthType: authMtd}

		c.Auth.Token = resp.Header.Get("X-Auth-Token")
		c.Auth.Session = resp.Header.Get("Location")

		if urlParser, err := neturl.ParseRequestURI(c.Auth.Session); err == nil {
			c.Auth.Session = urlParser.RequestURI()
		}
	case HPEToken:
		c.Auth = &AuthToken{AuthType: authMtd}
		sessionMap := map[string]any{}
		respbody, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("failed to read response body from: %v. \nwith error: %v", url.String(), err)
		}
		if err = json.Unmarshal(respbody, &sessionMap); err != nil {
			return fmt.Errorf(
				"failed to decode response from: %v. \nresponse: %v \twith error: %v",
				url.String(),
				string(respbody),
				err,
			)
		}
		c.Auth.Token = (sessionMap["sessionID"]).(string)
		c.Auth.SessionId = c.Auth.Token
	}

	return err
}

// DoRequest performs an HTTP request and returns the response body or an error.
// its caller is responsible to close the response body.
func (c *ManagerClient) DoRequest(ctx context.Context, req *http.Request, okCodes []int) (*http.Response, error) {
	res, err := c.Client.Do(req)
	if err != nil {
		return nil, err
	}
	// Check for expected status codes only if provided.
	// else accept any status code.
	if len(okCodes) > 0 && !slices.Contains(okCodes, res.StatusCode) {
		resBody := []byte{}
		if res.Body != nil {
			resBody, _ = io.ReadAll(res.Body)
		}
		res.Body.Close() // nolint: errcheck
		responseError := &ResponseError{
			StatusCode: res.StatusCode,
			Body:       resBody,
			URL:        req.URL.String(),
			Method:     req.Method,
		}
		return res, responseError
	}
	return res, nil
}

func (c *ManagerClient) GetBodyFromRequest(ctx context.Context, req *http.Request, okCodes []int) ([]byte, error) {
	var resBody []byte
	response, err := c.DoRequest(ctx, req, okCodes)
	if response != nil && response.Body != nil {
		defer response.Body.Close() // nolint: errcheck
		resBody, _ = io.ReadAll(response.Body)
	}
	return resBody, err
}

// Get performs a GET request to the specified path and decodes the response
func (c *ManagerClient) Get(ctx context.Context, url *neturl.URL, returnData any, okCodes []int) error {
	resBody, err := c.GetResponseBody(ctx, url, okCodes)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(resBody, returnData); err != nil {
		return fmt.Errorf(
			"failed to decode response from: %v. \nresponse: %v \twith error: %v",
			url.String(), string(resBody), err)
	}
	return err
}

func (c *ManagerClient) GetResponseBody(ctx context.Context, url *neturl.URL, okCodes []int) ([]byte, error) {
	req, err := c.CreateRequestWithAuth(
		url,
		http.MethodGet,
		nil,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create request %v \twith error: %v", url.String(), err)
	}
	return c.GetBodyFromRequest(ctx, req, okCodes)
}

func (c *ManagerClient) Post(
	ctx context.Context,
	url *neturl.URL,
	body io.Reader,
	returnData any,
	okCodes []int) error {
	response, err := c.PostWithResponse(ctx, url, body, okCodes)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(response, returnData); err != nil {
		return fmt.Errorf("failed to decode response from: %v \nresponse: %v \nwith error: %v", url.String(), response, err)
	}
	return err
}

func (c *ManagerClient) Put(ctx context.Context, url *neturl.URL, body io.Reader, returnData any, okCodes []int) error {
	response, err := c.PutWithResponse(ctx, url, body, okCodes)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(response, returnData); err != nil {
		return fmt.Errorf("failed to decode response from: %v. \nresponse: %v \twith error: %v", url.String(), response, err)
	}
	return err
}

func (c *ManagerClient) PutWithResponse(
	ctx context.Context,
	url *neturl.URL,
	body io.Reader,
	okCodes []int,
) ([]byte, error) {
	req, err := c.CreateRequestWithAuth(
		url,
		http.MethodPut,
		body,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create request %v \twith error: %v", url.String(), err)
	}
	return c.GetBodyFromRequest(ctx, req, okCodes)
}

func (c *ManagerClient) PostWithResponse(
	ctx context.Context,
	url *neturl.URL,
	body io.Reader,
	okCodes []int,
) ([]byte, error) {
	req, err := c.CreateRequestWithAuth(
		url,
		http.MethodPost,
		body,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create request %v \twith error: %v", url.String(), err)
	}
	return c.GetBodyFromRequest(ctx, req, okCodes)
}

func (c *ManagerClient) CreateRequestWithAuth(
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
			"Content-Type":  []string{"application/json"},
			"X-Api-Version": []string{"1400"},
			"Accept":        []string{"application/json"},
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

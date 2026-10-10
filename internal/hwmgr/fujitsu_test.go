// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package hwmgr

import (
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"testing"

	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
)

type fujitsuRoundTripper func(*http.Request) (*http.Response, error)

func (f fujitsuRoundTripper) RoundTrip(
	req *http.Request,
) (*http.Response, error) {
	return f(req)
}

func newFujitsuTestClient(
	t *testing.T,
	handler fujitsuRoundTripper,
) *FujitsuClient {
	t.Helper()

	return &FujitsuClient{
		client: &client{
			httpClient: &http.Client{
				Transport: handler,
			},
			parsedURL: &url.URL{
				Scheme: "https",
				Host:   "fujitsu.example.com",
			},
			username:  "admin",
			password:  "password",
			basicAuth: true,
		},
	}
}

func fujitsuResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func TestFujitsuListServers(t *testing.T) {
	requests := 0

	client := newFujitsuTestClient(t, func(req *http.Request) (*http.Response, error) {
		requests++

		switch req.URL.Path {
		case "/redfish/v1/Systems":
			return fujitsuResponse(http.StatusOK, `{
				"Members": [
					{
						"@odata.id": "/redfish/v1/Systems/1"
					}
				]
			}`), nil

		case "/redfish/v1/Systems/1":
			return fujitsuResponse(http.StatusOK, `{
				"Id": "1",
				"Name": "FJC640N4DC",
				"Model": "PRIMERGY RX2540 M8",
				"SerialNumber": "YLPK123456"
			}`), nil

		default:
			t.Fatalf("unexpected request path: %s", req.URL.Path)
			return nil, nil
		}
	})

	devices, err := client.ListServers()
	if err != nil {
		t.Fatalf("ListServers() returned error: %v", err)
	}

	if len(devices) != 1 {
		t.Fatalf("expected 1 device, got %d", len(devices))
	}

	device := devices[0]

	if device.Name != "FJC640N4DC" {
		t.Errorf("expected name FJC640N4DC, got %q", device.Name)
	}

	if device.Hostname != "fujitsu.example.com" {
		t.Errorf("expected hostname fujitsu.example.com, got %q", device.Hostname)
	}

	if device.Model != "PRIMERGY RX2540 M8" {
		t.Errorf(
			"expected model PRIMERGY RX2540 M8, got %q",
			device.Model,
		)
	}

	if requests != 2 {
		t.Errorf("expected 2 requests, got %d", requests)
	}
}

func TestFujitsuListServersPreservesSerialNumber(t *testing.T) {
	client := newFujitsuTestClient(t, func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/redfish/v1/Systems":
			return fujitsuResponse(http.StatusOK, `{
				"Members": [
					{
						"@odata.id": "/redfish/v1/Systems/1"
					}
				]
			}`), nil

		case "/redfish/v1/Systems/1":
			return fujitsuResponse(http.StatusOK, `{
				"Id": "1",
				"Name": "FujitsuServer",
				"Model": "PRIMERGY RX2540 M8",
				"SerialNumber": "SERIAL-12345"
			}`), nil

		default:
			t.Fatalf("unexpected request path: %s", req.URL.Path)
			return nil, nil
		}
	})

	devices, err := client.ListServers()
	if err != nil {
		t.Fatalf("ListServers() returned error: %v", err)
	}

	if len(devices) != 1 {
		t.Fatalf("expected 1 device, got %d", len(devices))
	}

	if devices[0].Serial != "SERIAL-12345" {
		t.Errorf(
			"expected serial number SERIAL-12345, got %q",
			devices[0].Serial,
		)
	}
}

func TestFujitsuListServersRequiresODataID(t *testing.T) {
	client := newFujitsuTestClient(t, func(req *http.Request) (*http.Response, error) {
		return fujitsuResponse(http.StatusOK, `{
			"Members": [
				{}
			]
		}`), nil
	})

	_, err := client.ListServers()
	if err == nil {
		t.Fatal("expected error for member without @odata.id")
	}

	if !strings.Contains(err.Error(), "@odata.id") {
		t.Errorf(
			"expected @odata.id error, got %q",
			err.Error(),
		)
	}
}

func TestFujitsuListServersInvalidJSON(t *testing.T) {
	client := newFujitsuTestClient(t, func(req *http.Request) (*http.Response, error) {
		return fujitsuResponse(
			http.StatusOK,
			`invalid json`,
		), nil
	})

	_, err := client.ListServers()
	if err == nil {
		t.Fatal("expected JSON parsing error")
	}
}

func TestFujitsuListServersHTTPError(t *testing.T) {
	client := newFujitsuTestClient(t, func(req *http.Request) (*http.Response, error) {
		return fujitsuResponse(
			http.StatusUnauthorized,
			`{"__all__":["authentication failed"]}`,
		), nil
	})

	_, err := client.ListServers()
	if err == nil {
		t.Fatal("expected HTTP error")
	}

	if !strings.Contains(err.Error(), "authentication failed") {
		t.Errorf(
			"expected authentication error, got %q",
			err.Error(),
		)
	}
}

func TestFujitsuUsesBasicAuthentication(t *testing.T) {
	client := newFujitsuTestClient(t, func(req *http.Request) (*http.Response, error) {
		username, password, ok := req.BasicAuth()

		if !ok {
			t.Fatal("expected HTTP Basic Authentication")
		}

		if username != "admin" {
			t.Errorf("expected username admin, got %q", username)
		}

		if password != "password" {
			t.Errorf("expected password password, got %q", password)
		}

		return fujitsuResponse(http.StatusOK, `{
			"Members": []
		}`), nil
	})

	_, err := client.ListServers()
	if err != nil {
		t.Fatalf("ListServers() returned error: %v", err)
	}
}

func TestFujitsuImportServer(t *testing.T) {
	client := newFujitsuTestClient(t, func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/redfish/v1/Systems":
			return fujitsuResponse(http.StatusOK, `{
                "Members": [
                    {
                        "@odata.id": "/redfish/v1/Systems/1"
                    }
                ]
            }`), nil

		case "/redfish/v1/Systems/1":
			return fujitsuResponse(http.StatusOK, `{
                "Id": "1",
                "Name": "FujitsuServer",
                "Model": "PRIMERGY RX2540 M8",
                "SerialNumber": "SERIAL-12345"
            }`), nil

		default:
			t.Fatalf("unexpected request path: %s", req.URL.Path)
			return nil, nil
		}
	})

	ip := metalv1alpha1.IP{
		Addr: netip.MustParseAddr("192.168.1.100"),
	}

	err := client.ImportServer(
		"FujitsuServer",
		ip,
		"admin",
		"password",
	)
	if err != nil {
		t.Fatalf("ImportServer() returned error: %v", err)
	}
}

func TestFujitsuImportServerNoSystems(t *testing.T) {
	client := newFujitsuTestClient(t, func(req *http.Request) (*http.Response, error) {
		return fujitsuResponse(http.StatusOK, `{
			"Members": []
		}`), nil
	})

	ip := metalv1alpha1.IP{
		Addr: netip.MustParseAddr("192.168.1.100"),
	}

	err := client.ImportServer(
		"FujitsuServer",
		ip,
		"admin",
		"password",
	)

	if err == nil {
		t.Fatal("expected ImportServer() to fail when no systems are found")
	}

	if !strings.Contains(err.Error(), "no Redfish systems found") {
		t.Errorf(
			"expected no systems error, got %q",
			err.Error(),
		)
	}
}

func TestFujitsuImportServerAsync(t *testing.T) {
	client := newFujitsuTestClient(t, func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/redfish/v1/Systems":
			return fujitsuResponse(http.StatusOK, `{
				"Members": [
					{
						"@odata.id": "/redfish/v1/Systems/1"
					}
				]
			}`), nil

		case "/redfish/v1/Systems/1":
			return fujitsuResponse(http.StatusOK, `{
				"Id": "1",
				"Name": "FujitsuServer",
				"Model": "PRIMERGY RX2540 M8",
				"SerialNumber": "SERIAL-12345"
			}`), nil

		default:
			t.Fatalf("unexpected request path: %s", req.URL.Path)
			return nil, nil
		}
	})

	ip := metalv1alpha1.IP{
		Addr: netip.MustParseAddr("192.168.1.100"),
	}

	jobID, err := client.ImportServerAsync(
		"FujitsuServer",
		ip,
		"admin",
		"password",
	)

	if err != nil {
		t.Fatalf("ImportServerAsync() returned error: %v", err)
	}

	if jobID != "" {
		t.Errorf("expected empty job ID for synchronous operation, got %q", jobID)
	}
}

func TestFujitsuRemoveServer(t *testing.T) {
	client := newFujitsuTestClient(t, func(req *http.Request) (*http.Response, error) {
		t.Fatal("RemoveServer should not make a Redfish request")
		return nil, nil
	})

	ip := metalv1alpha1.IP{
		Addr: netip.MustParseAddr("192.168.1.100"),
	}

	err := client.RemoveServer(
		"FujitsuServer",
		ip,
	)

	if err != nil {
		t.Fatalf("RemoveServer() returned error: %v", err)
	}
}

func TestFujitsuRemoveServerAsync(t *testing.T) {
	client := newFujitsuTestClient(t, func(req *http.Request) (*http.Response, error) {
		t.Fatal("RemoveServerAsync should not make a Redfish request")
		return nil, nil
	})

	ip := metalv1alpha1.IP{
		Addr: netip.MustParseAddr("192.168.1.100"),
	}

	jobID, err := client.RemoveServerAsync(
		"FujitsuServer",
		ip,
	)

	if err != nil {
		t.Fatalf("RemoveServerAsync() returned error: %v", err)
	}

	if jobID != "" {
		t.Errorf("expected empty job ID, got %q", jobID)
	}
}

func TestFujitsuGetAuthToken(t *testing.T) {
	client := newFujitsuTestClient(t, func(req *http.Request) (*http.Response, error) {
		t.Fatal("GetAuthToken should not make an HTTP request")
		return nil, nil
	})

	token, err := client.GetAuthToken()
	if err != nil {
		t.Fatalf("GetAuthToken() returned error: %v", err)
	}

	if token != "" {
		t.Errorf("expected empty token, got %q", token)
	}
}

func TestFujitsuGetJobStatus(t *testing.T) {
	client := newFujitsuTestClient(t, nil)

	jobInfo, err := client.GetJobStatus("job-123")
	if err != nil {
		t.Fatalf("GetJobStatus() returned error: %v", err)
	}

	if jobInfo == nil {
		t.Fatal("expected JobInfo, got nil")
	}

	if jobInfo.JobID != "job-123" {
		t.Errorf(
			"expected job ID job-123, got %q",
			jobInfo.JobID,
		)
	}

	if jobInfo.Status != jobStatusCompleted {
		t.Errorf(
			"expected status %q, got %q",
			jobStatusCompleted,
			jobInfo.Status,
		)
	}

	if jobInfo.Progress != 100 {
		t.Errorf(
			"expected progress 100, got %d",
			jobInfo.Progress,
		)
	}
}

func TestFujitsuIsJobComplete(t *testing.T) {
	client := newFujitsuTestClient(t, nil)

	jobInfo := &JobInfo{
		JobID:    "job-123",
		Status:   jobStatusCompleted,
		Progress: 100,
	}

	if !client.IsJobComplete(jobInfo) {
		t.Error("expected synchronous Fujitsu job to be complete")
	}
}

func TestFujitsuIsJobSuccessful(t *testing.T) {
	client := newFujitsuTestClient(t, nil)

	jobInfo := &JobInfo{
		JobID:    "job-123",
		Status:   jobStatusCompleted,
		Progress: 100,
	}

	if !client.IsJobSuccessful(jobInfo) {
		t.Error("expected synchronous Fujitsu job to be successful")
	}
}

func TestNewFujitsuClient(t *testing.T) {
	client, err := NewFujitsuClient(ClientOptions{
		Endpoint: "https://irmc.example.com",
		Username: "admin",
		Password: "password",
	})
	if err != nil {
		t.Fatalf("NewFujitsuClient() returned error: %v", err)
	}

	if client == nil {
		t.Fatal("expected non-nil client")
	}

	if !client.client.basicAuth {
		t.Error("expected basicAuth to be true for Fujitsu iRMC")
	}
}

func TestFujitsuGetSystemEmptyId(t *testing.T) {
	client := newFujitsuTestClient(t, func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/redfish/v1/Systems":
			return fujitsuResponse(http.StatusOK, `{
				"Members": [
					{
						"@odata.id": "/redfish/v1/Systems/0"
					}
				]
			}`), nil

		case "/redfish/v1/Systems/0":
			return fujitsuResponse(http.StatusOK, `{
				"Id": "",
				"Name": "RMManager",
				"Model": "PRIMERGY RX2540 M8",
				"SerialNumber": "EWCU001166"
			}`), nil

		default:
			t.Fatalf("unexpected request path: %s", req.URL.Path)
			return nil, nil
		}
	})

	_, err := client.ListServers()
	if err == nil {
		t.Fatal("expected error for system with empty Id")
	}

	if !strings.Contains(err.Error(), "has no Id") {
		t.Errorf("expected 'has no Id' error, got %q", err.Error())
	}
}

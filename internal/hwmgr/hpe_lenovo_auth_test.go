// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package hwmgr_test

import (
	"os"
	"testing"

	"github.com/ironcore-dev/metal-maintenance-operator/internal/hwmgr"
)

func TestHPEAuth(t *testing.T) {
	endpoint := os.Getenv("HPE_ENDPOINT")
	username := os.Getenv("HPE_USERNAME")
	password := os.Getenv("HPE_PASSWORD")
	token := os.Getenv("HPE_TOKEN")
	if endpoint == "" || (username == "" && token == "") {
		t.Skip("HPE_ENDPOINT and (HPE_USERNAME+HPE_PASSWORD or HPE_TOKEN) not set")
	}

	client, err := hwmgr.NewHPEClient(hwmgr.ClientOptions{
		Endpoint:           endpoint,
		Username:           username,
		Password:           password,
		Token:              token,
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	tok, err := client.GetAuthToken()
	if err != nil {
		t.Fatalf("GetAuthToken failed: %v", err)
	}
	t.Logf("token obtained: %s", tok)

	servers, err := client.ListServers()
	if err != nil {
		t.Fatalf("ListServers failed: %v", err)
	}
	t.Logf("found %d servers", len(servers))
}

func TestLenovoAuth(t *testing.T) {
	endpoint := os.Getenv("LENOVO_ENDPOINT")
	username := os.Getenv("LENOVO_USERNAME")
	password := os.Getenv("LENOVO_PASSWORD")
	if endpoint == "" || username == "" || password == "" {
		t.Skip("LENOVO_ENDPOINT, LENOVO_USERNAME, LENOVO_PASSWORD not set")
	}

	client, err := hwmgr.NewLenovoClient(hwmgr.ClientOptions{
		Endpoint:           endpoint,
		Username:           username,
		Password:           password,
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	tok, err := client.GetAuthToken()
	if err != nil {
		t.Fatalf("GetAuthToken failed: %v", err)
	}
	t.Logf("token obtained: %s", tok)

	servers, err := client.ListServers()
	if err != nil {
		t.Fatalf("ListServers failed: %v", err)
	}
	t.Logf("found %d servers", len(servers))
}

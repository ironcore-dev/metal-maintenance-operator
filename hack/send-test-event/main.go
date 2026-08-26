// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

// send-test-event fires a Redfish SubmitTestEvent against a real iDRAC
// using the same code path as the operator's health-check loop.
//
// Usage:
//
//	go run ./hack/send-test-event \
//	  -endpoint https://<bmc-ip> \
//	  -username <user> \
//	  -password <pass> \
//	  -message-id SYS1000
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	metalbmc "github.com/ironcore-dev/metal-operator/bmc"
	"github.com/stmcginnis/gofish"
	"github.com/stmcginnis/gofish/schemas"
)

func main() {
	endpoint := flag.String("endpoint", "", "BMC base URL, e.g. https://10.0.0.1")
	username := flag.String("username", "", "BMC username")
	password := flag.String("password", "", "BMC password")
	messageID := flag.String("message-id", "SYS1000", "Redfish MessageId to send")
	insecure := flag.Bool("insecure", true, "Skip TLS verification")
	flag.Parse()

	if *endpoint == "" || *username == "" || *password == "" {
		fmt.Fprintln(os.Stderr, "usage: send-test-event -endpoint <url> -username <u> -password <p> [-message-id <id>]")
		os.Exit(1)
	}

	ctx := context.Background()

	base, err := metalbmc.NewRedfishBMCClient(ctx, metalbmc.Options{
		Endpoint:    *endpoint,
		Username:    *username,
		Password:    *password,
		BasicAuth:   true,
		InsecureTLS: *insecure,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect: %v\n", err)
		os.Exit(1)
	}
	defer base.Logout()

	accessor, ok := base.(interface{ Client() *gofish.APIClient })
	if !ok {
		fmt.Fprintf(os.Stderr, "bmc client %T does not expose Client(); cannot send test event\n", base)
		os.Exit(1)
	}
	api := accessor.Client()

	svc := api.GetService()
	if svc == nil {
		fmt.Fprintln(os.Stderr, "no service root")
		os.Exit(1)
	}
	es, err := svc.EventService()
	if err != nil {
		fmt.Fprintf(os.Stderr, "event service: %v\n", err)
		os.Exit(1)
	}

	resp, err := es.SubmitTestEvent(&schemas.EventServiceSubmitTestEventParameters{
		MessageID: *messageID,
		Message:   "metal-maintenance-operator pipeline health check",
		Severity:  "Informational",
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "SubmitTestEvent: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("OK — response: %+v\n", resp)
}

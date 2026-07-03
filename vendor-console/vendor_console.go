// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
//
// SPDX-License-Identifier: Apache-2.0

package vendorconsole

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	mgrClient "github.com/ironcore-dev/metal-maintenance-operator/vendor-console/client"
	"github.com/ironcore-dev/metal-maintenance-operator/vendor-console/ome"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

type Manufacturer string

const (
	ManufacturerDell   Manufacturer = "Dell Inc."
	ManufacturerLenovo Manufacturer = "Lenovo"
	ManufacturerHPE    Manufacturer = "HPE"
)

type Config struct {
	InsecureSkipVerify  bool
	TLSHandshakeTimeout time.Duration
	ReuseConnections    bool
}

type VendorConsole = any

func GetDellConsole(ctx context.Context, config *mgrClient.Config, auth *mgrClient.AuthToken) (*ome.OME, error) {
	mfgConsole := &ome.OME{
		Client: &mgrClient.ManagerClient{
			Client: mgrClient.CreateClient(config),
			Config: config,
			Auth:   auth,
		},
	}
	if auth.Token != "" {
		mfgConsole.Client.Auth.Token = auth.Token
		session, err := mfgConsole.GetSession(ctx)
		if session != nil && err == nil {
			mfgConsole.Client.Auth.SessionId = session.Id
			return mfgConsole, nil
		}
		var reqErr *mgrClient.ResponseError
		if err != nil {
			if errors.As(err, &reqErr) && reqErr.StatusCode == http.StatusUnauthorized {
				log := logf.FromContext(ctx)
				log.V(1).Info("existing token is invalid, need to re-authorize", "status code", reqErr.StatusCode)
			} else {
				return nil, fmt.Errorf("failed to validate existing token with error: %v, %w", auth, err)
			}
		} else {
			return mfgConsole, nil
		}
	}
	dellAuthBody := map[string]string{
		"UserName":    auth.Username,
		"Password":    auth.Password,
		"SessionType": "API",
	}
	if err := mfgConsole.Client.CreateSession(
		ctx,
		config.URL.JoinPath(ome.SessionURL),
		dellAuthBody, mgrClient.DellToken,
		[]int{http.StatusCreated, http.StatusUnauthorized},
	); err != nil {
		return nil, fmt.Errorf("failed to create session with error: %w", err)
	}
	session, err := mfgConsole.GetSession(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to validate token with error: %w", err)
	}
	if session != nil {
		mfgConsole.Client.Auth.SessionId = session.Id
		return mfgConsole, nil
	}
	return mfgConsole, nil
}

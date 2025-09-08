package managerconsole

import (
	"fmt"
	"net/http"
	"time"

	mgrClient "github.com/ironcore-dev/maintenance-operator/ManagerConsole/client"
	"github.com/ironcore-dev/maintenance-operator/ManagerConsole/ome"
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

type ManagerConsole interface {
}

func GetDellConsole(config *mgrClient.Config, auth *mgrClient.AuthToken) (*ome.OME, error) {
	mfgConsole := &ome.OME{
		Client: &mgrClient.ManagerClient{
			Client: mgrClient.CreateClient(config),
		},
		Config: config,
	}
	dellAuthBody := map[string]string{
		"UserName":    auth.Username,
		"Password":    auth.Password,
		"SessionType": "API",
	}
	if err := mfgConsole.Client.CreateSession(
		config.URL.JoinPath(ome.SessionURL),
		dellAuthBody, mgrClient.DellToken,
		[]int{http.StatusCreated},
	); err != nil {
		return nil, fmt.Errorf("failed to create session with error: %w", err)
	}
	return mfgConsole, nil

}

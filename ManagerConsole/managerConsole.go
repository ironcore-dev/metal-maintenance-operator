package managerconsole

import (
	"fmt"
	"net/http"

	mgrClient "github.com/ironcore-dev/maintenance-operator/ManagerConsole/client"
	"github.com/ironcore-dev/maintenance-operator/ManagerConsole/ome"
)

type Manufacturer string

const (
	ManufacturerDell   Manufacturer = "Dell Inc."
	ManufacturerLenovo Manufacturer = "Lenovo"
	ManufacturerHPE    Manufacturer = "HPE"
)

type ManagerConsole interface {
}

func GetManagerConsole(manufacturer string, config *mgrClient.Config, auth *mgrClient.AuthToken) (ManagerConsole, error) {
	switch manufacturer {
	case string(ManufacturerDell):
		mfgConsole := &ome.OME{
			Client: &mgrClient.ManagerClient{
				Client: mgrClient.CreateClient(config),
			},
			Config: config,
		}
		ome.DellAuthBody["UserName"] = auth.Username
		ome.DellAuthBody["Password"] = auth.Password
		if err := mfgConsole.Client.CreateSession(config.URL.JoinPath(ome.SessionURL), ome.DellAuthBody, mgrClient.DellToken, []int{http.StatusCreated}); err != nil {
			return nil, fmt.Errorf("failed to create session with error: %w", err)
		}
		return mfgConsole, nil
	default:
		return nil, fmt.Errorf("unsupported manufacturer: %v", manufacturer)
	}
}

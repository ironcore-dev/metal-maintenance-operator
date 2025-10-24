package servermanagement

import (
	"github.com/HewlettPackard/oneview-golang/ov" // This is a common SDK path
	"github.com/HewlettPackard/oneview-golang/utils"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
)

type HPEClient struct {
	client *ov.OVClient
}

func NewHPEClient(options ClientOptions) (c *HPEClient, err error) {
	var ClientOV *ov.OVClient
	c = &HPEClient{}
	ovc := ClientOV.NewOVClient(
		options.Username,
		options.Password,
		"options.Domain",
		options.Endpoint,
		options.InsecureSkipVerify,
		1,
		"",
	)
	c.client.APIKey = options.Token
	c.client = ovc
	return
}

func (c *HPEClient) ImportServer(hostname string, IP metalv1alpha1.IP, bmcUser, bmcPassword string) error {
	scp, _ := c.client.GetScopeByName("ScopeHardware")
	//hostname:  node005r-bb097.cc.qa-de-1.cloud.sap
	rackServer := ov.ServerHardware{
		Name:               hostname,
		Username:           bmcUser,
		Password:           bmcPassword,
		Force:              false,
		LicensingIntent:    "OneView", //OneView or OneViewNoiLO for Managed
		ConfigurationState: "Managed",
		InitialScopeUris:   []utils.Nstring{scp.URI},
	}
	_, err := c.client.AddRackServer(rackServer)
	return err
}

func (c *HPEClient) RemoveServer(hostname string, ip metalv1alpha1.IP) error {
	server, err := c.client.GetServerHardwareByName(hostname)
	if err != nil {
		return err
	}
	return c.client.DeleteServerHardware(server.URI)
}

func (c *HPEClient) ListServers() ([]Device, error) {
	var devices []Device
	filters := []string{""}
	hpeServers, err := c.client.GetServerHardwareList(filters, "", "", "", "")
	if err != nil {
		return devices, err
	}
	for _, srv := range hpeServers.Members {
		device := Device{
			//ID:       srv.UUID.String(),
			Hostname: srv.Hostname,
			Name:     srv.Name,
			Model:    srv.Model,
		}
		devices = append(devices, device)
	}
	return devices, nil
}

func (c *HPEClient) GetAuthToken() (string, error) {
	_, err := c.client.GetIdleTimeout()
	if err != nil {
		c.client.RefreshLogin()
		return "", err
	}
	return c.client.APIKey, nil
}

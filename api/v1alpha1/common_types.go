package v1alpha1

import (
	"github.com/stmcginnis/gofish/common"
	"github.com/stmcginnis/gofish/redfish"
)

// Task contains the status of the task created by the BMC for the BIOS upgrade.
type Task struct {
	// URI is the URI of the task created by the BMC for the BIOS upgrade.
	// +optional
	URI string `json:"URI,omitempty"`

	// State is the current state of the task.
	// +optional
	State redfish.TaskState `json:"state,omitempty"`

	// Status is the current status of the task.
	// +optional
	Status common.Health `json:"status,omitempty"`

	// PercentComplete is the percentage of completion of the task.
	// +optional
	PercentComplete int32 `json:"percentageComplete,omitempty"`
}

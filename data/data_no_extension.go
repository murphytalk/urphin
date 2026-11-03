//go:build !extension

package data

import (
	"fmt"

	databackend "github.com/murphytalk/urphin/data/backend"
	c "github.com/murphytalk/urphin/internal"
)

func connectToExtendedDataBackend(backendType databackend.BackendType, backendName databackend.BackendName, config *c.Config) (databackend.DataBackend, error) {
	return nil, fmt.Errorf("data backend %q requires the extension build", backendType)
}

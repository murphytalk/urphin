//go:build extension

package data

import (
	databackend "github.com/murphytalk/urphin/data/backend"
	extensiondata "github.com/murphytalk/urphin/extension/data"
	c "github.com/murphytalk/urphin/internal"
)

func connectToExtendedDataBackend(backendType databackend.BackendType, backendName databackend.BackendName, config *c.Config) (databackend.DataBackend, error) {
	return extensiondata.ConnectToExtendedDataBackend(backendType, backendName, config)
}

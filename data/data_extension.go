//go:build extension

package data

import (
	databackend "github.com/murphytalk/urphin/data/backend"
	extensiondata "github.com/murphytalk/urphin/extension/data"
	c "github.com/murphytalk/urphin/internal"
)

func connectToExtendedDataBackend(backendName string, connName string, config c.Config) (databackend.DataBackend, error) {
	return extensiondata.ConnectToExtendedDataBackend(backendName, connName, config)
}

//go:build extension

package mktdata

import (
	extensionmktfeed "github.com/murphytalk/urphin/extension/mktfeed"
)

func regExtendedMktFeedFactories(factories MktFeedFactoryMap) {
	extensionmktfeed.RegExtendedMktFeedFactories(factories)
}

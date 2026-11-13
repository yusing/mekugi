package appserver

import "github.com/yusing/mekugi/contracts/observation"

// FeatureUse is a detached native snapshot. It is never checkpointed or incremented in Go.
type FeatureUse = observation.FeatureUse

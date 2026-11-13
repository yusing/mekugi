package appserver

import "github.com/yusing/mekugi/contracts/observation"

// Native wire names share the read-only facts used by retained replay.
type CoordinationProjection = observation.CoordinationProjection
type CoordinationInterruptRequest = observation.CoordinationInterruptRequest
type CoordinationTarget = observation.CoordinationTarget
type CoordinationTerminalDelivery = observation.CoordinationTerminalDelivery
type CoordinationWaitSettlement = observation.CoordinationWaitSettlement
type CoordinationWaitOrigin = observation.CoordinationWaitOrigin
type CoordinationWaitResult = observation.CoordinationWaitResult
type CoordinationWaitCancellation = observation.CoordinationWaitCancellation
type CoordinationWaitMessage = observation.CoordinationWaitMessage
type CoordinationWaitOutcome = observation.CoordinationWaitOutcome
type AgentCoordination = observation.AgentCoordination

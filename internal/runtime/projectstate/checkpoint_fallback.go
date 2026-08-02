//go:build linux || darwin

package projectstate

type checkpointPublicationStrategy uint8

const (
	checkpointPublicationExchange checkpointPublicationStrategy = iota + 1
	checkpointPublicationFallback
)

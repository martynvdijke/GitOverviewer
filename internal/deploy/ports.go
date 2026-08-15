package deploy

import (
	"fmt"
	"strconv"
	"strings"
)

// firstPublishedPort returns the container port (the key part of the first
// entry of the container's port bindings, e.g. "8080" from "8080/tcp").
// It returns 0 when the container has no published ports.
func firstPublishedPort(c containerInspect) (int, error) {
	for key := range c.HostConfig.PortBindings {
		// Key format: "8080/tcp" or "8080/udp"; strip the protocol.
		port := key
		if idx := strings.IndexByte(port, '/'); idx >= 0 {
			port = port[:idx]
		}
		n, err := strconv.Atoi(port)
		if err != nil {
			continue // non-numeric key, skip
		}
		if n <= 0 || n > 65535 {
			continue
		}
		return n, nil
	}
	return 0, fmt.Errorf("no published ports")
}

// portForContainer returns the first published container port for the given
// inspect data, or 0 if none is published.
func portForContainer(c containerInspect) int {
	p, err := firstPublishedPort(c)
	if err != nil {
		return 0
	}
	return p
}

package deploy

import (
	"encoding/json"
	"fmt"
)

// containerConfig is the Config section of docker inspect.
type containerConfig struct {
	Image        string
	Hostname     string
	User         string
	WorkingDir   string
	Env          []string
	Labels       map[string]string
	ExposedPorts map[string]struct{}
}

// containerRestartPolicy is the RestartPolicy section of HostConfig.
type containerRestartPolicy struct {
	Name              string
	MaximumRetryCount int
}

// containerPortBinding is one entry of a port's bindings.
type containerPortBinding struct {
	HostIP   string `json:"HostIp"`
	HostPort string
}

// containerHostConfig is the HostConfig section of docker inspect.
type containerHostConfig struct {
	NetworkMode   string
	RestartPolicy containerRestartPolicy
	PortBindings  map[string][]containerPortBinding
	ExtraHosts    []string
}

// containerMount is one entry of the Mounts array of docker inspect.
type containerMount struct {
	Type        string // "bind", "volume" or "tmpfs"
	Name        string // volume name, when Type == "volume"
	Source      string // host path, when Type == "bind"
	Destination string
	ReadOnly    bool
}

// containerNetwork describes one attached network in NetworkSettings.
type containerNetwork struct {
	Aliases []string
}

// containerNetworkSettings is the NetworkSettings section of docker inspect.
type containerNetworkSettings struct {
	Networks map[string]containerNetwork
}

// containerInspect is the subset of `docker inspect` output we need to
// recreate a container with its runtime configuration preserved. It is also
// used for label-based target discovery.
type containerInspect struct {
	ID              string
	Name            string
	Config          containerConfig
	HostConfig      containerHostConfig
	Mounts          []containerMount
	NetworkSettings containerNetworkSettings
}

// parseInspect parses `docker inspect` output for a single container.
func parseInspect(raw []byte) (*containerInspect, error) {
	var out []containerInspect
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no container found")
	}
	return &out[0], nil
}

// containerCreateArgs builds the `docker create` arguments (excluding the
// container name and image) that preserve the runtime configuration of the
// inspected container: env, mounts, port bindings, exposed ports, networks
// and aliases, restart policy, labels, user, working dir, hostname and extra
// hosts.
func containerCreateArgs(c *containerInspect) []string {
	var args []string

	for _, e := range c.Config.Env {
		args = append(args, "-e", e)
	}
	for k, v := range c.Config.Labels {
		args = append(args, "--label", k+"="+v)
	}
	for port := range c.Config.ExposedPorts {
		args = append(args, "--expose", port)
	}
	if c.Config.WorkingDir != "" {
		args = append(args, "--workdir", c.Config.WorkingDir)
	}
	if c.Config.User != "" {
		args = append(args, "--user", c.Config.User)
	}
	if c.Config.Hostname != "" {
		args = append(args, "--hostname", c.Config.Hostname)
	}
	for _, m := range c.Mounts {
		switch m.Type {
		case "volume":
			args = append(args, "-v", mountSpec(m.Name, m.Destination, m.ReadOnly))
		case "tmpfs":
			args = append(args, "--tmpfs", m.Destination)
		default: // "bind" (and anything unknown → treat as bind)
			args = append(args, "-v", mountSpec(m.Source, m.Destination, m.ReadOnly))
		}
	}
	for port, binds := range c.HostConfig.PortBindings {
		for _, b := range binds {
			host := b.HostPort
			if b.HostIP != "" && b.HostIP != "0.0.0.0" && b.HostIP != "::" {
				host = b.HostIP + ":" + host
			}
			args = append(args, "-p", host+":"+port)
		}
	}
	if rp := c.HostConfig.RestartPolicy; rp.Name != "" {
		switch rp.Name {
		case "on-failure":
			if rp.MaximumRetryCount > 0 {
				args = append(args, "--restart", fmt.Sprintf("on-failure:%d", rp.MaximumRetryCount))
			} else {
				args = append(args, "--restart", "on-failure")
			}
		default:
			args = append(args, "--restart", rp.Name)
		}
	}
	for _, h := range c.HostConfig.ExtraHosts {
		args = append(args, "--add-host", h)
	}
	// Exclusive network modes replace the default bridge; attached user-defined
	// networks (with their aliases) are preserved from NetworkSettings.
	switch c.HostConfig.NetworkMode {
	case "host", "none":
		args = append(args, "--network", c.HostConfig.NetworkMode)
	}
	if c.HostConfig.NetworkMode != "host" && c.HostConfig.NetworkMode != "none" {
		for netName, net := range c.NetworkSettings.Networks {
			args = append(args, "--network", netName)
			for _, a := range net.Aliases {
				if a != "" {
					args = append(args, "--network-alias", a)
				}
			}
		}
	}
	return args
}

// mountSpec renders a "-v" mount spec, preserving read-only flags.
func mountSpec(src, dst string, readOnly bool) string {
	spec := src + ":" + dst
	if readOnly {
		spec += ":ro"
	}
	return spec
}

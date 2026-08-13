package deploy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

// helperTimeout bounds how long we wait for a detached helper container.
const helperTimeout = 10 * time.Minute

// shQuote quotes s for safe embedding in a POSIX sh -c script.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// selfContainerID returns the short container ID of the process, or "" when
// not running inside a Docker container (inside a container the hostname is
// the short container ID).
func selfContainerID() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return h
}

// isSelfContainer reports whether the container with the given full ID is the
// one this process runs in.
func isSelfContainer(fullID string) bool {
	self := selfContainerID()
	return self != "" && strings.HasPrefix(fullID, self)
}

// helperImage returns the image used for detached helper containers, in order:
// the DEPLOY_HELPER_IMAGE env var, the image of the container this process
// runs in, or the official docker CLI image as a last resort.
func helperImage(ctx context.Context) string {
	if v := os.Getenv("DEPLOY_HELPER_IMAGE"); v != "" {
		return v
	}
	if id := selfContainerID(); id != "" {
		out, err := execCmd(ctx, "docker", "inspect", "--format", "{{.Config.Image}}", id)
		if err == nil {
			if img := strings.TrimSpace(string(out)); img != "" {
				return img
			}
		}
	}
	return "docker:cli"
}

// runHelper executes script inside a detached helper container that mounts the
// Docker socket (and any extra mounts). The helper runs independently of the
// deployer process, so it survives the target container being stopped and
// removed — which makes self-updates possible. It waits for the helper to
// finish and returns an error when the helper exits non-zero.
func runHelper(ctx context.Context, script string, extraMounts []string) error {
	image := helperImage(ctx)
	name := "gitlens-deploy-" + randomHex(6)

	args := []string{"run", "-d", "--name", name, "-v", "/var/run/docker.sock:/var/run/docker.sock"}
	for _, m := range extraMounts {
		args = append(args, "-v", m)
	}
	args = append(args, image, "sh", "-c", script)

	log.Printf("Deploy: launching helper container %s (%s)", name, image)
	if _, err := execCmd(ctx, "docker", args...); err != nil {
		return fmt.Errorf("launch helper container failed: %w", err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, helperTimeout)
	defer cancel()

	out, waitErr := execCmd(waitCtx, "docker", "wait", name)
	logs, _ := execCmd(context.Background(), "docker", "logs", name)
	execCmd(context.Background(), "docker", "rm", "-f", name) // best-effort cleanup

	if waitErr != nil {
		return fmt.Errorf("wait helper container failed: %w", waitErr)
	}
	if exit := strings.TrimSpace(string(out)); exit != "0" {
		return fmt.Errorf("helper deploy failed (exit %s): %s", exit, strings.TrimSpace(string(logs)))
	}
	return nil
}

// randomHex returns a hex string of n random bytes (falling back to a
// timestamp when the entropy source fails).
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

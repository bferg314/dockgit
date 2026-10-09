package config

import "os/exec"

// Available reports whether cmd can be found on PATH.
func Available(cmd string) bool {
	_, err := exec.LookPath(cmd)
	return err == nil
}

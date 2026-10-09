package dock

import (
	"context"
	"strings"
)

// Info describes the daemon dockgit is talking to.
type Info struct {
	Context string // docker context name, e.g. "desktop-linux"
	Version string // server version
}

// GetInfo asks for the current context and the server's version. An error
// means the daemon can't be reached.
func GetInfo(ctx context.Context, r Runner) (Info, error) {
	var info Info
	if out, err := r.Run(ctx, "", "context", "show"); err == nil {
		info.Context = strings.TrimSpace(string(out))
	}
	out, err := r.Run(ctx, "", "version", "--format", "{{.Server.Version}}")
	if err != nil {
		return info, err
	}
	info.Version = strings.TrimSpace(string(out))
	return info, nil
}

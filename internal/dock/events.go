package dock

import (
	"bufio"
	"context"
	"encoding/json"
	"strings"
	"time"
)

// containerEvents are the lifecycle events that change what the Docker tab
// shows. exec_* events are left out: healthchecks fire them every few
// seconds.
var containerEvents = []string{
	"create", "start", "restart", "die", "stop", "kill", "oom",
	"pause", "unpause", "destroy", "rename", "update", "health_status",
}

// Event is one container lifecycle event.
type Event struct {
	ID     string
	Name   string
	Action string // "start", "die", "health_status", ...
	Time   time.Time
}

type eventJSON struct {
	Type   string `json:"Type"`
	Action string `json:"Action"`
	Actor  struct {
		ID         string            `json:"ID"`
		Attributes map[string]string `json:"Attributes"`
	} `json:"Actor"`
	TimeNano int64 `json:"timeNano"`
}

// Events streams container lifecycle events until ctx is cancelled or the
// docker command exits; then the channel is closed. The error, if any, is
// sent on errc once the channel is closed.
func Events(ctx context.Context, r Runner) (<-chan Event, <-chan error) {
	ch := make(chan Event, 64)
	errc := make(chan error, 1)
	args := []string{"events", "--format", "json", "--filter", "type=container"}
	for _, e := range containerEvents {
		args = append(args, "--filter", "event="+e)
	}
	go func() {
		defer close(ch)
		out, err := r.Stream(ctx, "", args...)
		if err != nil {
			errc <- err
			return
		}
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			e, ok := parseEvent(sc.Bytes())
			if !ok {
				continue
			}
			select {
			case ch <- e:
			case <-ctx.Done():
			}
		}
		err = out.Close()
		if ctx.Err() != nil {
			err = nil
		}
		errc <- err
	}()
	return ch, errc
}

func parseEvent(line []byte) (Event, bool) {
	var j eventJSON
	if err := json.Unmarshal(line, &j); err != nil || j.Type != "container" || j.Actor.ID == "" {
		return Event{}, false
	}
	// "health_status: healthy" -> "health_status"
	action, _, _ := strings.Cut(j.Action, ":")
	return Event{
		ID:     j.Actor.ID,
		Name:   j.Actor.Attributes["name"],
		Action: strings.TrimSpace(action),
		Time:   time.Unix(0, j.TimeNano),
	}, true
}

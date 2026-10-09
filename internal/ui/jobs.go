package ui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/bferg314/dockgit/internal/job"
)

// jobState is a build, pull, up or down running (or run) for a repo, a
// stack or a compose root. The owner keeps its latest job, so its output
// can be shown again.
type jobState struct {
	name, title string // "clock", "Build main"
	lines       []string
	done        bool
	err         error
	started     time.Time
	// onDone runs once the job finishes, with err set.
	onDone func(j *jobState) tea.Cmd

	stop context.CancelFunc
	ch   <-chan string
	errc <-chan error
	gen  int
}

func (j *jobState) running() bool { return j != nil && !j.done }

type jobLinesMsg struct {
	gen   int
	lines []string
	done  bool
	err   error
}

// runJob starts steps and stores the job in *slot, replacing a finished
// one. It refuses while the owner's previous job is still running.
func (a *App) runJob(slot **jobState, name, title string, steps []job.Step, onDone func(*jobState) tea.Cmd) tea.Cmd {
	if (*slot).running() {
		return a.notify(2, "%s is busy: %s", name, (*slot).title)
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.jobGen++
	j := &jobState{name: name, title: title, started: time.Now(), onDone: onDone, stop: cancel, gen: a.jobGen}
	j.ch, j.errc = job.Run(ctx, a.exec, steps)
	*slot = j
	a.jobs[j.gen] = j
	return tea.Batch(a.startSpinner(), waitJobLines(j))
}

func waitJobLines(j *jobState) tea.Cmd {
	gen, ch, errc := j.gen, j.ch, j.errc
	return func() tea.Msg {
		l, ok := <-ch
		if !ok {
			return jobLinesMsg{gen: gen, done: true, err: <-errc}
		}
		lines := []string{l}
		for len(lines) < 2000 {
			select {
			case l, ok := <-ch:
				if !ok {
					return jobLinesMsg{gen: gen, lines: lines}
				}
				lines = append(lines, l)
			default:
				return jobLinesMsg{gen: gen, lines: lines}
			}
		}
		return jobLinesMsg{gen: gen, lines: lines}
	}
}

func (a *App) handleJobLines(msg jobLinesMsg) tea.Cmd {
	j := a.jobs[msg.gen]
	if j == nil {
		return nil
	}
	if v := a.logView; v != nil && v.job == j {
		defer func() { v.lines, v.ended, v.err = j.lines, j.done, j.err }()
	}
	if !msg.done {
		j.lines = append(j.lines, msg.lines...)
		return waitJobLines(j)
	}

	delete(a.jobs, msg.gen)
	j.done, j.err = true, msg.err
	var cmds []tea.Cmd
	if msg.err != nil {
		j.lines = append(j.lines, "", "✗ "+msg.err.Error())
		// Failures stay on screen until dismissed.
		if a.logView == nil && a.popup == nil && a.confirmDlg == nil && !a.dialogOpen() {
			a.openJobView(j)
		}
		cmds = append(cmds, a.notify(2, "%s failed: %s", j.title, j.name))
	} else {
		j.lines = append(j.lines, "", "✓ done in "+duration(time.Since(j.started)))
		cmds = append(cmds, a.notify(1, "%s: %s", j.title, j.name))
	}
	if j.onDone != nil {
		cmds = append(cmds, j.onDone(j))
	}
	return tea.Batch(cmds...)
}

// anyJobRunning reports whether a job is in progress anywhere.
func (a *App) anyJobRunning() bool { return len(a.jobs) > 0 }

func (a *App) openJobView(j *jobState) {
	a.closeLogs()
	a.logView = &logView{name: j.name + " · " + j.title, job: j, lines: j.lines, ended: j.done, err: j.err,
		follow: true, wrap: true, search: newSearch()}
}

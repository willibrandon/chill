package main

import (
	"context"
	"io"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
)

type outputMsg struct {
	id   uint64
	line string
}

// replTask streams complete lines to the UI with bounded buffering. Cancellation
// releases a blocked writer too, so quitting never leaves a diagnostic running.
type replTask struct {
	id      uint64
	ctx     context.Context
	cancel  context.CancelFunc
	once    sync.Once
	output  chan string
	done    chan struct{}
	work    func(context.Context, io.Writer) error
	partial string // accessed only by the worker
	err     error  // published by closing output
}

func newREPLTask(id uint64, work func(context.Context, io.Writer) error) *replTask {
	ctx, cancel := context.WithCancel(context.Background())
	return &replTask{id: id, ctx: ctx, cancel: cancel, work: work,
		output: make(chan string, 16), done: make(chan struct{})}
}

func (task *replTask) start() {
	task.once.Do(func() {
		go func() {
			defer close(task.done)
			defer close(task.output)
			if task.err = task.ctx.Err(); task.err != nil {
				return
			}
			task.err = task.work(task.ctx, task)
			if task.partial != "" {
				task.emit(task.partial)
			}
			if task.ctx.Err() != nil {
				task.err = task.ctx.Err()
			}
		}()
	})
}

func (task *replTask) next() tea.Msg {
	task.start()
	if line, open := <-task.output; open {
		return outputMsg{id: task.id, line: line}
	}
	return resultMsg{id: task.id, err: task.err}
}

func (task *replTask) emit(line string) error {
	if err := task.ctx.Err(); err != nil {
		return err
	}
	select {
	case task.output <- line:
		return nil
	case <-task.ctx.Done():
		return task.ctx.Err()
	}
}

// Write forwards complete diagnostic lines to the REPL's asynchronous output.
func (task *replTask) Write(p []byte) (int, error) {
	task.partial += string(p)
	for {
		line, rest, found := strings.Cut(task.partial, "\n")
		if !found {
			return len(p), nil
		}
		task.partial = rest
		if err := task.emit(line); err != nil {
			return 0, err
		}
	}
}

func (task *replTask) stop() {
	task.cancel()
	task.start() // also handles quitting before Bubble Tea dispatched next
	<-task.done
}

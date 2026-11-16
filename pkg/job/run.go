package job

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/stashapp/stash/pkg/logger"
)

// Run executes a job at once, in the calling goroutine and next to whatever the
// queue is running, and returns its error. While it runs it is listed like any
// other job and can be cancelled.
//
// Start is not used for this: it leaves the finished job in the queue, and Add
// only wakes the dispatcher for the first job of an empty queue, so every job
// queued after a Start would wait forever.
func (m *Manager) Run(ctx context.Context, description string, e JobExec) (err error) {
	jobCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()

	m.mutex.Lock()
	t := time.Now()
	j := &Job{
		ID:          m.nextID(),
		Status:      StatusRunning,
		Description: description,
		AddTime:     t,
		StartTime:   &t,
		exec:        e,
		outerCtx:    ctx,
		cancelFunc:  cancel,
	}
	m.queue = append(m.queue, j)
	m.notifyNewJob(j)
	m.mutex.Unlock()

	defer func() {
		if p := recover(); p != nil {
			logger.Errorf("panic in job %d - %s: %v", j.ID, j.Description, p)
			logger.Error(string(debug.Stack()))
			err = fmt.Errorf("panic: %v", p)
		}

		m.mutex.Lock()
		defer m.mutex.Unlock()

		switch {
		case j.Status == StatusStopping:
			j.Status = StatusCancelled
		case err != nil:
			j.error(err)
		default:
			j.Status = StatusFinished
		}
		end := time.Now()
		j.EndTime = &end
		m.removeJob(j)

		// a job queued while this one was in the queue did not wake the
		// dispatcher (Add only does for the first job of an empty queue)
		m.notEmpty.Broadcast()
	}()

	return e.Execute(jobCtx, m.newProgress(j))
}

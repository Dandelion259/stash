package job

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if cond() {
			return
		}
		time.Sleep(sleepTime)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestRun(t *testing.T) {
	m := NewManager()
	ctx := context.Background()

	// a queued job is running...
	queuedFinish := make(chan struct{})
	queued := newTestExec(queuedFinish)
	m.Add(ctx, "queued", queued)
	<-queued.started

	// ...and Run does not wait for it
	ran := false
	err := m.Run(ctx, "at once", MakeJobExec(func(ctx context.Context, progress *Progress) error {
		ran = true
		assert.Len(t, m.GetQueue(), 2)
		return nil
	}))
	assert.NoError(t, err)
	assert.True(t, ran)
	assert.Len(t, m.GetQueue(), 1)

	// its error comes back
	want := errors.New("failed")
	assert.Equal(t, want, m.Run(ctx, "failing", MakeJobExec(func(ctx context.Context, progress *Progress) error { return want })))
	assert.Len(t, m.GetQueue(), 1)

	close(queuedFinish)
	waitFor(t, "the queued job to leave", func() bool { return len(m.GetQueue()) == 0 })

	// A job queued while a Run is the only job in the list must still be
	// started: Add wakes the dispatcher only for the first job of an empty queue.
	inRun := make(chan struct{})
	finishRun := make(chan struct{})
	runDone := make(chan struct{})
	go func() {
		_ = m.Run(ctx, "slow", MakeJobExec(func(ctx context.Context, progress *Progress) error {
			close(inRun)
			<-finishRun
			return nil
		}))
		close(runDone)
	}()
	<-inRun
	laterFinish := make(chan struct{})
	later := newTestExec(laterFinish)
	m.Add(ctx, "queued behind a run", later)
	close(finishRun)
	<-runDone

	select {
	case <-later.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the job queued during a Run was never started")
	}
	close(laterFinish)
	waitFor(t, "the queue to empty", func() bool { return len(m.GetQueue()) == 0 })
}

// Package scheduler wraps robfig/cron with second-level precision and
// provides a managed job registry for recurring background tasks.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/seilbekskindirov/monitor/internal"
)

// NewScheduler creates a new Scheduler with second-level precision.
func NewScheduler(logger io.Writer) (*Scheduler, error) {
	c := cron.New(cron.WithSeconds())

	s := &Scheduler{cron: c, logger: logger}

	return s, nil
}

// Scheduler wraps robfig/cron and manages scheduled jobs.
type Scheduler struct {
	cron   *cron.Cron
	logger io.Writer
}

// Schedule registers a job to run on the given interval.
func (scheduler *Scheduler) Schedule(ctx context.Context, interval time.Duration, job job) error {
	if interval < time.Second*10 {
		err := fmt.Errorf("interval must be at least 10 second")
		err = errors.Join(err, internal.NewTraceError())
		return err
	}
	if job == nil {
		err := errors.New("job cannot be run")
		err = errors.Join(err, internal.NewTraceError())
		return err
	}

	delay := cron.Every(interval)

	scheduler.cron.Schedule(delay, cron.FuncJob(func() {
		n := job.Name()
		_, _ = fmt.Fprintf(scheduler.logger, "cron: job %s started\n", n)
		defer func(name string) { _, _ = fmt.Fprintf(scheduler.logger, "cron: job %s finished\n", name) }(n)

		defer jobRecovering()

		ctxTimeout, cancel := context.WithTimeout(ctx, interval-time.Second)
		defer cancel()

		job.Run(ctxTimeout)
	}))

	return nil
}

// Ping returns nil if the scheduler is running, error otherwise.
func (scheduler *Scheduler) Ping(_ context.Context) error {
	if scheduler.cron == nil {
		err := errors.New("scheduler is not initialized")
		err = errors.Join(err, internal.NewTraceError())
		return err
	}
	return nil
}

// Start begins the scheduler and blocks until ctx is cancelled.
// It waits for all running jobs to complete before returning.
func (scheduler *Scheduler) Start(ctx context.Context) {
	scheduler.cron.Start()
	<-ctx.Done()
	scheduler.Stop()
}

// Stop stops the cron scheduler if it is running; otherwise it does nothing.
// It blocks until all running jobs complete or the stop timeout elapses.
func (scheduler *Scheduler) Stop(delay ...time.Duration) {
	timeout := time.Second * 15
	for _, d := range delay {
		timeout += d
	}

	ctx := scheduler.cron.Stop()

	timeoutCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	select {
	case <-ctx.Done():
		log.Printf("all jobs is stopped")
	case <-timeoutCtx.Done():
		log.Printf("stop timeout (%s) exceeded, forcing shutdown", timeout)
	}
}

type job interface {
	Name() string
	Run(context.Context)
}

func jobRecovering() {
	if e := recover(); e != nil {
		stackTrace := internal.NewStackTraceError()
		err := fmt.Errorf("recovered from panic, details: %v", e)

		log.Println(err.Error() + "\n" + stackTrace.Error())
	}
}

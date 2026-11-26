// Package outbound is andaru-outbound: the unprivileged service that talks to the outside world for the engine,
// which has no network. It reads delivery jobs from the bus and sends them.
package outbound

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/andaru-project/andaru/internal/bus"
	"github.com/andaru-project/andaru/internal/delivery"
	"github.com/andaru-project/andaru/internal/notify"
)

// User is the user andaru-outbound runs as, and the name it gives the bus.
const User = "andaru-outbound"

// failuresToRaise is how many jobs in a row a channel gives up before it raises a notification of its own.
const failuresToRaise = 3

// Service sends the jobs on the bus's delivery topics.
type Service struct {
	Socket   string // the bus's services socket
	CacheDir string // where sent job IDs and jobs waiting for a retry are kept
	HTTP     *http.Client
	Log      *slog.Logger
	// RetryFor is how long after its first attempt a failing job is given up; zero means an hour.
	RetryFor time.Duration
	// Backoff is the wait before the nth retry; nil doubles from 5 s up to 5 minutes.
	Backoff func(n int) time.Duration
	// Ready, if set, is called each time the consumer is attached.
	Ready func()

	sent     *sent
	retries  *retries
	mu       sync.Mutex
	failures map[string]int      // jobs each channel gave up in a row
	working  map[string]bool     // channels that sent a job since the service started
	sending  map[string]struct{} // the IDs of the jobs being sent from the bus
}

// Run serves until ctx ends, reconnecting to the bus whenever the connection is lost.
func (s *Service) Run(ctx context.Context) error {
	if s.Log == nil {
		s.Log = slog.Default()
	}
	if s.HTTP == nil {
		// A redirect would send the job's headers to another host, and could turn the POST into a GET that answers 2xx.
		s.HTTP = &http.Client{Timeout: 15 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	if s.RetryFor == 0 {
		s.RetryFor = time.Hour
	}
	if s.Backoff == nil {
		s.Backoff = func(n int) time.Duration { return min(5*time.Second<<min(n, 6), 5*time.Minute) }
	}
	var err error
	if s.sent, err = openSent(s.CacheDir); err != nil {
		return fmt.Errorf("read the sent jobs: %w", err)
	}
	if s.retries, err = openRetries(s.CacheDir); err != nil {
		return fmt.Errorf("read the jobs waiting for a retry: %w", err)
	}
	s.failures, s.working, s.sending = map[string]int{}, map[string]bool{}, map[string]struct{}{}
	for attempt := 0; ; attempt++ {
		err := s.session(ctx)
		if ctx.Err() != nil {
			return nil
		}
		s.Log.Warn("bus connection lost; reconnecting", "error", err)
		if !sleep(ctx, min(time.Second<<min(attempt, 5), 30*time.Second)) {
			return nil
		}
	}
}

// session reads the consumer over one connection. Jobs are sent concurrently and acked in order; a job whose first
// send fails is acked once it waits in the retry queue.
func (s *Service) session(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c, err := bus.Dial(ctx, s.Socket, User, "")
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.Consume(ctx, delivery.Consumer, delivery.Filter); err != nil {
		return err
	}
	s.Log.Info("reading delivery jobs", "socket", s.Socket)
	if s.Ready != nil {
		s.Ready()
	}
	var acks order
	done := make(chan int64)
	var running sync.WaitGroup
	defer running.Wait()
	defer cancel() // runs first: the jobs still being sent stop, unacked, and the bus delivers them again
	running.Add(1)
	go func() {
		defer running.Done()
		s.retryLoop(ctx, c, &running)
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case seq := <-done:
			if ack := acks.finish(seq); ack > 0 {
				if err := c.Ack(delivery.Consumer, ack); err != nil {
					return err
				}
			}
		case d, ok := <-c.Messages():
			switch {
			case !ok:
				return errors.New("the bus closed the connection")
			case d.Detached:
				return errors.New("another connection took the deliveries consumer over")
			case d.Gap:
				s.raise(ctx, c, notify.Raise{Key: "missed", Severity: notify.Warning,
					Subject: "Notifications may not have been delivered",
					Detail:  "Some delivery jobs were lost before andaru-outbound could send them. Check its journal and the engine's for what happened."})
				if err := c.Ack(delivery.Consumer, d.Seq); err != nil {
					return err
				}
			case acks.has(d.Seq):
				// The bus sends the unacked events again after a gap; the first copy is still being handled.
			default:
				acks.start(d.Seq)
				running.Add(1)
				go func(m bus.Message) {
					defer running.Done()
					if s.handle(ctx, c, m) {
						select {
						case done <- m.Seq:
						case <-ctx.Done():
						}
					}
				}(d.Message)
			}
		}
	}
}

// handle sends a job from the bus once; false means it stopped unfinished, because the session ended.
func (s *Service) handle(ctx context.Context, c *bus.Client, m bus.Message) bool {
	var e struct {
		Type string       `json:"type"`
		Data delivery.Job `json:"data"`
	}
	err := json.Unmarshal(m.Payload, &e)
	if err == nil && (e.Type != delivery.JobType || e.Data.ID == "") {
		err = fmt.Errorf("type %q, job ID %q", e.Type, e.Data.ID)
	}
	if err != nil {
		// Retrying a job it cannot read would never succeed.
		s.Log.Error("delivery job unreadable; skipped", "topic", m.Topic, "seq", m.Seq, "error", err)
		return true
	}
	job := e.Data
	if s.sent.seen(job.ID, time.Now()) || s.retries.has(job.ID) {
		return true
	}
	// A copy delivered again while the first is being sent is done: the first is sent or queued, and if the session
	// ends first, the bus delivers whichever copy is unacked again.
	s.mu.Lock()
	_, busy := s.sending[job.ID]
	s.sending[job.ID] = struct{}{}
	s.mu.Unlock()
	if busy {
		return true
	}
	defer func() {
		s.mu.Lock()
		delete(s.sending, job.ID)
		s.mu.Unlock()
	}()
	now := time.Now()
	err = s.send(ctx, job)
	switch {
	case err == nil:
		s.sentOK(ctx, c, job)
	case ctx.Err() != nil:
		return false
	case isPermanent(err):
		s.giveUp(ctx, c, job, 1, err)
	default:
		s.Log.Warn("delivery failed; will retry", "job", job.ID, "channel", job.Channel, "error", err)
		if err := s.retries.save(&retry{Job: job, First: now, Attempts: 1, Next: time.Now().Add(s.Backoff(0))}); err != nil {
			// Without the queue, the job waits unacked for the next session.
			s.Log.Error("queue a delivery for a retry", "job", job.ID, "error", err)
			return false
		}
	}
	return true
}

// retryLoop sends the queued jobs as they come due, each in its own goroutine.
func (s *Service) retryLoop(ctx context.Context, c *bus.Client, running *sync.WaitGroup) {
	tick := time.NewTicker(min(time.Second, s.Backoff(0)))
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		for _, item := range s.retries.due(time.Now()) {
			running.Add(1)
			go func() {
				defer running.Done()
				defer s.retries.release(item)
				s.retryOnce(ctx, c, item)
			}()
		}
	}
}

func (s *Service) retryOnce(ctx context.Context, c *bus.Client, item *retry) {
	err := s.send(ctx, item.Job)
	switch {
	case err == nil:
		s.sentOK(ctx, c, item.Job)
	case ctx.Err() != nil:
		return // the entry stays as it was, for the next session
	case isPermanent(err) || time.Since(item.First)+s.Backoff(item.Attempts) > s.RetryFor:
		s.giveUp(ctx, c, item.Job, item.Attempts+1, err)
	default:
		s.Log.Warn("delivery failed; will retry", "job", item.Job.ID, "channel", item.Job.Channel, "attempt", item.Attempts+1, "error", err)
		next := &retry{Job: item.Job, First: item.First, Attempts: item.Attempts + 1}
		next.Next = time.Now().Add(s.Backoff(next.Attempts))
		if err := s.retries.save(next); err != nil {
			s.Log.Error("requeue a delivery", "job", item.Job.ID, "error", err)
		}
		return
	}
	if err := s.retries.remove(item.Job.ID); err != nil {
		s.Log.Warn("remove a finished retry", "job", item.Job.ID, "error", err)
	}
}

func (s *Service) send(ctx context.Context, job delivery.Job) error {
	switch job.Kind {
	case delivery.Webhook:
		return redact(sendWebhook(ctx, s.HTTP, job))
	}
	return permanent(fmt.Errorf("unknown channel kind %q", job.Kind))
}

func (s *Service) sentOK(ctx context.Context, c *bus.Client, job delivery.Job) {
	if err := s.sent.add(job.ID, time.Now()); err != nil {
		s.Log.Warn("record a sent job", "job", job.ID, "error", err)
	}
	s.mu.Lock()
	failed, known := s.failures[job.Channel] >= failuresToRaise, s.working[job.Channel]
	delete(s.failures, job.Channel)
	s.working[job.Channel] = true
	s.mu.Unlock()
	// The channel's notification may stand from before a restart, when nothing here remembers it.
	if failed || !known {
		s.raise(ctx, c, notify.Raise{Key: "channel/" + job.Channel})
	}
}

// giveUp logs a job given up; the third in a row for its channel raises the channel's notification.
func (s *Service) giveUp(ctx context.Context, c *bus.Client, job delivery.Job, attempts int, err error) {
	s.Log.Error("delivery given up", "job", job.ID, "channel", job.Channel, "attempts", attempts, "error", err)
	s.mu.Lock()
	s.failures[job.Channel]++
	n := s.failures[job.Channel]
	s.mu.Unlock()
	if n != failuresToRaise {
		return
	}
	s.raise(ctx, c, notify.Raise{Key: "channel/" + job.Channel, Severity: notify.Warning, Link: "/system/notifications",
		Subject: notify.Subject(fmt.Sprintf("Notifications to %s are failing", cmp.Or(job.Name, job.Channel))),
		Detail:  notify.Detail(fmt.Sprintf("The last %d notifications for this channel could not be sent. The last error: %v", n, err))})
}

// raise publishes one of andaru-outbound's own notifications, or clears it when r has no severity.
func (s *Service) raise(ctx context.Context, c *bus.Client, r notify.Raise) {
	topic, payload, err := notify.Event(delivery.Source, r)
	if err == nil {
		err = c.Publish(ctx, topic, payload, true)
	}
	if err != nil {
		s.Log.Error("raise a notification", "key", r.Key, "error", err)
	}
}

// redact drops the URL from a failed request's error: a webhook's URL is often its secret, and the error goes to the
// journal and into a notification other channels send.
func redact(err error) error {
	var u *url.Error
	if errors.As(err, &u) {
		return fmt.Errorf("%s request: %w", u.Op, u.Err)
	}
	return err
}

// permanentError is a failure no retry can fix.
type permanentError struct{ error }

func permanent(err error) error { return permanentError{err} }

func isPermanent(err error) bool { return errors.As(err, new(permanentError)) }

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

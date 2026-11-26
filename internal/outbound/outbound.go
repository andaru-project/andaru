// Package outbound is andaru-outbound: the unprivileged service that talks to the outside world for the engine,
// which has no network. It reads delivery jobs from the bus and sends them.
package outbound

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/andaru-project/andaru/internal/bus"
	"github.com/andaru-project/andaru/internal/delivery"
	"github.com/andaru-project/andaru/internal/notify"
)

// User is the user andaru-outbound runs as, and the name it gives the bus.
const User = "andaru-outbound"

// failuresToRaise is how many jobs in a row a channel fails before it raises a notification of its own.
const failuresToRaise = 3

// Service sends the jobs on the bus's delivery topics.
type Service struct {
	Socket   string // the bus's services socket
	CacheDir string // where the IDs of sent jobs are kept
	HTTP     *http.Client
	Log      *slog.Logger
	// RetryFor is how long a failing job is retried before it is given up; zero means an hour.
	RetryFor time.Duration
	// Backoff is the wait before the nth retry; nil doubles from 5 s up to 5 minutes.
	Backoff func(n int) time.Duration

	// Ready, if set, is called each time the consumer is attached.
	Ready func()

	sent     *sent
	mu       sync.Mutex
	failures map[string]int      // jobs each channel failed in a row
	sending  map[string]struct{} // the IDs of the jobs being sent
}

// Run serves until ctx ends, reconnecting to the bus whenever the connection is lost.
func (s *Service) Run(ctx context.Context) error {
	if s.Log == nil {
		s.Log = slog.Default()
	}
	if s.HTTP == nil {
		s.HTTP = &http.Client{Timeout: 15 * time.Second}
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
	s.failures, s.sending = map[string]int{}, map[string]struct{}{}
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

// session reads the consumer over one connection. Jobs are sent concurrently, and acked in order.
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

// handle sends one job; false means it stopped unfinished, because the session ended.
func (s *Service) handle(ctx context.Context, c *bus.Client, m bus.Message) bool {
	var e struct {
		Type string       `json:"type"`
		Data delivery.Job `json:"data"`
	}
	if err := json.Unmarshal(m.Payload, &e); err != nil || e.Type != delivery.JobType || e.Data.ID == "" {
		// Retrying a job it cannot read would never succeed.
		s.Log.Error("delivery job unreadable; skipped", "topic", m.Topic, "seq", m.Seq, "error", err)
		return true
	}
	job := e.Data
	if s.sent.seen(job.ID, time.Now()) {
		return true
	}
	// A copy delivered again while the first is being sent is done: the first is sent or given up, and if the session
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
	start := time.Now()
	for n := 0; ; n++ {
		err := s.send(ctx, job)
		if err == nil {
			if err := s.sent.add(job.ID, time.Now()); err != nil {
				s.Log.Warn("record a sent job", "job", job.ID, "error", err)
			}
			s.result(ctx, c, job, nil)
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		var p permanentError
		if errors.As(err, &p) || time.Since(start)+s.Backoff(n) > s.RetryFor {
			s.Log.Error("delivery given up", "job", job.ID, "channel", job.Channel, "attempts", n+1, "error", err)
			s.result(ctx, c, job, err)
			return true
		}
		s.Log.Warn("delivery failed; retrying", "job", job.ID, "channel", job.Channel, "attempt", n+1, "error", err)
		if !sleep(ctx, s.Backoff(n)) {
			return false
		}
	}
}

func (s *Service) send(ctx context.Context, job delivery.Job) error {
	switch job.Kind {
	case delivery.Webhook:
		return sendWebhook(ctx, s.HTTP, job)
	}
	return permanent(fmt.Errorf("unknown channel kind %q", job.Kind))
}

// result counts a channel's failures in a row: the third raises the channel's notification, and a success clears it.
func (s *Service) result(ctx context.Context, c *bus.Client, job delivery.Job, err error) {
	s.mu.Lock()
	before := s.failures[job.Channel]
	if err == nil {
		s.failures[job.Channel] = 0
	} else {
		s.failures[job.Channel]++
	}
	after := s.failures[job.Channel]
	s.mu.Unlock()
	key := "channel/" + job.Channel
	switch {
	case err == nil && before >= failuresToRaise:
		s.raise(ctx, c, notify.Raise{Key: key})
	case err != nil && after == failuresToRaise:
		name := job.Name
		if name == "" {
			name = job.Channel
		}
		s.raise(ctx, c, notify.Raise{Key: key, Severity: notify.Warning, Link: "/system/notifications",
			Subject: fmt.Sprintf("Notifications to %s are failing", name),
			Detail:  fmt.Sprintf("The last %d notifications for this channel could not be sent. The last error: %v", after, err)})
	}
}

// raise publishes one of andaru-outbound's own notifications, or clears it when r has no severity.
func (s *Service) raise(ctx context.Context, c *bus.Client, r notify.Raise) {
	typ, data := notify.RaisedType, any(r)
	if r.Severity == "" {
		typ, data = notify.ClearedType, map[string]string{"key": r.Key}
	}
	payload, err := bus.NewEvent(typ, delivery.Source, data)
	if err == nil {
		err = c.Publish(ctx, "notification/"+delivery.Source, payload, true)
	}
	if err != nil {
		s.Log.Error("raise a notification", "key", r.Key, "error", err)
	}
}

// permanentError is a failure no retry can fix.
type permanentError struct{ error }

func permanent(err error) error { return permanentError{err} }

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

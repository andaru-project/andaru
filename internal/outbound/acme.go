package outbound

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/andaru-project/andaru/internal/acme"
	"github.com/andaru-project/andaru/internal/bus"
)

const (
	acmeIssueTimeout   = 14 * time.Minute
	acmePublishTimeout = 15 * time.Second
)

// handleACME runs one ACME order from the bus and publishes its result, non-durable as the order was.
// HTTP-01 orders use their own single-slot semaphore so DNS-01 propagation never blocks HTTP-01 responder startup.
func (s *Service) handleACME(ctx context.Context, c *bus.Client, http01Slot, dns01Slot chan struct{}, m bus.Message) {
	var e struct {
		Type string   `json:"type"`
		Data acme.Job `json:"data"`
	}
	orderID := strings.TrimPrefix(m.Topic, "acme-order/")
	if err := json.Unmarshal(m.Payload, &e); err != nil || e.Type != acme.JobType || e.Data.JobID == "" || e.Data.OrderID != orderID {
		s.Log.Error("ACME order unreadable; skipped", "topic", m.Topic) // never the payload: it holds credentials
		return
	}
	deadline := time.Now().Add(acmeIssueTimeout)
	if e.Data.Deadline != nil && e.Data.Deadline.Before(deadline) {
		deadline = *e.Data.Deadline
	}
	if !time.Now().Before(deadline) {
		s.Log.Warn("ACME order expired before start; skipped", "order", orderID, "job", e.Data.JobID)
		return
	}
	jobCtx, jobCancel := context.WithDeadline(ctx, deadline)
	defer jobCancel()

	slot := dns01Slot
	if e.Data.Challenge.Type == "http-01" {
		slot = http01Slot
	}
	select {
	case slot <- struct{}{}:
	case <-jobCtx.Done():
		if ctx.Err() == nil {
			s.Log.Warn("ACME order expired while waiting for a worker slot; skipped", "order", orderID, "job", e.Data.JobID)
		}
		return
	}
	if jobCtx.Err() != nil {
		<-slot
		return
	}
	issuer := s.ACME
	if issuer == nil {
		issuer = &acme.Issuer{}
	}
	s.Log.Info("ACME order started", "order", orderID, "job", e.Data.JobID, "challenge", e.Data.Challenge.Type, "staging", e.Data.StagingTest)
	res := issuer.Issue(jobCtx, e.Data)
	// Release the worker slot before publishing so a slow bus reply cannot hold port 8480 or block other orders.
	<-slot
	if ctx.Err() != nil {
		return // the engine times the order out and may ask again
	}
	if res.OK {
		s.Log.Info("ACME order issued", "order", orderID, "job", res.JobID)
	} else {
		s.Log.Warn("ACME order failed", "order", orderID, "job", res.JobID, "error", res.Error)
	}
	pubCtx, pubCancel := context.WithTimeout(ctx, acmePublishTimeout)
	defer pubCancel()
	payload, err := bus.NewEvent(acme.ResultType, User, res)
	if err == nil {
		err = c.Publish(pubCtx, acme.ResultTopic(orderID), payload, false)
	}
	if err != nil {
		s.Log.Warn("ACME result not published", "order", orderID, "error", err)
		if res.OK {
			// Fall back to a compact error result so the engine does not wait the full job timeout.
			fallback := acme.Result{JobID: res.JobID, OrderID: res.OrderID, AccountID: res.AccountID, StagingTest: res.StagingTest, Error: "the issued certificate result could not be published"}
			if fp, ferr := bus.NewEvent(acme.ResultType, User, fallback); ferr == nil {
				fbCtx, fbCancel := context.WithTimeout(ctx, acmePublishTimeout)
				_ = c.Publish(fbCtx, acme.ResultTopic(orderID), fp, false)
				fbCancel()
			}
		}
	}
}

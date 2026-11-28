package outbound

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/andaru-project/andaru/internal/acme"
	"github.com/andaru-project/andaru/internal/bus"
)

const acmeIssueTimeout = 14 * time.Minute

// handleACME runs one ACME order from the bus and publishes its result, non-durable as the order was. It waits for
// slot, so orders run one at a time.
func (s *Service) handleACME(ctx context.Context, c *bus.Client, slot chan struct{}, m bus.Message) {
	var e struct {
		Type string   `json:"type"`
		Data acme.Job `json:"data"`
	}
	orderID := strings.TrimPrefix(m.Topic, "acme-order/")
	if err := json.Unmarshal(m.Payload, &e); err != nil || e.Type != acme.JobType || e.Data.JobID == "" || e.Data.OrderID != orderID {
		s.Log.Error("ACME order unreadable; skipped", "topic", m.Topic) // never the payload: it holds credentials
		return
	}
	select {
	case slot <- struct{}{}:
	case <-ctx.Done():
		return
	}
	defer func() { <-slot }()
	issuer := s.ACME
	if issuer == nil {
		issuer = &acme.Issuer{}
	}
	s.Log.Info("ACME order started", "order", orderID, "job", e.Data.JobID, "challenge", e.Data.Challenge.Type, "staging", e.Data.StagingTest)
	issueCtx, cancel := context.WithTimeout(ctx, acmeIssueTimeout)
	res := issuer.Issue(issueCtx, e.Data)
	cancel()
	if ctx.Err() != nil {
		return // the engine times the order out and may ask again
	}
	if res.OK {
		s.Log.Info("ACME order issued", "order", orderID, "job", res.JobID)
	} else {
		s.Log.Warn("ACME order failed", "order", orderID, "job", res.JobID, "error", res.Error)
	}
	payload, err := bus.NewEvent(acme.ResultType, User, res)
	if err == nil {
		err = c.Publish(ctx, acme.ResultTopic(orderID), payload, false)
	}
	if err != nil {
		s.Log.Warn("ACME result not published", "order", orderID, "error", err)
		if res.OK {
			// Fall back to a compact error result so the engine does not wait the full job timeout.
			fallback := acme.Result{JobID: res.JobID, OrderID: res.OrderID, AccountID: res.AccountID, StagingTest: res.StagingTest, Error: "the issued certificate result could not be published"}
			if fp, ferr := bus.NewEvent(acme.ResultType, User, fallback); ferr == nil {
				_ = c.Publish(ctx, acme.ResultTopic(orderID), fp, false)
			}
		}
	}
}

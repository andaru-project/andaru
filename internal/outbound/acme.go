package outbound

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/andaru-project/andaru/internal/acme"
	"github.com/andaru-project/andaru/internal/bus"
)

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
	res := issuer.Issue(ctx, e.Data)
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
		s.Log.Warn("ACME result not published", "order", orderID, "error", fmt.Sprint(err))
	}
}

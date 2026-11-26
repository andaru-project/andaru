package engine

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/andaru-project/andaru/internal/bus"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/delivery"
	"github.com/andaru-project/andaru/internal/notify"
)

// channelReader reads the saved notification channels with their secrets: the store's NotificationChannels.
type channelReader func(context.Context) ([]config.NotificationChannel, error)

// deliverNotifications publishes a delivery job for each raise to each enabled channel whose filter passes it. A
// job's ID is the event's and the channel's, so a raise handed on again makes the same jobs, which andaru-outbound
// sends once.
func deliverNotifications(events *bus.Bus, channels channelReader, log *slog.Logger) notify.Deliver {
	return func(e bus.Event, r notify.Raise, payload []byte) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		list, err := channels(ctx)
		if err != nil {
			return fmt.Errorf("read the notification channels: %w", err)
		}
		for _, c := range list {
			// A channel's own failure is not sent to it.
			if !c.Enabled || !passes(c, e.Source, r.Severity) || e.Source == delivery.Source && r.Key == "channel/"+c.ID {
				continue
			}
			if c.Unresolved {
				log.Warn("notification not sent: the channel's secrets have no value", "channel", c.ID, "name", c.Name, "source", e.Source, "key", r.Key)
				continue
			}
			// The layer records a raise from the future as raised now; the job says the same.
			raised := e.Time
			if now := time.Now().UTC(); raised.After(now) {
				raised = now
			}
			if err = publishJob(events, c, e.ID+":"+c.ID, e.Source, raised, r, payload); err != nil {
				return err
			}
		}
		return nil
	}
}

var severityRank = map[string]int{notify.Info: 0, notify.Warning: 1, notify.Critical: 2}

// passes reports whether c's filter lets a raise of severity from source through.
func passes(c config.NotificationChannel, source, severity string) bool {
	return severityRank[severity] >= severityRank[c.MinSeverity] && (len(c.Sources) == 0 || slices.Contains(c.Sources, source))
}

// publishJob publishes, durably, the job sending r to c. event is the notification's own event.
func publishJob(events *bus.Bus, c config.NotificationChannel, id, source string, raised time.Time, r notify.Raise, event []byte) error {
	job := delivery.Job{ID: id, Channel: c.ID, Name: c.Name, Kind: c.Kind, Event: event, Message: delivery.Message{
		Source: source, Key: r.Key, Severity: r.Severity, Subject: r.Subject, Detail: r.Detail, Link: r.Link, Raised: raised}}
	if c.Kind == delivery.Webhook {
		job.Webhook = &delivery.WebhookTarget{URL: c.URL}
		if c.Authorization != "" {
			job.Webhook.Headers = map[string]string{"Authorization": c.Authorization}
		}
	}
	payload, err := bus.NewEvent(delivery.JobType, "notifications", job)
	if err == nil {
		err = events.Publish(delivery.Topic(c.ID), payload, true)
	}
	if err != nil {
		return fmt.Errorf("publish the delivery job %s: %w", id, err)
	}
	return nil
}

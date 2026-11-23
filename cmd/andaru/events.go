package main

import (
	"context"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/andaru-project/andaru/internal/bus"
	"github.com/eclipse/paho.golang/paho"
)

// tellApply publishes that generation id changed state on the engine's event bus, so the web UI hears of it
// without asking. It is not durable: the engine may be down, and a subscriber reads the state again whenever it
// reconnects.
func tellApply(ctx context.Context, socket string, id int) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return err
	}
	client := paho.NewClient(paho.ClientConfig{Conn: conn})
	if _, err = client.Connect(ctx, &paho.Connect{
		ClientID: "realize-" + strconv.Itoa(os.Getpid()), CleanStart: true, Username: "realize", UsernameFlag: true,
	}); err != nil {
		_ = conn.Close()
		return err
	}
	defer client.Disconnect(&paho.Disconnect{})
	payload, err := bus.NewEvent("apply.changed", "realize", map[string]int{"id": id})
	if err != nil {
		return err
	}
	_, err = client.Publish(ctx, &paho.Publish{Topic: "apply/" + strconv.Itoa(id), Payload: payload})
	return err
}

package bus

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Event is the payload of every event on the bus. A subscriber that sees an ID
// again drops the duplicate.
type Event struct {
	ID     string    `json:"id"`
	Type   string    `json:"type"`
	Time   time.Time `json:"time"`
	Source string    `json:"source"`
	Data   any       `json:"data"`
}

// NewEvent encodes an event of typ from source, with a new ID and the current time.
func NewEvent(typ, source string, data any) ([]byte, error) {
	return json.Marshal(Event{ID: uuid.NewString(), Type: typ, Time: time.Now().UTC(), Source: source, Data: data})
}

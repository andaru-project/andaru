package config

import (
	"bytes"
	"encoding/json"

	"github.com/andaru-project/andaru/internal/jsonprofile"
)

func decodeStrict(data []byte) (any, error) { return decodeStrictLimit(data, requestLimit) }
func decodeStrictLimit(data []byte, limit int) (any, error) {
	if len(data) > limit {
		return nil, errTooLarge
	}
	if !jsonprofile.Valid(data, false) {
		return nil, errInvalidJSON
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var instance any
	if e := decoder.Decode(&instance); e != nil {
		return nil, errInvalidJSON
	}
	return instance, nil
}

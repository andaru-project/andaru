package config

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"io"
)

func decodeStrict(data []byte) (any, error) { return decodeStrictLimit(data, requestLimit) }
func decodeStrictLimit(data []byte, limit int) (any, error) {
	if len(data) > limit {
		return nil, errTooLarge
	}
	d := jsontext.NewDecoder(bytes.NewReader(data), jsontext.AllowDuplicateNames(false), jsontext.AllowInvalidUTF8(false))
	complete := false
	for {
		t, e := d.ReadToken()
		if e == io.EOF {
			if !complete {
				return nil, errInvalidJSON
			}
			break
		}
		if e != nil || complete || t.Kind() == 'n' || d.StackDepth() > 32 {
			return nil, errInvalidJSON
		}
		if t.Kind() == '0' {
			s := t.String()
			digits := s
			if len(digits) > 0 && digits[0] == '-' {
				digits = digits[1:]
			}
			if len(digits) == 0 || len(digits) > 19 || (len(digits) > 1 && digits[0] == '0') {
				return nil, errInvalidJSON
			}
			for _, c := range digits {
				if c < '0' || c > '9' {
					return nil, errInvalidJSON
				}
			}
		}
		if d.StackDepth() == 0 {
			complete = true
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var instance any
	if e := decoder.Decode(&instance); e != nil {
		return nil, errInvalidJSON
	}
	return instance, nil
}

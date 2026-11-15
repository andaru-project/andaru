// Package jsonprofile validates the application's strict JSON token profile.
package jsonprofile

import (
	"bytes"
	"encoding/json/jsontext"
	"io"
)

// Valid reports whether raw is one complete JSON value within the application's token limits.
func Valid(raw []byte, allowNull bool) bool {
	decoder := jsontext.NewDecoder(bytes.NewReader(raw), jsontext.AllowDuplicateNames(false), jsontext.AllowInvalidUTF8(false))
	complete := false
	for {
		token, err := decoder.ReadToken()
		if err == io.EOF {
			return complete
		}
		if err != nil || complete || decoder.StackDepth() > 32 || token.Kind() == 'n' && !allowNull {
			return false
		}
		if token.Kind() == '0' {
			text := token.String()
			if len(text) > 0 && text[0] == '-' {
				text = text[1:]
			}
			if len(text) == 0 || len(text) > 19 || len(text) > 1 && text[0] == '0' {
				return false
			}
			for _, c := range text {
				if c < '0' || c > '9' {
					return false
				}
			}
		}
		if decoder.StackDepth() == 0 {
			complete = true
		}
	}
}

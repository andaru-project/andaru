// SPDX-License-Identifier: AGPL-3.0-or-later
package rtlplayground

import (
	"strconv"
	"strings"
)

// sample is one Prometheus text-format sample.
type sample struct {
	name   string
	labels map[string]string
	value  float64
	raw    string // value as written, for exact 64-bit counters
}

// parsePromText reads the Prometheus text exposition format (0.0.4) the firmware's exporter
// writes. Comments, timestamps and unparsable lines are skipped, so a damaged scrape still
// yields every sample it holds. A final line without its newline is dropped: a reply cut
// mid-line could otherwise report a truncated counter.
func parsePromText(body []byte) []sample {
	text := string(body)
	if i := strings.LastIndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	} else {
		text = ""
	}
	var out []sample
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		if s, ok := parseSample(line); ok {
			out = append(out, s)
		}
	}
	return out
}

func parseSample(line string) (sample, bool) {
	s := sample{labels: map[string]string{}}
	i := strings.IndexAny(line, "{ \t")
	if i <= 0 {
		return s, false
	}
	s.name = line[:i]
	rest := line[i:]
	if rest[0] == '{' {
		var ok bool
		if rest, ok = parseLabels(rest[1:], s.labels); !ok {
			return s, false
		}
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return s, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return s, false
	}
	s.value, s.raw = v, fields[0]
	return s, true
}

// parseLabels parses `k="v",...}` into dst and returns the text after the closing brace.
func parseLabels(in string, dst map[string]string) (string, bool) {
	for {
		in = strings.TrimLeft(in, " ,")
		if strings.HasPrefix(in, "}") {
			return in[1:], true
		}
		eq := strings.IndexByte(in, '=')
		if eq <= 0 || len(in) < eq+2 || in[eq+1] != '"' {
			return "", false
		}
		key := strings.TrimSpace(in[:eq])
		in = in[eq+2:]
		var b strings.Builder
		closed := false
		for j := 0; j < len(in); j++ {
			c := in[j]
			if c == '\\' && j+1 < len(in) {
				j++
				if in[j] == 'n' {
					b.WriteByte('\n')
				} else {
					b.WriteByte(in[j])
				}
				continue
			}
			if c == '"' {
				in = in[j+1:]
				closed = true
				break
			}
			b.WriteByte(c)
		}
		if !closed {
			return "", false
		}
		dst[key] = b.String()
	}
}

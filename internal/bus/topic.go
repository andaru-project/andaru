package bus

import "strings"

// A topic names what changed, from the general to the specific: config/head, apply/12. A filter is a topic in
// which a level may be + (any one level) or the last level # (any number of levels, none included).

func validTopic(topic string) bool {
	if topic == "" || strings.ContainsAny(topic, "+#\x00") {
		return false
	}
	for _, level := range strings.Split(topic, "/") {
		if level == "" {
			return false
		}
	}
	return true
}

func validFilter(filter string) bool {
	if filter == "" || strings.ContainsRune(filter, 0) {
		return false
	}
	levels := strings.Split(filter, "/")
	for i, level := range levels {
		switch {
		case level == "":
			return false
		case level == "#":
			if i != len(levels)-1 {
				return false
			}
		case level != "+" && strings.ContainsAny(level, "+#"):
			return false
		}
	}
	return true
}

// match reports whether topic is one of the topics filter names.
func match(filter, topic string) bool {
	f, t := strings.Split(filter, "/"), strings.Split(topic, "/")
	for i, level := range f {
		if level == "#" {
			return true
		}
		if i >= len(t) || level != "+" && level != t[i] {
			return false
		}
	}
	return len(f) == len(t)
}

// covers reports whether grant matches every topic that filter can match. filter is a topic or a filter.
func covers(grant, filter string) bool {
	g, f := strings.Split(grant, "/"), strings.Split(filter, "/")
	for i, level := range g {
		if level == "#" {
			return true
		}
		if i >= len(f) {
			return false
		}
		switch {
		case level == "+":
			if f[i] == "#" {
				return false
			}
		case f[i] != level:
			return false
		}
	}
	return len(f) == len(g)
}

func allowed(grants []string, filter string) bool {
	for _, g := range grants {
		if covers(g, filter) {
			return true
		}
	}
	return false
}

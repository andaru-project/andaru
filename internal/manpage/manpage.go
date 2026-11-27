// Package manpage turns a command's --help text into a man page, so that the page cannot drift from the help (#103).
package manpage

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Section is a part of a page with a title of its own, as one subcommand's help.
type Section struct{ Title, Help string }

// Page renders help as the man page of name in section, with summary for its NAME line. Every help has a usage.
// The help's paragraphs before its usage are the description, the usage is the synopsis, and every later paragraph, a "Heading:" block
// or not, is kept as preformatted text, as --help prints it. Each of more follows under COMMANDS IN DETAIL.
func Page(name, section, summary, help string, more ...Section) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, ".TH %s %s \"\" \"Andaru\" \"Andaru Manual\"\n", escape(strings.ToUpper(name)), section)
	// Unhyphenated and ragged: paths and options must read as they are typed.
	fmt.Fprintf(&b, ".nh\n.ad l\n.SH NAME\n%s \\- %s\n", escape(name), escape(summary))
	var description []string
	synopsis := false
	for _, p := range strings.Split(strings.Trim(help, "\n"), "\n\n") {
		switch {
		case !synopsis && strings.HasPrefix(p, "usage: "):
			synopsis = true
			b.WriteString(".SH SYNOPSIS\n")
			preformatted(&b, strings.TrimPrefix(p, "usage: "), "       ")
			if len(description) > 0 {
				b.WriteString(".SH DESCRIPTION\n" + strings.Join(description, ".PP\n"))
			}
		case !synopsis:
			description = append(description, escape(strings.Join(strings.Fields(p), " "))+"\n")
		default:
			if heading, body, ok := strings.Cut(p, "\n"); ok && strings.HasSuffix(heading, ":") && !strings.HasPrefix(heading, " ") {
				fmt.Fprintf(&b, ".SH %s\n", escape(strings.ToUpper(strings.TrimSuffix(heading, ":"))))
				preformatted(&b, body, "")
				continue
			}
			b.WriteString(".PP\n")
			preformatted(&b, p, "")
		}
	}
	if !synopsis {
		panic("manpage: the help of " + name + " has no usage paragraph")
	}
	if len(more) > 0 {
		b.WriteString(".SH COMMANDS IN DETAIL\n")
	}
	for _, s := range more {
		fmt.Fprintf(&b, ".SS %s\n", escape(s.Title))
		preformatted(&b, strings.Trim(s.Help, "\n"), "")
	}
	return []byte(b.String())
}

// preformatted writes text as it is, line for line, without the indent cut from each continuation line. A block
// with a line too wide for man's 7-column indent within its 78-column line, goes to the left margin instead.
func preformatted(b *strings.Builder, text, indent string) {
	lines := strings.Split(text, "\n")
	wide := false
	for i, line := range lines {
		if i > 0 {
			lines[i] = strings.TrimPrefix(line, indent)
		}
		wide = wide || utf8.RuneCountInString(lines[i]) > 78-7
	}
	b.WriteString(".nf\n")
	if wide {
		b.WriteString(".in 0\n")
	}
	for _, line := range lines {
		b.WriteString(escape(line) + "\n")
	}
	b.WriteString(".fi\n")
	if wide {
		b.WriteString(".in\n")
	}
}

// escape keeps text literal in roff: backslashes; double quotes, which would split a macro's arguments; hyphens,
// quotes, carets and tildes, which a terminal would show as their typographic forms and so break a copied command;
// and a line that would start with a dot.
func escape(s string) string {
	s = strings.NewReplacer(`\`, `\e`, `"`, `\(dq`, "-", `\-`, "'", `\(aq`, "`", `\(ga`, "^", `\(ha`, "~", `\(ti`).Replace(s)
	if strings.HasPrefix(s, ".") { // a quote, the other control character, is \(aq by now
		s = `\&` + s
	}
	return s
}

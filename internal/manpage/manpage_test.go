package manpage

import "testing"

func TestPage(t *testing.T) {
	help := `Run the thing. It does
one job.

usage: tool [--flag] ARG
       tool other

Options:
  --flag  a \ backslash
.dot  at a line's start
`
	got := string(Page("tool", "8", "run the thing", help, Section{"tool other", "Other.\n\nusage: tool other\n"}))
	want := `.TH TOOL 8 "" "Andaru" "Andaru Manual"
.nh
.ad l
.SH NAME
tool \- run the thing
.SH SYNOPSIS
.nf
tool [\-\-flag] ARG
tool other
.fi
.SH DESCRIPTION
Run the thing. It does one job.
.SH OPTIONS
.nf
  \-\-flag  a \e backslash
\&.dot  at a line's start
.fi
.SH COMMANDS IN DETAIL
.SS tool other
.nf
Other.

usage: tool other
.fi
`
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

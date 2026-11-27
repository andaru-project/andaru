package manpage

import "testing"

func TestPage(t *testing.T) {
	help := `Run the thing. It does
one job.

A second paragraph.

usage: tool [--flag] ARG
       tool other

Options:
  --flag  a \ backslash; this line is wider than man's indent leaves, at 75
.dot  at a line's start, 'quoted' ~ ^ ` + "`" + ` "dq"
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
.PP
A second paragraph.
.SH OPTIONS
.nf
.in 0
  \-\-flag  a \e backslash; this line is wider than man\(aqs indent leaves, at 75
\&.dot  at a line\(aqs start, \(aqquoted\(aq \(ti \(ha \(ga \(dqdq\(dq
.fi
.in
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

func TestPageWithoutUsagePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("no panic")
		}
	}()
	Page("tool", "8", "run the thing", "Run the thing.\n\nOptions:\n  --flag  a flag\n")
}

package render

import (
	"strings"

	"github.com/arnocho/spanline/internal/result"
)

// DoctorText renders the preflight report for a terminal: who spanline authenticates as,
// what it may read, what it will never do, and exactly what a client audit log will show.
// The full checks table is held back unless Options.Details is set; without it the report
// names the reads that were refused, which is what the table was being read for.
func DoctorText(r *result.DoctorReport, o Options) string {
	p := newPalette(o)
	var b strings.Builder
	if r == nil {
		b.WriteString(p.bold("spanline doctor") + "\n")
		b.WriteString(indent1 + "no report to render\n")
		return out(&b)
	}
	w := o.width()

	header(&b, p, o, "spanline doctor", []string{
		kv("context", r.Context),
		kv("profile", r.Profile),
		kv("source", doctorSource(r)),
	}, []string{
		kv("identity", r.Identity),
		kv("generated", ts(r.GeneratedAt)),
	})

	section(&b, p, "access")
	writeFields(&b, [][2]string{
		{"can write", yesNo(r.CanWrite)},
		{"can read secrets", yesNo(r.CanReadSec)},
		{"level", itoa(r.Level)},
	}, indent1)

	if o.Details {
		section(&b, p, "checks")
		if len(r.Checks) == 0 {
			b.WriteString(indent1 + "no check run\n")
		} else {
			rows := make([][]string, 0, len(r.Checks))
			for _, c := range r.Checks {
				rows = append(rows, []string{c.Name, p.status(orEmpty(c.Status)), orEmpty(c.Detail)})
			}
			writeTable(&b, p, []string{"CHECK", "STATUS", "DETAIL"}, nil, rows, indent1)
		}
	} else {
		// The whole checks table is noise next to the one thing a security team asks:
		// what did the cluster refuse. An empty list is stated, never left blank.
		section(&b, p, "refused reads")
		if refused := refusedReads(r.Checks); len(refused) == 0 {
			b.WriteString(indent1 + "none recorded\n")
		} else {
			writeFields(&b, refused, indent1)
		}
	}

	section(&b, p, "egress")
	if len(r.Egress) == 0 {
		b.WriteString(indent1 + "none recorded\n")
	} else {
		writeList(&b, r.Egress, w, indent1)
	}

	section(&b, p, "audit footprint")
	if len(r.AuditFoot) == 0 {
		b.WriteString(indent1 + "none recorded\n")
	} else {
		writeList(&b, r.AuditFoot, w, indent1)
	}

	writeGaps(&b, p, o, r.Gaps)
	return out(&b)
}

// refusedReads lists the checks whose status records a refusal, in report order. The detail
// printed is the check's own, so a line never claims more than the report holds.
func refusedReads(checks []result.Check) [][2]string {
	var refused [][2]string
	for _, c := range checks {
		switch strings.ToLower(strings.TrimSpace(c.Status)) {
		case "denied", "refused", "forbidden":
			refused = append(refused, [2]string{c.Name, orEmpty(c.Detail)})
		}
	}
	return refused
}

// doctorSource names where the report came from, so the header never claims a live cluster
// when the run replayed recorded fixtures.
func doctorSource(r *result.DoctorReport) string {
	for _, c := range r.Checks {
		if c.Name != "source" {
			continue
		}
		if c.Status == "fixtures" {
			return "recorded fixtures, no cluster contacted"
		}
		return "live api discovery"
	}
	return "unknown"
}

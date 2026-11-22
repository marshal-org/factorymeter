package view

import (
	_ "embed"
	"html/template"
	"io"
	"net/url"
)

//go:embed assets/drill.html
var drillHTML string

var drillTmpl = template.Must(template.New("drill").Parse(drillHTML))

// DrillPage lists the items one number of the line was drawn from.
type DrillPage struct {
	Name, Station, Target, Value, Period string
	// How says how the number comes from the rows below.
	How string
	// Others names observations no item can be shown for.
	Others, Definition string
	// Back is the address of the line, at the station of the number.
	Back                          string
	ItemHead, WhenHead, ValueHead string
	Rows                          []DrillRow
}

// DrillRow is one item a number counted.
type DrillRow struct {
	Key, URL, Title, Who, When, Value string
	// Dim marks a row that is counted but is not a hit of a share.
	Dim bool
}

type drillDoc struct {
	*DrillPage
	CSP string
	CSS template.CSS
	JS  template.JS
}

// WriteDrill writes the page of one number's items. Only an http or https
// address of an item becomes a link.
func WriteDrill(w io.Writer, p *DrillPage) error {
	rows := make([]DrillRow, len(p.Rows))
	for i, r := range p.Rows {
		if u, err := url.Parse(r.URL); err != nil || (u.Scheme != "https" && u.Scheme != "http") {
			r.URL = ""
		}
		rows[i] = r
	}
	cp := *p
	cp.Rows = rows
	return drillTmpl.Execute(w, drillDoc{DrillPage: &cp, CSP: ServedCSP(), CSS: template.CSS(pageCSS), JS: template.JS(pageJS)})
}

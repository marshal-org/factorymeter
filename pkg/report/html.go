package report

import (
	"fmt"
	"html/template"
	"io"

	"github.com/marshal-org/factorymeter/pkg/metrics"
)

// The page is one file with no outside assets, so it can be mailed, attached
// to a ticket or opened from disk.
var page = template.Must(template.New("page").Funcs(template.FuncMap{
	"pct":   func(f float64) string { return fmt.Sprintf("%.0f%%", f*100) },
	"width": func(f float64) template.CSS { return template.CSS(fmt.Sprintf("width:%.1f%%", min(f, 1)*100)) },
	"first": func(i int) bool { return i == 0 },
}).Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="generator" content="factorymeter {{.R.Version}}">
<title>factorymeter: {{.Title}}</title>
<style>
:root{--bg:#f7f6f2;--card:#fff;--ink:#17181a;--mute:#5d6168;--line:#dedcd4;--accent:#b4410e;--bar:#c9c6bb;--soft:#f0eee7}
@media (prefers-color-scheme:dark){:root{--bg:#131416;--card:#1b1d20;--ink:#ecebe6;--mute:#a0a4ab;--line:#2f3237;--accent:#ff8a4c;--bar:#4a4e55;--soft:#22252a}}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--ink);font:16px/1.5 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif}
main{max-width:920px;margin:0 auto;padding:32px 16px 64px}
header p{margin:0;color:var(--mute);font-size:14px}
h1{font-size:clamp(24px,4.4vw,38px);line-height:1.2;margin:14px 0 20px;letter-spacing:-.01em;text-wrap:balance}
h2{font-size:18px;margin:0 0 12px}
section{background:var(--card);border:1px solid var(--line);border-radius:10px;padding:20px;margin:16px 0}
ul{margin:0;padding-left:20px}
li{margin:6px 0}
.scroll{overflow-x:auto}
table{border-collapse:collapse;width:100%;font-size:15px;font-variant-numeric:tabular-nums}
th,td{padding:8px 10px;border-bottom:1px solid var(--line);text-align:right;white-space:nowrap}
th:first-child,td:first-child{text-align:left;white-space:normal}
thead th{color:var(--mute);font-weight:600;font-size:13px}
tbody tr:last-child td{border-bottom:0}
.note{color:var(--mute);font-size:14px;margin:12px 0 0}
.bars{display:grid;gap:10px;margin:0 0 18px}
.bar{display:grid;grid-template-columns:minmax(120px,240px) 1fr 48px;gap:10px;align-items:center;font-size:14px}
.track{background:var(--soft);border-radius:4px;height:14px;overflow:hidden}
.fill{background:var(--bar);height:100%}
.bar.on .fill{background:var(--accent)}
.bar.on span:first-child{font-weight:600}
.bar span:last-child{text-align:right;font-variant-numeric:tabular-nums}
a.cta{display:inline-block;background:var(--accent);color:var(--bg);font-weight:600;text-decoration:none;padding:10px 16px;border-radius:8px;margin-top:14px}
a{color:var(--accent)}
footer{color:var(--mute);font-size:13px;margin-top:24px}
@media (max-width:560px){.bar{grid-template-columns:1fr 48px}.bar .track{grid-column:1/-1;grid-row:2}}
</style>
</head>
<body>
<main>
<header>
<p>factorymeter &middot; {{.Title}} &middot; {{.Window}}</p>
<h1>{{.R.Headline}}</h1>
</header>

<section>
<h2>What the numbers say</h2>
<ul>{{range .R.Findings}}<li>{{.}}</li>{{end}}</ul>
</section>

{{if .Bars}}<section>
<h2>{{.Stages.Title}}</h2>
<div class="bars">{{range .Bars}}
<div class="bar{{if .Constraint}} on{{end}}"><span>{{.Name}}{{if .Constraint}} (constraint){{end}}</span><div class="track"><div class="fill" style="{{width .Share}}"></div></div><span>{{pct .Share}}</span></div>{{end}}
</div>
{{template "table" .Stages}}
</section>{{end}}

<section>
<h2>{{.Classes.Title}}</h2>
{{template "table" .Classes}}
</section>

{{if .HasAgents}}<section>
<h2>{{.Agents.Title}}</h2>
{{template "table" .Agents}}
</section>{{end}}

{{if .HasRepos}}<section>
<h2>{{.Repos.Title}}</h2>
{{template "table" .Repos}}
</section>{{end}}

<section>
<h2>{{.LightsOut.Title}}</h2>
{{template "table" .LightsOut}}
<p class="note">Lights Out is a simulator of a software delivery line. The link opens it with the numbers above, so you can try changes on your own line before you make them.</p>
<a class="cta" href="{{.R.LightsOut.URL}}">Open your factory in Lights Out</a>
</section>

<section>
<h2>Limits</h2>
<ul>{{range .R.Limits}}<li>{{.}}</li>{{end}}{{range .R.Notes}}<li>{{.}}</li>{{end}}</ul>
</section>

<footer>Made with <a href="https://github.com/marshal-org/factorymeter">factorymeter</a> {{.R.Version}}, signature registry version {{.R.RegistryVersion}}, on {{.R.GeneratedAt.Format "2006-01-02"}}. It read GitHub with a read-only token and sent nothing anywhere else.</footer>
</main>
</body>
</html>
{{define "table"}}<div class="scroll"><table><thead><tr>{{range .Header}}<th>{{.}}</th>{{end}}</tr></thead><tbody>{{range .Rows}}<tr>{{range .}}<td>{{.}}</td>{{end}}</tr>{{end}}</tbody></table></div>{{if .Note}}<p class="note">{{.Note}}</p>{{end}}{{end}}`))

// HTML writes the one-page report.
func HTML(w io.Writer, r *metrics.Report) error {
	return page.Execute(w, NewView(r))
}

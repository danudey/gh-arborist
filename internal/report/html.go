package report

import (
	"html/template"
)

// renderHTML writes a self-contained HTML page: one collapsible row per
// finding, laid out as a table but expanding to show every reason and every
// detail.
//
// The page has no external dependencies, so it works from a file:// URL and
// can be mailed around as a single file. Rows expand with <details>, which
// needs no script; the script only adds filtering.
func renderHTML(res *result, opts Options) error {
	return htmlTemplate.Execute(opts.Out, buildDocument(res))
}

var htmlTemplate = template.Must(template.New("report").Parse(htmlSource))

const htmlSource = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Pruning report: {{.Repository}}</title>
<style>
:root {
  color-scheme: light dark;
  --bg: #ffffff;
  --panel: #f6f8fa;
  --border: #d0d7de;
  --text: #1f2328;
  --muted: #59636e;
  --link: #0969da;
  --delete: #cf222e;
  --reassign: #1868c7;
  --review: #9a6700;
  --mono: ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, monospace;
}
@media (prefers-color-scheme: dark) {
  :root {
    --bg: #0d1117;
    --panel: #161b22;
    --border: #30363d;
    --text: #e6edf3;
    --muted: #9198a1;
    --link: #4493f8;
    --delete: #ff7b72;
    --reassign: #6cb6ff;
    --review: #d29922;
  }
}
* { box-sizing: border-box; }
body {
  margin: 0;
  padding: 1.5rem;
  background: var(--bg);
  color: var(--text);
  font: 14px/1.5 -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
}
main { max-width: 1200px; margin: 0 auto; }
a { color: var(--link); }
h1 { font-size: 1.5rem; margin: 0 0 .25rem; }
h2 { font-size: 1.1rem; margin: 2rem 0 .5rem; }
h3 { font-size: .8rem; text-transform: uppercase; letter-spacing: .04em; color: var(--muted); margin: 1rem 0 .35rem; }
h3.group-title { font-size: .95rem; text-transform: none; letter-spacing: 0; color: var(--text); margin: 1.25rem 0 .35rem; }
.group + .group { margin-top: .5rem; }
.meta { color: var(--muted); margin: 0 0 .75rem; }
.mono, code { font-family: var(--mono); font-size: .92em; }
.count { color: var(--muted); font-weight: normal; }

.toolbar { display: flex; flex-wrap: wrap; gap: .5rem; align-items: center; margin: 1rem 0; }
.chip {
  font: inherit; cursor: pointer; padding: .3rem .7rem; border-radius: 999px;
  border: 1px solid var(--border); background: var(--panel); color: var(--text);
}
.chip[aria-pressed="true"] { border-color: currentColor; font-weight: 600; }
.chip.delete { color: var(--delete); }
.chip.reassign { color: var(--reassign); }
.chip.review { color: var(--review); }
.spacer { flex: 1 1 auto; }
input[type=search] {
  font: inherit; padding: .35rem .6rem; min-width: 15rem; flex: 1 1 15rem;
  border: 1px solid var(--border); border-radius: 6px; background: var(--bg); color: var(--text);
}
.plain {
  font: inherit; cursor: pointer; background: none; border: 1px solid var(--border);
  border-radius: 6px; padding: .3rem .6rem; color: var(--muted);
}
.disclaimer { color: var(--muted); border-left: 3px solid var(--border); padding-left: .75rem; margin: 1rem 0; }
.notes ul { margin: .25rem 0; padding-left: 1.25rem; color: var(--muted); }

/* One grid so the summaries line up like table rows. */
.head, summary {
  display: grid;
  grid-template-columns: minmax(10rem, 2.2fr) 4rem minmax(6rem, 1fr) 5.5rem minmax(8rem, 3fr);
  gap: .75rem;
  align-items: baseline;
}
.head {
  padding: .4rem 1.6rem;
  font-size: .72rem; text-transform: uppercase; letter-spacing: .05em; color: var(--muted);
  border-bottom: 1px solid var(--border);
}
.row { border-bottom: 1px solid var(--border); }
.row > summary {
  cursor: pointer; padding: .45rem 1.6rem; position: relative; list-style: none;
}
.row > summary::-webkit-details-marker { display: none; }
.row > summary::before {
  content: "›"; position: absolute; left: .5rem; top: .3rem;
  font-size: 1.1rem; line-height: 1.2; color: var(--muted);
  transition: transform .12s ease-out; display: inline-block;
}
.row[open] > summary::before { transform: rotate(90deg); }
.row > summary:hover { background: var(--panel); }
.name { overflow-wrap: anywhere; }
.age, .owner { color: var(--muted); overflow-wrap: anywhere; }
.why { color: var(--muted); overflow-wrap: anywhere; }
.badge { font-size: .78rem; font-weight: 600; }
.badge.delete { color: var(--delete); }
.badge.reassign { color: var(--reassign); }
.badge.review { color: var(--review); }

.body { padding: .5rem 1.6rem 1.25rem; background: var(--panel); }
.reasons { margin: 0; padding-left: 1.25rem; }
.reasons li { margin: .2rem 0; }
.reasons em { color: var(--muted); font-style: normal; }
dl { display: grid; grid-template-columns: max-content 1fr; gap: .3rem .9rem; margin: 0; }
dt { color: var(--muted); }
dd { margin: 0; overflow-wrap: anywhere; }
footer { margin-top: 2.5rem; color: var(--muted); font-size: .85rem; }
.empty { color: var(--muted); padding: 1rem 0; }

@media (max-width: 700px) {
  .head { display: none; }
  .row > summary { grid-template-columns: 1fr; gap: .15rem; }
}
</style>
</head>
<body>
<main>
<header>
  <h1>{{if .RepositoryURL}}<a href="{{.RepositoryURL}}">{{.Repository}}</a>{{else}}{{.Repository}}{{end}}</h1>
  <p class="meta">
    Pruning report · scanned {{.ScannedAt}} · {{.Scanned}}
    {{- if .Checks}} · checks: {{range $i, $c := .Checks}}{{if $i}}, {{end}}<code>{{$c}}</code>{{end}}{{end}}
  </p>

  <div class="toolbar">
    <button class="chip" data-action="all" aria-pressed="true">All {{.Total}}</button>
    {{- range .Counts}}
    <button class="chip {{.Action}}" data-action="{{.Action}}" aria-pressed="false">{{.Count}} to {{.Action}}</button>
    {{- end}}
    <span class="spacer"></span>
    <input type="search" id="filter" placeholder="Filter by name, owner, reason or check" aria-label="Filter findings">
    <button class="plain" id="expand-all">Expand all</button>
    <button class="plain" id="collapse-all">Collapse all</button>
  </div>

  <p class="disclaimer">{{.Disclaimer}}</p>
</header>

{{if .Notes}}
<section class="notes">
  <h2>Notes</h2>
  <ul>{{range .Notes}}<li>{{.}}</li>{{end}}</ul>
</section>
{{end}}

{{if not .Sections}}<p class="empty">Nothing to prune.</p>{{end}}

{{range .Sections}}
{{$header := .NameHeader}}
<section class="kind">
  <h2>{{.Title}} <span class="count" data-total="{{.Count}}">{{.Count}}</span></h2>
  {{range .Groups}}
  <div class="group" id="{{.ID}}">
  <h3 class="group-title">{{.Title}} <span class="count" data-total="{{.Count}}">{{.Count}}</span></h3>
  <div class="head">
    <span>{{$header}}</span><span>Age</span><span>Owner</span><span>Suggested</span><span>Why</span>
  </div>
  {{range .Rows}}
  <details class="row" id="{{.ID}}" data-key="{{.Key}}" data-action="{{.Action}}" data-search="{{.Search}}">
    <summary>
      <span class="name{{if .Monospace}} mono{{end}}">{{.Name}}</span>
      <span class="age" title="last activity {{.AgeExact}}">{{.Age}}</span>
      <span class="owner">{{.Owner}}</span>
      <span class="badge {{.Action}}">{{.Action}}</span>
      <span class="why">{{.Why}}</span>
    </summary>
    <div class="body">
      {{if .URL}}<p><a href="{{.URL}}" target="_blank" rel="noreferrer noopener">Open on GitHub &#8599;</a></p>{{end}}

      <h3>Why</h3>
      <ul class="reasons">
        {{- range .Reasons}}
        <li><code>{{.Check}}</code> {{.Summary}} <em>(suggests {{.Action}})</em></li>
        {{- end}}
      </ul>

      {{if .Details}}
      <h3>Details</h3>
      <dl>
        {{- range .Details}}
        <dt>{{.Label}}</dt>
        <dd>
          {{- if .URL}}<a href="{{.URL}}" target="_blank" rel="noreferrer noopener">
            {{- if .Code}}<code>{{.Value}}</code>{{else}}{{.Value}}{{end}}</a>
          {{- else}}
            {{- if .Code}}<code>{{.Value}}</code>{{else}}{{.Value}}{{end}}
          {{- end}}
        </dd>
        {{- end}}
      </dl>
      {{end}}
    </div>
  </details>
  {{end}}
  </div>
  {{end}}
</section>
{{end}}

<footer>Generated by gh-arborist. This report is advisory: nothing was deleted, closed or reassigned.</footer>
</main>

<script>
(function () {
  var rows = Array.prototype.slice.call(document.querySelectorAll(".row"));
  var chips = Array.prototype.slice.call(document.querySelectorAll(".chip"));
  var filter = document.getElementById("filter");
  var action = "all";

  function setCount(el, shown) {
    el.hidden = shown === 0;
    var count = el.querySelector(".count");
    count.textContent = shown === Number(count.dataset.total) ? count.dataset.total : shown + " of " + count.dataset.total;
  }

  function apply() {
    var term = filter.value.trim().toLowerCase();
    rows.forEach(function (row) {
      var matchesAction = action === "all" || row.dataset.action === action;
      var matchesTerm = term === "" || row.dataset.search.indexOf(term) !== -1;
      row.hidden = !(matchesAction && matchesTerm);
    });
    // Hide a sub-section once nothing in it is showing, and keep its count
    // honest.
    Array.prototype.forEach.call(document.querySelectorAll(".group"), function (group) {
      setCount(group, group.querySelectorAll(".row:not([hidden])").length);
    });
    // A section counts each item once, however many sub-sections list it.
    Array.prototype.forEach.call(document.querySelectorAll(".kind"), function (section) {
      var keys = {};
      Array.prototype.forEach.call(section.querySelectorAll(".row:not([hidden])"), function (row) {
        keys[row.dataset.key] = true;
      });
      setCount(section, Object.keys(keys).length);
    });
  }

  chips.forEach(function (chip) {
    chip.addEventListener("click", function () {
      action = chip.dataset.action;
      chips.forEach(function (other) {
        other.setAttribute("aria-pressed", String(other === chip));
      });
      apply();
    });
  });

  filter.addEventListener("input", apply);

  function setOpen(open) {
    rows.forEach(function (row) {
      if (!row.hidden) { row.open = open; }
    });
  }
  document.getElementById("expand-all").addEventListener("click", function () { setOpen(true); });
  document.getElementById("collapse-all").addEventListener("click", function () { setOpen(false); });

  // Opening the page at #some-branch should reveal that row.
  if (location.hash.length > 1) {
    var target = document.getElementById(location.hash.slice(1));
    if (target && target.classList.contains("row")) { target.open = true; }
  }
})();
</script>
</body>
</html>
`

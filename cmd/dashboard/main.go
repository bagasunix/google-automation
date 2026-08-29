// dashboard generates an HTML analytics report from the search_automation.db.
// Usage: ./dashboard [--db path/to/db] [--out path/to/report.html]
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"html/template"
	"log"
	"os"
	"time"

	_ "modernc.org/sqlite"
)

type DailyRow struct {
	Date            string
	TotalSearch     int
	Success         int
	Fail            int
	Captcha         int
	AvgDwellSeconds float64
	AvgSerpPosition float64
	SuccessRate     float64
	CaptchaRate     float64
}

type ProxyRow struct {
	IP           string
	Port         int
	Country      string
	Used         int
	Success      int
	Fail         int
	Captcha      int
	SuccessRate  float64
	Blacklisted  bool
}

type ArticleRow struct {
	Title        string
	URL          string
	Searched     int
	SerpPosition string
	LastSearched string
}

type ReportData struct {
	Generated string
	Daily     []DailyRow
	Proxies   []ProxyRow
	Articles  []ArticleRow
	Totals    DailyRow
}

func main() {
	dbPath := flag.String("db", "search_automation.db", "path to SQLite DB")
	outPath := flag.String("out", "analytics/dashboard.html", "output HTML file")
	flag.Parse()

	db, err := sql.Open("sqlite", *dbPath+"?mode=ro")
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	data := ReportData{
		Generated: time.Now().Format("2006-01-02 15:04:05"),
	}

	data.Daily = queryDaily(db)
	data.Proxies = queryProxies(db)
	data.Articles = queryArticles(db)
	data.Totals = computeTotals(data.Daily)

	if err := os.MkdirAll("analytics", 0755); err != nil {
		log.Fatalf("mkdir analytics: %v", err)
	}

	f, err := os.Create(*outPath)
	if err != nil {
		log.Fatalf("create output: %v", err)
	}
	defer f.Close()

	if err := tmpl.Execute(f, data); err != nil {
		log.Fatalf("render template: %v", err)
	}

	fmt.Printf("Dashboard written to %s\n", *outPath)
}

func queryDaily(db *sql.DB) []DailyRow {
	rows, err := db.Query(`
		SELECT date, total_search, success, fail, captcha,
		       COALESCE(avg_dwell_seconds,0), COALESCE(avg_serp_position,0)
		FROM daily_stats
		ORDER BY date DESC
		LIMIT 30`)
	if err != nil {
		log.Printf("query daily: %v", err)
		return nil
	}
	defer rows.Close()

	var out []DailyRow
	for rows.Next() {
		var r DailyRow
		rows.Scan(&r.Date, &r.TotalSearch, &r.Success, &r.Fail, &r.Captcha,
			&r.AvgDwellSeconds, &r.AvgSerpPosition)
		if r.TotalSearch > 0 {
			r.SuccessRate = float64(r.Success) / float64(r.TotalSearch) * 100
			r.CaptchaRate = float64(r.Captcha) / float64(r.TotalSearch) * 100
		}
		out = append(out, r)
	}
	return out
}

func queryProxies(db *sql.DB) []ProxyRow {
	rows, err := db.Query(`
		SELECT p.ip, p.port, COALESCE(p.country,'?'), p.blacklisted,
		       COUNT(t.id),
		       SUM(CASE WHEN t.status='success' THEN 1 ELSE 0 END),
		       SUM(CASE WHEN t.status='fail' THEN 1 ELSE 0 END),
		       SUM(CASE WHEN t.status='captcha' THEN 1 ELSE 0 END)
		FROM proxies p
		LEFT JOIN tasks t ON t.proxy_id = p.id
		GROUP BY p.id
		ORDER BY COUNT(t.id) DESC`)
	if err != nil {
		log.Printf("query proxies: %v", err)
		return nil
	}
	defer rows.Close()

	var out []ProxyRow
	for rows.Next() {
		var r ProxyRow
		var bl int
		rows.Scan(&r.IP, &r.Port, &r.Country, &bl, &r.Used, &r.Success, &r.Fail, &r.Captcha)
		r.Blacklisted = bl == 1
		if r.Used > 0 {
			r.SuccessRate = float64(r.Success) / float64(r.Used) * 100
		}
		out = append(out, r)
	}
	return out
}

func queryArticles(db *sql.DB) []ArticleRow {
	rows, err := db.Query(`
		SELECT title, url, searched_count,
		       COALESCE(serp_position, 0),
		       COALESCE(date(last_searched_at), 'never')
		FROM articles
		ORDER BY last_searched_at DESC
		LIMIT 50`)
	if err != nil {
		log.Printf("query articles: %v", err)
		return nil
	}
	defer rows.Close()

	var out []ArticleRow
	for rows.Next() {
		var r ArticleRow
		var pos int
		rows.Scan(&r.Title, &r.URL, &r.Searched, &pos, &r.LastSearched)
		if pos == 0 {
			r.SerpPosition = "N/A"
		} else {
			r.SerpPosition = fmt.Sprintf("%d", pos)
		}
		out = append(out, r)
	}
	return out
}

func computeTotals(rows []DailyRow) DailyRow {
	var t DailyRow
	t.Date = "TOTAL"
	for _, r := range rows {
		t.TotalSearch += r.TotalSearch
		t.Success += r.Success
		t.Fail += r.Fail
		t.Captcha += r.Captcha
	}
	if t.TotalSearch > 0 {
		t.SuccessRate = float64(t.Success) / float64(t.TotalSearch) * 100
		t.CaptchaRate = float64(t.Captcha) / float64(t.TotalSearch) * 100
	}
	return t
}

var tmpl = template.Must(template.New("dashboard").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<title>Search Automation Dashboard</title>
<style>
  body { font-family: system-ui, sans-serif; background: #0f1117; color: #e2e8f0; margin: 0; padding: 24px; }
  h1 { color: #7c3aed; margin-bottom: 4px; }
  .meta { color: #64748b; font-size: 13px; margin-bottom: 32px; }
  h2 { color: #a78bfa; border-bottom: 1px solid #1e293b; padding-bottom: 8px; margin-top: 40px; }
  table { width: 100%; border-collapse: collapse; font-size: 13px; margin-bottom: 16px; }
  th { background: #1e293b; color: #94a3b8; text-align: left; padding: 8px 12px; font-weight: 600; }
  td { padding: 7px 12px; border-bottom: 1px solid #1e293b; }
  tr:hover td { background: #1e293b44; }
  .good { color: #34d399; }
  .bad  { color: #f87171; }
  .warn { color: #fbbf24; }
  .na   { color: #475569; }
  .totals td { background: #1e1b3a; font-weight: 600; }
  a { color: #818cf8; text-decoration: none; }
  a:hover { text-decoration: underline; }
</style>
</head>
<body>
<h1>Search Automation Dashboard</h1>
<div class="meta">Generated: {{.Generated}}</div>

<h2>Summary (last 30 days)</h2>
<table>
  <tr><th>Date</th><th>Searches</th><th>OK</th><th>Fail</th><th>CAPTCHA</th><th>Success%</th><th>CAPTCHA%</th><th>Avg Dwell</th><th>Avg SERP</th></tr>
  {{range .Daily}}
  <tr>
    <td>{{.Date}}</td>
    <td>{{.TotalSearch}}</td>
    <td class="good">{{.Success}}</td>
    <td class="bad">{{.Fail}}</td>
    <td class="warn">{{.Captcha}}</td>
    <td class="{{if ge .SuccessRate 50.0}}good{{else}}bad{{end}}">{{printf "%.1f" .SuccessRate}}%</td>
    <td class="{{if ge .CaptchaRate 20.0}}bad{{else}}warn{{end}}">{{printf "%.1f" .CaptchaRate}}%</td>
    <td>{{printf "%.1f" .AvgDwellSeconds}}s</td>
    <td>{{if eq .AvgSerpPosition 0.0}}<span class="na">N/A</span>{{else}}{{printf "%.1f" .AvgSerpPosition}}{{end}}</td>
  </tr>
  {{end}}
  <tr class="totals">
    <td>TOTAL</td>
    <td>{{.Totals.TotalSearch}}</td>
    <td class="good">{{.Totals.Success}}</td>
    <td class="bad">{{.Totals.Fail}}</td>
    <td class="warn">{{.Totals.Captcha}}</td>
    <td>{{printf "%.1f" .Totals.SuccessRate}}%</td>
    <td>{{printf "%.1f" .Totals.CaptchaRate}}%</td>
    <td>—</td><td>—</td>
  </tr>
</table>

<h2>Proxy Performance</h2>
<table>
  <tr><th>IP:Port</th><th>Country</th><th>Used</th><th>OK</th><th>Fail</th><th>CAPTCHA</th><th>Success%</th><th>Status</th></tr>
  {{range .Proxies}}
  <tr>
    <td>{{.IP}}:{{.Port}}</td>
    <td>{{.Country}}</td>
    <td>{{.Used}}</td>
    <td class="good">{{.Success}}</td>
    <td class="bad">{{.Fail}}</td>
    <td class="warn">{{.Captcha}}</td>
    <td class="{{if ge .SuccessRate 50.0}}good{{else}}bad{{end}}">{{printf "%.1f" .SuccessRate}}%</td>
    <td>{{if .Blacklisted}}<span class="bad">blacklisted</span>{{else}}<span class="good">active</span>{{end}}</td>
  </tr>
  {{end}}
</table>

<h2>Articles (last searched)</h2>
<table>
  <tr><th>Title</th><th>Searches</th><th>SERP Pos</th><th>Last Searched</th></tr>
  {{range .Articles}}
  <tr>
    <td><a href="{{.URL}}" target="_blank">{{.Title}}</a></td>
    <td>{{.Searched}}</td>
    <td class="{{if eq .SerpPosition "N/A"}}na{{else}}good{{end}}">{{.SerpPosition}}</td>
    <td>{{.LastSearched}}</td>
  </tr>
  {{end}}
</table>
</body>
</html>
`))

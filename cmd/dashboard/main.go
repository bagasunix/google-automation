// dashboard generates an HTML analytics report from the search_automation.db.
// Usage: ./dashboard [--db path/to/db] [--out path/to/report.html]
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
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
	serveAddr := flag.String("serve", "", "run live web server on address (e.g. ':8080' or '0.0.0.0:8080')")
	flag.Parse()

	db, err := sql.Open("sqlite", *dbPath+"?mode=ro")
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	if *serveAddr != "" {
		log.Printf("🚀 Starting live Search Automation Dashboard on http://localhost%s", *serveAddr)

		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			data := ReportData{
				Generated: time.Now().Format("2006-01-02 15:04:05"),
				Daily:     queryDaily(db),
				Proxies:   queryProxies(db),
				Articles:  queryArticles(db),
			}
			data.Totals = computeTotals(data.Daily)

			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if err := tmpl.Execute(w, data); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		})

		http.HandleFunc("/api/stats", func(w http.ResponseWriter, r *http.Request) {
			daily := queryDaily(db)
			proxies := queryProxies(db)
			articles := queryArticles(db)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"status":"ok","daily_count":%d,"proxy_count":%d,"article_count":%d}`, len(daily), len(proxies), len(articles))
		})

		http.HandleFunc("/api/export/csv", func(w http.ResponseWriter, r *http.Request) {
			daily := queryDaily(db)
			w.Header().Set("Content-Type", "text/csv")
			w.Header().Set("Content-Disposition", "attachment; filename=\"search_automation_report.csv\"")
			fmt.Fprintf(w, "Date,TotalSearches,Success,Fail,CAPTCHA,SuccessRatePercent,AvgDwellSeconds,AvgSerpPosition\n")
			for _, d := range daily {
				fmt.Fprintf(w, "%s,%d,%d,%d,%d,%.2f,%.2f,%.2f\n",
					d.Date, d.TotalSearch, d.Success, d.Fail, d.Captcha, d.SuccessRate, d.AvgDwellSeconds, d.AvgSerpPosition)
			}
		})

		if err := http.ListenAndServe(*serveAddr, nil); err != nil {
			log.Fatalf("http serve: %v", err)
		}
		return
	}

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
<meta http-equiv="refresh" content="30">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Search Automation Live Dashboard</title>
<script src="https://cdn.jsdelivr.net/npm/chart.js"></script>
<style>
  :root { --bg: #0f172a; --card: #1e293b; --text: #f8fafc; --muted: #94a3b8; --border: #334155; --primary: #8b5cf6; --success: #10b981; --danger: #ef4444; --warning: #f59e0b; }
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; background: var(--bg); color: var(--text); margin: 0; padding: 24px; }
  .header { display: flex; justify-content: space-between; align-items: center; border-bottom: 1px solid var(--border); padding-bottom: 16px; margin-bottom: 24px; }
  h1 { margin: 0; font-size: 24px; color: var(--primary); display: flex; align-items: center; gap: 8px; }
  .badge { background: #10b98122; color: #10b981; border: 1px solid #10b98144; padding: 4px 8px; border-radius: 9999px; font-size: 12px; font-weight: 600; }
  .meta { color: var(--muted); font-size: 13px; }
  .grid-stats { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 16px; margin-bottom: 24px; }
  .stat-card { background: var(--card); border: 1px solid var(--border); border-radius: 12px; padding: 16px; }
  .stat-card .label { color: var(--muted); font-size: 13px; font-weight: 500; }
  .stat-card .val { font-size: 26px; font-weight: 700; margin-top: 4px; }
  .card { background: var(--card); border: 1px solid var(--border); border-radius: 12px; padding: 20px; margin-bottom: 24px; }
  h2 { font-size: 16px; font-weight: 600; color: #c4b5fd; margin: 0 0 16px 0; }
  table { width: 100%; border-collapse: collapse; font-size: 13px; }
  th { background: #0f172a88; color: var(--muted); text-align: left; padding: 10px 12px; font-weight: 600; border-bottom: 1px solid var(--border); }
  td { padding: 9px 12px; border-bottom: 1px solid var(--border); }
  tr:hover td { background: #33415533; }
  .good { color: var(--success); font-weight: 600; }
  .bad  { color: var(--danger); font-weight: 600; }
  .warn { color: var(--warning); font-weight: 600; }
  .na   { color: #64748b; }
  .btn { display: inline-flex; align-items: center; gap: 6px; background: #6366f1; color: white; border: none; padding: 8px 16px; border-radius: 8px; font-size: 13px; font-weight: 600; cursor: pointer; text-decoration: none; }
  .btn:hover { background: #4f46e5; text-decoration: none; }
  .chart-container { position: relative; height: 260px; width: 100%; margin-top: 12px; }
  a { color: #a78bfa; text-decoration: none; }
  a:hover { text-decoration: underline; }
</style>
</head>
<body>
<div class="header">
  <div>
    <h1><span>⚡</span> Search Automation Engine <span class="badge">LIVE</span></h1>
    <div class="meta">Auto-refreshes every 30s • Last update: {{.Generated}}</div>
  </div>
  <div>
    <a href="/api/export/csv" class="btn">📥 Export CSV Report</a>
  </div>
</div>

<div class="grid-stats">
  <div class="stat-card">
    <div class="label">Total Searches (30d)</div>
    <div class="val">{{.Totals.TotalSearch}}</div>
  </div>
  <div class="stat-card">
    <div class="label">Success Rate</div>
    <div class="val good">{{printf "%.1f" .Totals.SuccessRate}}%</div>
  </div>
  <div class="stat-card">
    <div class="label">CAPTCHA Rate</div>
    <div class="val warn">{{printf "%.1f" .Totals.CaptchaRate}}%</div>
  </div>
  <div class="stat-card">
    <div class="label">Active Proxies</div>
    <div class="val">{{len .Proxies}}</div>
  </div>
</div>

<div class="card">
  <h2>Performance & Ranking Trend</h2>
  <div class="chart-container">
    <canvas id="perfChart"></canvas>
  </div>
</div>

<div class="card">
  <h2>Daily Performance (Last 30 Days)</h2>
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
  </table>
</div>

<div class="card">
  <h2>Proxy Pool & Health Scoring</h2>
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
</div>

<div class="card">
  <h2>Article SERP Positions & History</h2>
  <table>
    <tr><th>Title</th><th>Searches</th><th>SERP Position</th><th>Last Searched</th></tr>
    {{range .Articles}}
    <tr>
      <td><a href="{{.URL}}" target="_blank">{{.Title}}</a></td>
      <td>{{.Searched}}</td>
      <td class="{{if eq .SerpPosition "N/A"}}na{{else}}good{{end}}">{{.SerpPosition}}</td>
      <td>{{.LastSearched}}</td>
    </tr>
    {{end}}
  </table>
</div>

<script>
  const ctx = document.getElementById('perfChart').getContext('2d');
  const labels = [{{range .Daily}}"{{.Date}}",{{end}}];
  const searches = [{{range .Daily}}{{.TotalSearch}},{{end}}];
  const successes = [{{range .Daily}}{{.Success}},{{end}}];

  new Chart(ctx, {
    type: 'line',
    data: {
      labels: labels,
      datasets: [
        { label: 'Total Searches', data: searches, borderColor: '#8b5cf6', backgroundColor: '#8b5cf622', tension: 0.3, fill: true },
        { label: 'Successes', data: successes, borderColor: '#10b981', backgroundColor: '#10b98122', tension: 0.3, fill: true }
      ]
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      scales: {
        x: { grid: { color: '#33415544' }, ticks: { color: '#94a3b8' } },
        y: { grid: { color: '#33415544' }, ticks: { color: '#94a3b8' }, beginAtZero: true }
      },
      plugins: {
        legend: { labels: { color: '#f8fafc' } }
      }
    }
  });
</script>
</body>
</html>
`))

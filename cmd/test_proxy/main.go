package main

import (
	"fmt"
	"time"

	"google-automation/internal/proxy"
)

func main() {
	sources := []string{
		"https://raw.githubusercontent.com/TheSpeedX/PROXY-List/master/http.txt",
		"https://raw.githubusercontent.com/monosans/proxy-list/main/proxies/http.txt",
		"https://raw.githubusercontent.com/clarketm/proxy-list/master/proxy-list-raw.txt",
		"https://raw.githubusercontent.com/jetkai/proxy-list/main/online-proxies/txt/proxies-http.txt",
		"https://raw.githubusercontent.com/roosterkid/openproxylist/main/HTTPS_RAW.txt",
		"https://api.proxyscrape.com/v4/free-proxy-list/get?request=display_proxies&proxy_format=protocolipport&format=text&protocol=http",
		"https://proxylist.geonode.com/api/proxy-list?limit=500&page=1&sort_by=lastChecked&sort_type=desc",
		"https://free-proxy-list.net/",
		"https://spys.one/en/http-proxy-list/",
	}

	fmt.Println("=== SCRAPING PROXIES (9 sources, parallel goroutines) ===")
	fmt.Println()

	scraper := proxy.NewScraper(sources)
	proxies, err := scraper.Scrape()
	if err != nil {
		fmt.Printf("ERROR: %v\n", err)
		return
	}

	fmt.Printf("\n=== SCRAPE COMPLETE ===\n")
	fmt.Printf("Total unique proxies: %d\n", len(proxies))

	fmt.Printf("\n=== HEALTH CHECK (goroutines, 100 concurrent) ===\n\n")
	checker := proxy.NewChecker(5)
	results := checker.CheckAll(proxies)

	fmt.Printf("\n=== FINAL RESULTS ===\n")
	fmt.Printf("Scraped: %d\n", len(proxies))
	fmt.Printf("Healthy: %d\n", len(results))
	fmt.Printf("Time: %v\n", time.Now().Format("15:04:05"))
}

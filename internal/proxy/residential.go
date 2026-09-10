package proxy

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"google-automation/internal/config"
)

// residentialCountryTZ maps a residential provider's country code to an IANA
// timezone. The gateway IP can't be geo-located to the real exit (see
// health.go), so we set the browser timezone from the configured country here.
// Empty/unknown falls back to "" — the worker's stealth profile then derives
// tz from country on its own (browser/stealth.py _COUNTRY_TIMEZONES).
var residentialCountryTZ = map[string]string{
	"id": "Asia/Jakarta", "us": "America/New_York", "gb": "Europe/London",
	"de": "Europe/Berlin", "fr": "Europe/Paris", "nl": "Europe/Amsterdam",
	"sg": "Asia/Singapore", "jp": "Asia/Tokyo", "au": "Australia/Sydney",
	"ca": "America/Toronto", "in": "Asia/Kolkata", "my": "Asia/Kuala_Lumpur",
}

// GenerateResidentialProxies builds a list of proxy endpoints with rotating session IDs
// for providers like Smartproxy, IPRoyal, BrightData, or Oxylabs.
func GenerateResidentialProxies(cfg *config.ProxyConfig, count int) []Proxy {
	if count <= 0 {
		count = 20
	}
	var out []Proxy

	host := cfg.ResidentialHost
	port := cfg.ResidentialPort
	baseUser := cfg.ResidentialUser
	pass := cfg.ResidentialPassword
	country := strings.ToLower(cfg.ResidentialCountry)
	if country == "" {
		country = "id"
	}
	tz := residentialCountryTZ[country]

	for i := 1; i <= count; i++ {
		sessionID := fmt.Sprintf("sess%d%d", time.Now().Unix()%10000, i)
		username := baseUser
		if baseUser != "" {
			// Format: user-country-id-session-sessX
			if !strings.Contains(baseUser, "session-") {
				username = fmt.Sprintf("%s-country-%s-session-%s", baseUser, country, sessionID)
			}
		}

		out = append(out, Proxy{
			IP:            host,
			Port:          port,
			Protocol:      "http",
			Country:       strings.ToUpper(country),
			Username:      username,
			Password:      pass,
			APIKeyIndex:   0,
			IsResidential: true,
			Timezone:      tz,
		})
	}
	return out
}

// LoadCustomProxyFile loads proxies from a text file formatted as:
// ip:port or ip:port:username:password or protocol://user:pass@ip:port
func LoadCustomProxyFile(filePath string) ([]Proxy, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open proxy file: %w", err)
	}
	defer f.Close()

	var out []Proxy
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		p, ok := ParseSingleProxyLine(line)
		if ok {
			out = append(out, p)
		}
	}
	return out, scanner.Err()
}

// ParseProxyString parses multiple lines of proxies from text/bulk upload.
func ParseProxyString(rawText string) []Proxy {
	var out []Proxy
	scanner := bufio.NewScanner(strings.NewReader(rawText))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p, ok := ParseSingleProxyLine(line)
		if ok {
			out = append(out, p)
		}
	}
	return out
}

// ParseSingleProxyLine parses a single proxy string in various standard formats.
func ParseSingleProxyLine(line string) (Proxy, bool) {
	line = strings.TrimSpace(line)
	protocol := "http"

	// Strip protocol prefix if present (e.g. http://, https://, socks5://)
	if strings.Contains(line, "://") {
		parts := strings.SplitN(line, "://", 2)
		protocol = strings.ToLower(parts[0])
		line = parts[1]
	}

	// Format: user:pass@ip:port
	if strings.Contains(line, "@") {
		authAndHost := strings.SplitN(line, "@", 2)
		auth := authAndHost[0]
		hostPort := authAndHost[1]

		authUser := ""
		authPass := ""
		if strings.Contains(auth, ":") {
			ap := strings.SplitN(auth, ":", 2)
			authUser = ap[0]
			authPass = ap[1]
		} else {
			authUser = auth
		}

		hp := strings.SplitN(hostPort, ":", 2)
		if len(hp) == 2 {
			port, err := strconv.Atoi(hp[1])
			if err == nil {
				return Proxy{
					IP:       hp[0],
					Port:     port,
					Protocol: protocol,
					Username: authUser,
					Password: authPass,
					Country:  "CUSTOM",
				}, true
			}
		}
	}

	// Format: ip:port:username:password OR ip:port
	parts := strings.Split(line, ":")
	if len(parts) >= 2 {
		ip := parts[0]
		port, err := strconv.Atoi(parts[1])
		if err != nil {
			return Proxy{}, false
		}
		user := ""
		pass := ""
		if len(parts) >= 4 {
			user = parts[2]
			pass = parts[3]
		}
		return Proxy{
			IP:       ip,
			Port:     port,
			Protocol: protocol,
			Username: user,
			Password: pass,
			Country:  "CUSTOM",
		}, true
	}

	return Proxy{}, false
}

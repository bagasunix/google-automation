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
			IP:          host,
			Port:        port,
			Protocol:    "http",
			Country:     strings.ToUpper(country),
			Username:    username,
			Password:    pass,
			APIKeyIndex: 0,
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

		parts := strings.Split(line, ":")
		if len(parts) >= 2 {
			ip := parts[0]
			port, err := strconv.Atoi(parts[1])
			if err != nil {
				continue
			}
			user := ""
			pass := ""
			if len(parts) >= 4 {
				user = parts[2]
				pass = parts[3]
			}
			out = append(out, Proxy{
				IP:       ip,
				Port:     port,
				Protocol: "http",
				Username: user,
				Password: pass,
			})
		}
	}
	return out, scanner.Err()
}

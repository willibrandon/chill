package radio

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// DetectLocalCountry infers an ISO country code from local system settings.
// It performs no network request and should only be called after user consent.
func DetectLocalCountry() string {
	zone := currentZone()
	if code := countryForZone(zone); code != "" {
		return code
	}
	for _, key := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if code := countryFromLocale(os.Getenv(key)); code != "" {
			return code
		}
	}
	return platformCountry()
}

func currentZone() string {
	if zone := strings.TrimPrefix(strings.TrimSpace(os.Getenv("TZ")), ":"); zone != "" && !strings.HasPrefix(zone, "/") {
		return zone
	}
	if target, err := filepath.EvalSymlinks("/etc/localtime"); err == nil {
		if _, after, ok := strings.Cut(filepath.ToSlash(target), "/zoneinfo/"); ok {
			return after
		}
	}
	if data, err := os.ReadFile("/etc/timezone"); err == nil {
		return strings.TrimSpace(string(data))
	}
	if name := time.Now().Location().String(); name != "Local" {
		return name
	}
	return ""
}

func countryForZone(zone string) string {
	if zone == "" {
		return ""
	}
	paths := []string{"/usr/share/zoneinfo/zone1970.tab", "/usr/share/zoneinfo/zone.tab"}
	if runtime.GOOS == "darwin" {
		paths = append([]string{"/var/db/timezone/zoneinfo/zone1970.tab", "/var/db/timezone/zoneinfo/zone.tab"}, paths...)
	}
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" || line[0] == '#' {
				continue
			}
			fields := strings.Split(line, "\t")
			if len(fields) >= 3 && fields[2] == zone {
				f.Close()
				code := strings.Split(fields[0], ",")[0]
				if len(code) == 2 {
					return strings.ToUpper(code)
				}
				return ""
			}
		}
		f.Close()
	}
	return ""
}

func countryFromLocale(locale string) string {
	locale = strings.TrimSpace(locale)
	if separator := strings.IndexAny(locale, "_-"); separator >= 0 && len(locale) >= separator+3 {
		code := strings.ToUpper(locale[separator+1 : separator+3])
		if code[0] >= 'A' && code[0] <= 'Z' && code[1] >= 'A' && code[1] <= 'Z' {
			return code
		}
	}
	return ""
}

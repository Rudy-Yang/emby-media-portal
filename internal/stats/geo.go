package stats

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"emby-media-portal/internal/database"
)

const (
	geoScopeChina = "china"
	geoScopeWorld = "world"
	geoCacheTTL   = 30 * 24 * time.Hour
)

type RegionUserTraffic struct {
	UserID        string `json:"user_id"`
	UserName      string `json:"user_name"`
	TotalBytesIn  int64  `json:"total_bytes_in"`
	TotalBytesOut int64  `json:"total_bytes_out"`
	RequestCount  int64  `json:"request_count"`
}

type TrafficRegion struct {
	Key           string              `json:"key"`
	MapName       string              `json:"map_name"`
	DisplayName   string              `json:"display_name"`
	CountryCode   string              `json:"country_code"`
	CountryName   string              `json:"country_name"`
	RegionName    string              `json:"region_name"`
	TotalBytesIn  int64               `json:"total_bytes_in"`
	TotalBytesOut int64               `json:"total_bytes_out"`
	RequestCount  int64               `json:"request_count"`
	Users         []RegionUserTraffic `json:"users"`
}

type geoCacheEntry struct {
	IP          string
	CountryCode string
	CountryName string
	RegionName  string
	CityName    string
	UpdatedAt   time.Time
}

type ipwhoisResponse struct {
	Success     bool   `json:"success"`
	IP          string `json:"ip"`
	Country     string `json:"country"`
	CountryCode string `json:"country_code"`
	Region      string `json:"region"`
	City        string `json:"city"`
	Message     string `json:"message"`
}

type geoJSONCacheEntry struct {
	ContentType string
	Body        []byte
	FetchedAt   time.Time
}

var (
	geoLookupClient = &http.Client{Timeout: 4 * time.Second}
	geoLookupMu     sync.Mutex
	geoJSONCache    = map[string]geoJSONCacheEntry{}
)

func NormalizeGeoScope(scope string) string {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case geoScopeWorld:
		return geoScopeWorld
	default:
		return geoScopeChina
	}
}

func GetTrafficRegions(since time.Time, scope string) ([]TrafficRegion, error) {
	db := database.Get()
	if db == nil {
		return nil, ErrDatabaseNotAvailable
	}

	scope = NormalizeGeoScope(scope)
	geoByIP, err := warmGeoCache(db, since)
	if err != nil {
		return nil, err
	}

	rows, err := db.Query(
		`SELECT
		     COALESCE(t.client_ip, ''),
		     t.user_id,
		     `+trafficEntryDisplayUserExpr+` AS display_user_name,
		     COALESCE(t.bytes_in, 0),
		     COALESCE(t.bytes_out, 0)
		 FROM traffic_stats t
		 LEFT JOIN users u ON u.id = t.user_id
		 WHERE t.timestamp >= ?
		   AND COALESCE(TRIM(t.client_ip), '') <> ''`,
		since,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type regionBucket struct {
		TrafficRegion
		userByKey map[string]*RegionUserTraffic
	}

	regions := make(map[string]*regionBucket)
	for rows.Next() {
		var clientIP, userID, userName string
		var bytesIn, bytesOut int64
		if err := rows.Scan(&clientIP, &userID, &userName, &bytesIn, &bytesOut); err != nil {
			return nil, err
		}

		geo, ok := geoByIP[normalizeClientIP(clientIP)]
		if !ok {
			continue
		}

		key, mapName, displayName, include := regionIdentity(scope, geo)
		if !include || key == "" {
			continue
		}

		bucket := regions[key]
		if bucket == nil {
			bucket = &regionBucket{
				TrafficRegion: TrafficRegion{
					Key:         key,
					MapName:     mapName,
					DisplayName: displayName,
					CountryCode: geo.CountryCode,
					CountryName: geo.CountryName,
					RegionName:  geo.RegionName,
				},
				userByKey: make(map[string]*RegionUserTraffic),
			}
			regions[key] = bucket
		}

		bucket.TotalBytesIn += bytesIn
		bucket.TotalBytesOut += bytesOut
		bucket.RequestCount++

		userKey := strings.TrimSpace(userID)
		if userKey == "" {
			userKey = userName
		}
		item := bucket.userByKey[userKey]
		if item == nil {
			item = &RegionUserTraffic{
				UserID:   userID,
				UserName: userName,
			}
			bucket.userByKey[userKey] = item
		}
		item.TotalBytesIn += bytesIn
		item.TotalBytesOut += bytesOut
		item.RequestCount++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	regionList := make([]TrafficRegion, 0, len(regions))
	for _, bucket := range regions {
		users := make([]RegionUserTraffic, 0, len(bucket.userByKey))
		for _, item := range bucket.userByKey {
			users = append(users, *item)
		}
		sort.Slice(users, func(i, j int) bool {
			if users[i].TotalBytesOut != users[j].TotalBytesOut {
				return users[i].TotalBytesOut > users[j].TotalBytesOut
			}
			if users[i].TotalBytesIn != users[j].TotalBytesIn {
				return users[i].TotalBytesIn > users[j].TotalBytesIn
			}
			if users[i].RequestCount != users[j].RequestCount {
				return users[i].RequestCount > users[j].RequestCount
			}
			return users[i].UserName < users[j].UserName
		})
		bucket.Users = users
		regionList = append(regionList, bucket.TrafficRegion)
	}

	sort.Slice(regionList, func(i, j int) bool {
		if regionList[i].TotalBytesOut != regionList[j].TotalBytesOut {
			return regionList[i].TotalBytesOut > regionList[j].TotalBytesOut
		}
		if regionList[i].TotalBytesIn != regionList[j].TotalBytesIn {
			return regionList[i].TotalBytesIn > regionList[j].TotalBytesIn
		}
		if regionList[i].RequestCount != regionList[j].RequestCount {
			return regionList[i].RequestCount > regionList[j].RequestCount
		}
		return regionList[i].DisplayName < regionList[j].DisplayName
	})

	return regionList, nil
}

func GetTrafficMapGeoJSON(scope string) (string, []byte, error) {
	scope = NormalizeGeoScope(scope)

	geoLookupMu.Lock()
	cached, ok := geoJSONCache[scope]
	geoLookupMu.Unlock()
	if ok && time.Since(cached.FetchedAt) < 24*time.Hour && len(cached.Body) > 0 {
		return cached.ContentType, cached.Body, nil
	}

	url := "https://echarts.apache.org/examples/data/asset/geo/world.json"
	if scope == geoScopeChina {
		url = "https://geo.datav.aliyun.com/areas_v3/bound/100000_full.json"
	}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("User-Agent", "emby-media-portal/1.0")

	resp, err := geoLookupClient.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", nil, fmt.Errorf("map source returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", nil, err
	}
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json; charset=utf-8"
	}

	geoLookupMu.Lock()
	geoJSONCache[scope] = geoJSONCacheEntry{
		ContentType: contentType,
		Body:        body,
		FetchedAt:   time.Now(),
	}
	geoLookupMu.Unlock()

	return contentType, body, nil
}

func warmGeoCache(db *sql.DB, since time.Time) (map[string]geoCacheEntry, error) {
	ips, err := distinctClientIPs(db, since)
	if err != nil {
		return nil, err
	}

	cached, err := loadGeoCacheEntries(db, ips)
	if err != nil {
		return nil, err
	}

	for _, ip := range ips {
		if ip == "" {
			continue
		}
		entry, ok := cached[ip]
		if ok && time.Since(entry.UpdatedAt) < geoCacheTTL {
			continue
		}

		resolved, err := resolveGeoCacheEntry(ip)
		if err != nil {
			continue
		}
		if err := upsertGeoCacheEntry(db, resolved); err != nil {
			continue
		}
		cached[ip] = resolved
	}

	return cached, nil
}

func distinctClientIPs(db *sql.DB, since time.Time) ([]string, error) {
	rows, err := db.Query(
		`SELECT DISTINCT COALESCE(TRIM(client_ip), '')
		 FROM traffic_stats
		 WHERE timestamp >= ?
		   AND COALESCE(TRIM(client_ip), '') <> ''`,
		since,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ips := make([]string, 0)
	for rows.Next() {
		var ip string
		if err := rows.Scan(&ip); err != nil {
			return nil, err
		}
		ip = normalizeClientIP(ip)
		if ip != "" {
			ips = append(ips, ip)
		}
	}
	return ips, rows.Err()
}

func loadGeoCacheEntries(db *sql.DB, ips []string) (map[string]geoCacheEntry, error) {
	entries := make(map[string]geoCacheEntry, len(ips))
	if len(ips) == 0 {
		return entries, nil
	}

	placeholders := make([]string, 0, len(ips))
	args := make([]any, 0, len(ips))
	for _, ip := range ips {
		placeholders = append(placeholders, "?")
		args = append(args, ip)
	}

	rows, err := db.Query(
		`SELECT ip, COALESCE(country_code, ''), COALESCE(country_name, ''), COALESCE(region_name, ''), COALESCE(city_name, ''), COALESCE(updated_at, CURRENT_TIMESTAMP)
		 FROM ip_geo_cache
		 WHERE ip IN (`+strings.Join(placeholders, ",")+`)`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var entry geoCacheEntry
		var updatedAt string
		if err := rows.Scan(&entry.IP, &entry.CountryCode, &entry.CountryName, &entry.RegionName, &entry.CityName, &updatedAt); err != nil {
			return nil, err
		}
		entry.UpdatedAt = parseGeoCacheTime(updatedAt)
		entries[entry.IP] = entry
	}
	return entries, rows.Err()
}

func resolveGeoCacheEntry(ip string) (geoCacheEntry, error) {
	ip = normalizeClientIP(ip)
	if ip == "" {
		return geoCacheEntry{}, errors.New("empty ip")
	}

	if isPrivateOrReservedIP(ip) {
		return geoCacheEntry{
			IP:          ip,
			CountryCode: "LAN",
			CountryName: "局域网 / 保留地址",
			RegionName:  "局域网 / 保留地址",
			CityName:    "",
			UpdatedAt:   time.Now(),
		}, nil
	}

	req, err := http.NewRequest(http.MethodGet, "https://ipwho.is/"+ip, nil)
	if err != nil {
		return geoCacheEntry{}, err
	}
	req.Header.Set("User-Agent", "emby-media-portal/1.0")

	resp, err := geoLookupClient.Do(req)
	if err != nil {
		return geoCacheEntry{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return geoCacheEntry{}, fmt.Errorf("geo lookup returned HTTP %d", resp.StatusCode)
	}

	var payload ipwhoisResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return geoCacheEntry{}, err
	}
	if !payload.Success {
		return geoCacheEntry{}, fmt.Errorf("geo lookup failed: %s", strings.TrimSpace(payload.Message))
	}

	return geoCacheEntry{
		IP:          ip,
		CountryCode: strings.ToUpper(strings.TrimSpace(payload.CountryCode)),
		CountryName: strings.TrimSpace(payload.Country),
		RegionName:  strings.TrimSpace(payload.Region),
		CityName:    strings.TrimSpace(payload.City),
		UpdatedAt:   time.Now(),
	}, nil
}

func upsertGeoCacheEntry(db *sql.DB, entry geoCacheEntry) error {
	_, err := db.Exec(
		`INSERT INTO ip_geo_cache (ip, country_code, country_name, region_name, city_name, updated_at)
		 VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(ip) DO UPDATE SET
		   country_code = excluded.country_code,
		   country_name = excluded.country_name,
		   region_name = excluded.region_name,
		   city_name = excluded.city_name,
		   updated_at = CURRENT_TIMESTAMP`,
		entry.IP,
		entry.CountryCode,
		entry.CountryName,
		entry.RegionName,
		entry.CityName,
	)
	return err
}

func parseGeoCacheTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05Z07:00",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

func normalizeClientIP(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	value = strings.Trim(value, "[]")
	if addr, err := netip.ParseAddr(value); err == nil {
		return addr.String()
	}
	return value
}

func isPrivateOrReservedIP(value string) bool {
	addr, err := netip.ParseAddr(normalizeClientIP(value))
	if err != nil {
		return true
	}
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsMulticast() || addr.IsUnspecified() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() {
		return true
	}
	if addr.Is4() {
		for _, prefix := range []string{
			"100.64.0.0/10",
			"169.254.0.0/16",
			"192.0.0.0/24",
			"192.0.2.0/24",
			"198.18.0.0/15",
			"198.51.100.0/24",
			"203.0.113.0/24",
			"224.0.0.0/4",
			"240.0.0.0/4",
		} {
			if netip.MustParsePrefix(prefix).Contains(addr) {
				return true
			}
		}
	}
	return false
}

func regionIdentity(scope string, geo geoCacheEntry) (string, string, string, bool) {
	countryCode := strings.ToUpper(strings.TrimSpace(geo.CountryCode))
	switch scope {
	case geoScopeWorld:
		mapName, displayName, ok := worldRegionNames(countryCode, geo.CountryName)
		if !ok {
			return "", "", "", false
		}
		return "world:" + countryCode + ":" + mapName, mapName, displayName, true
	default:
		if countryCode != "CN" {
			return "", "", "", false
		}
		mapName, displayName, ok := chinaProvinceNames(geo.RegionName)
		if !ok {
			return "", "", "", false
		}
		return "china:" + mapName, mapName, displayName, true
	}
}

func worldRegionNames(countryCode, countryName string) (string, string, bool) {
	if countryCode == "" {
		return "", "", false
	}
	if alias, ok := worldCountryNameMap[countryCode]; ok {
		return alias.MapName, alias.DisplayName, true
	}
	name := strings.TrimSpace(countryName)
	if name == "" {
		return "", "", false
	}
	return name, name, true
}

func chinaProvinceNames(region string) (string, string, bool) {
	key := strings.ToLower(strings.TrimSpace(region))
	if key == "" {
		return "", "", false
	}
	if alias, ok := chinaProvinceNameMap[key]; ok {
		return alias, alias, true
	}
	return "", "", false
}

type worldCountryAlias struct {
	MapName     string
	DisplayName string
}

var worldCountryNameMap = map[string]worldCountryAlias{
	"CN": {MapName: "China", DisplayName: "中国"},
	"US": {MapName: "United States of America", DisplayName: "美国"},
	"CA": {MapName: "Canada", DisplayName: "加拿大"},
	"GB": {MapName: "United Kingdom", DisplayName: "英国"},
	"RU": {MapName: "Russia", DisplayName: "俄罗斯"},
	"JP": {MapName: "Japan", DisplayName: "日本"},
	"KR": {MapName: "South Korea", DisplayName: "韩国"},
	"DE": {MapName: "Germany", DisplayName: "德国"},
	"FR": {MapName: "France", DisplayName: "法国"},
	"SG": {MapName: "Singapore", DisplayName: "新加坡"},
	"AU": {MapName: "Australia", DisplayName: "澳大利亚"},
	"NZ": {MapName: "New Zealand", DisplayName: "新西兰"},
	"MY": {MapName: "Malaysia", DisplayName: "马来西亚"},
	"TH": {MapName: "Thailand", DisplayName: "泰国"},
	"VN": {MapName: "Vietnam", DisplayName: "越南"},
	"PH": {MapName: "Philippines", DisplayName: "菲律宾"},
	"IN": {MapName: "India", DisplayName: "印度"},
}

var chinaProvinceNameMap = map[string]string{
	"beijing":        "北京市",
	"北京市":            "北京市",
	"tianjin":        "天津市",
	"天津市":            "天津市",
	"shanghai":       "上海市",
	"上海市":            "上海市",
	"chongqing":      "重庆市",
	"重庆市":            "重庆市",
	"hebei":          "河北省",
	"河北省":            "河北省",
	"shanxi":         "山西省",
	"山西省":            "山西省",
	"inner mongolia": "内蒙古自治区",
	"内蒙古":            "内蒙古自治区",
	"内蒙古自治区":         "内蒙古自治区",
	"liaoning":       "辽宁省",
	"辽宁省":            "辽宁省",
	"jilin":          "吉林省",
	"吉林省":            "吉林省",
	"heilongjiang":   "黑龙江省",
	"黑龙江":            "黑龙江省",
	"黑龙江省":           "黑龙江省",
	"jiangsu":        "江苏省",
	"江苏省":            "江苏省",
	"zhejiang":       "浙江省",
	"浙江省":            "浙江省",
	"anhui":          "安徽省",
	"安徽省":            "安徽省",
	"fujian":         "福建省",
	"福建省":            "福建省",
	"jiangxi":        "江西省",
	"江西省":            "江西省",
	"shandong":       "山东省",
	"山东省":            "山东省",
	"henan":          "河南省",
	"河南省":            "河南省",
	"hubei":          "湖北省",
	"湖北省":            "湖北省",
	"hunan":          "湖南省",
	"湖南省":            "湖南省",
	"guangdong":      "广东省",
	"广东省":            "广东省",
	"guangxi":        "广西壮族自治区",
	"广西":             "广西壮族自治区",
	"广西壮族自治区":        "广西壮族自治区",
	"hainan":         "海南省",
	"海南省":            "海南省",
	"sichuan":        "四川省",
	"四川省":            "四川省",
	"guizhou":        "贵州省",
	"贵州省":            "贵州省",
	"yunnan":         "云南省",
	"云南省":            "云南省",
	"xizang":         "西藏自治区",
	"tibet":          "西藏自治区",
	"西藏":             "西藏自治区",
	"西藏自治区":          "西藏自治区",
	"shaanxi":        "陕西省",
	"陕西省":            "陕西省",
	"gansu":          "甘肃省",
	"甘肃省":            "甘肃省",
	"qinghai":        "青海省",
	"青海省":            "青海省",
	"ningxia":        "宁夏回族自治区",
	"宁夏":             "宁夏回族自治区",
	"宁夏回族自治区":        "宁夏回族自治区",
	"xinjiang":       "新疆维吾尔自治区",
	"新疆":             "新疆维吾尔自治区",
	"新疆维吾尔自治区":       "新疆维吾尔自治区",
	"hong kong":      "香港特别行政区",
	"hong kong sar":  "香港特别行政区",
	"香港":             "香港特别行政区",
	"香港特别行政区":        "香港特别行政区",
	"macau":          "澳门特别行政区",
	"macao":          "澳门特别行政区",
	"macau sar":      "澳门特别行政区",
	"澳门":             "澳门特别行政区",
	"澳门特别行政区":        "澳门特别行政区",
	"taiwan":         "台湾省",
	"台湾":             "台湾省",
	"台湾省":            "台湾省",
}

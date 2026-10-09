package core

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type NodeGeoInfo struct {
	IP          string `json:"ip"`
	CountryCode string `json:"country_code"`
	Country     string `json:"country"`
	City        string `json:"city"`
	ISP         string `json:"isp"`
}

// DiscoverNodeGeo detects the worker node's external public network characteristics
func DiscoverNodeGeo() NodeGeoInfo {
	client := &http.Client{
		Timeout: 4 * time.Second,
	}

	resp, err := client.Get("http://ip-api.com/json/?fields=status,message,country,countryCode,city,isp,query")
	if err == nil && resp.StatusCode == http.StatusOK {
		defer resp.Body.Close()
		var res struct {
			Status      string `json:"status"`
			Query       string `json:"query"`
			CountryCode string `json:"countryCode"`
			Country     string `json:"country"`
			City        string `json:"city"`
			ISP         string `json:"isp"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&res); err == nil && res.Status == "success" {
			return NodeGeoInfo{
				IP:          res.Query,
				CountryCode: res.CountryCode,
				Country:     res.Country,
				City:        res.City,
				ISP:         res.ISP,
			}
		}
	}

	// Fallback if IP-API is unreachable or rate-limited
	return NodeGeoInfo{
		IP:          "127.0.0.1",
		CountryCode: "US",
		Country:     "United States (Local)",
		City:        "Unknown",
		ISP:         "Residential ISP",
	}
}

func (g NodeGeoInfo) String() string {
	return fmt.Sprintf("%s (%s, %s) via %s", g.IP, g.City, g.CountryCode, g.ISP)
}

package control

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"cdn-platform/internal/domain"
	"cdn-platform/internal/logstore"
)

const overviewWindow = 24 * time.Hour

type overviewTotals struct {
	Requests      uint64 `json:"requests"`
	Bytes         int64  `json:"bytes"`
	ErrorRequests uint64 `json:"error_requests"`
}

type overviewSeriesPoint struct {
	Time          time.Time `json:"time"`
	Requests      uint64    `json:"requests"`
	Bytes         int64     `json:"bytes"`
	ErrorRequests uint64    `json:"error_requests"`
}

type overviewSitePoint struct {
	Time     time.Time `json:"time"`
	Requests uint64    `json:"requests"`
}

type overviewStatusCode struct {
	Code     uint16 `json:"code"`
	Requests uint64 `json:"requests"`
}

type overviewSite struct {
	ID       string              `json:"id"`
	Name     string              `json:"name"`
	Domains  []string            `json:"domains"`
	Requests uint64              `json:"requests"`
	Bytes    int64               `json:"bytes"`
	Series   []overviewSitePoint `json:"series"`
}

type overviewPayload struct {
	From          time.Time             `json:"from"`
	To            time.Time             `json:"to"`
	BucketSeconds int                   `json:"bucket_seconds"`
	Totals        overviewTotals        `json:"totals"`
	Series        []overviewSeriesPoint `json:"series"`
	StatusCodes   []overviewStatusCode  `json:"status_codes"`
	Sites         []overviewSite        `json:"sites"`
}

func (s *Server) overview(response http.ResponseWriter, request *http.Request) {
	to := time.Now().UTC().Truncate(time.Second)
	from := to.Add(-overviewWindow)
	buckets := make([]logstore.OverviewBucket, 0)
	if s.Logs != nil {
		var err error
		buckets, err = s.Logs.Overview(request.Context(), from, to)
		if err != nil {
			writeError(response, http.StatusBadGateway, err)
			return
		}
	}
	sites, err := s.Store.ListSites()
	if err != nil {
		writeStoreError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, buildOverviewPayload(from, to, sites, buckets))
}

func buildOverviewPayload(from, to time.Time, configuredSites []domain.Site, buckets []logstore.OverviewBucket) overviewPayload {
	times := overviewBucketTimes(from, to)
	series := make([]overviewSeriesPoint, len(times))
	timeIndexes := make(map[int64]int, len(times))
	for index, bucketTime := range times {
		series[index].Time = bucketTime
		timeIndexes[bucketTime.Unix()] = index
	}

	sites := make([]overviewSite, 0, len(configuredSites))
	siteIndexes := make(map[string]int, len(configuredSites))
	for _, site := range configuredSites {
		siteSeries := make([]overviewSitePoint, len(times))
		for index, bucketTime := range times {
			siteSeries[index].Time = bucketTime
		}
		siteIndexes[site.ID] = len(sites)
		sites = append(sites, overviewSite{ID: site.ID, Name: site.Name, Domains: append([]string{}, site.Domains...), Series: siteSeries})
	}

	payload := overviewPayload{From: from, To: to, BucketSeconds: int(time.Hour.Seconds()), Series: series, StatusCodes: make([]overviewStatusCode, 0), Sites: sites}
	statusCounts := make(map[uint16]uint64)
	for _, bucket := range buckets {
		if index, ok := timeIndexes[bucket.Hour.UTC().Truncate(time.Hour).Unix()]; ok {
			payload.Totals.Requests += bucket.Requests
			payload.Totals.Bytes += bucket.Bytes
			statusCounts[bucket.Status] += bucket.Requests
			isError := bucket.Status >= 400
			if isError {
				payload.Totals.ErrorRequests += bucket.Requests
			}
			payload.Series[index].Requests += bucket.Requests
			payload.Series[index].Bytes += bucket.Bytes
			if isError {
				payload.Series[index].ErrorRequests += bucket.Requests
			}
			if siteIndex, exists := siteIndexes[bucket.SiteID]; exists {
				payload.Sites[siteIndex].Requests += bucket.Requests
				payload.Sites[siteIndex].Bytes += bucket.Bytes
				payload.Sites[siteIndex].Series[index].Requests += bucket.Requests
			}
		}
	}

	for code, requests := range statusCounts {
		payload.StatusCodes = append(payload.StatusCodes, overviewStatusCode{Code: code, Requests: requests})
	}
	sort.Slice(payload.StatusCodes, func(i, j int) bool {
		if payload.StatusCodes[i].Requests == payload.StatusCodes[j].Requests {
			return payload.StatusCodes[i].Code < payload.StatusCodes[j].Code
		}
		return payload.StatusCodes[i].Requests > payload.StatusCodes[j].Requests
	})
	sort.SliceStable(payload.Sites, func(i, j int) bool {
		if payload.Sites[i].Requests == payload.Sites[j].Requests {
			left, right := strings.ToLower(payload.Sites[i].Name), strings.ToLower(payload.Sites[j].Name)
			if left == right {
				return payload.Sites[i].ID < payload.Sites[j].ID
			}
			return left < right
		}
		return payload.Sites[i].Requests > payload.Sites[j].Requests
	})
	return payload
}

func overviewBucketTimes(from, to time.Time) []time.Time {
	start := from.UTC().Truncate(time.Hour)
	end := to.UTC().Add(-time.Nanosecond).Truncate(time.Hour)
	if end.Before(start) {
		return []time.Time{}
	}
	times := make([]time.Time, 0, int(end.Sub(start)/time.Hour)+1)
	for current := start; !current.After(end); current = current.Add(time.Hour) {
		times = append(times, current)
	}
	return times
}

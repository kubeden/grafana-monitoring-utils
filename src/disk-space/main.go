package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/shirou/gopsutil/disk"
)

// DiskMetrics holds the disk space information
type DiskMetrics struct {
	Timestamp  int64              `json:"timestamp"`
	Partitions []PartitionMetrics `json:"partitions"`
}

type PartitionMetrics struct {
	Path         string  `json:"path"`
	Total        uint64  `json:"total"`
	Used         uint64  `json:"used"`
	Free         uint64  `json:"free"`
	UsagePercent float64 `json:"usagePercent"`
}

// TimeserieResponse represents Grafana JSON response format
type TimeserieResponse struct {
	Target     string      `json:"target"`
	Datapoints [][]float64 `json:"datapoints"`
}

// MetricsStore keeps historical metrics
type MetricsStore struct {
	data    []DiskMetrics
	maxSize int
}

var (
	store = &MetricsStore{
		data:    make([]DiskMetrics, 0),
		maxSize: 60 * 24, // Store 24 hours of minute-resolution data
	}

	diskUsage = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "disk_usage_bytes",
			Help: "Disk usage in bytes",
		},
		[]string{"path", "type"},
	)

	diskUsagePercent = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "disk_usage_percent",
			Help: "Disk usage percentage",
		},
		[]string{"path"},
	)
)

func init() {
	prometheus.MustRegister(diskUsage)
	prometheus.MustRegister(diskUsagePercent)
}

func (s *MetricsStore) add(metrics DiskMetrics) {
	s.data = append(s.data, metrics)
	if len(s.data) > s.maxSize {
		s.data = s.data[1:]
	}
}

func (s *MetricsStore) getRange(fromTime, toTime time.Time) []DiskMetrics {
	result := make([]DiskMetrics, 0)
	for _, m := range s.data {
		ts := time.Unix(m.Timestamp, 0)
		if ts.After(fromTime) && ts.Before(toTime) || ts.Equal(fromTime) || ts.Equal(toTime) {
			result = append(result, m)
		}
	}
	return result
}

// Helper function to sort datapoints by timestamp
func sortDatapoints(datapoints [][]float64) [][]float64 {
	sorted := make([][]float64, len(datapoints))
	copy(sorted, datapoints)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i][1] < sorted[j][1]
	})
	return sorted
}

func collectMetrics() (*DiskMetrics, error) {
	partitions, err := disk.Partitions(false)
	if err != nil {
		return nil, err
	}

	metrics := &DiskMetrics{
		Timestamp:  time.Now().Unix(),
		Partitions: make([]PartitionMetrics, 0),
	}

	for _, partition := range partitions {
		usage, err := disk.Usage(partition.Mountpoint)
		if err != nil {
			log.Printf("Error getting usage for %s: %v", partition.Mountpoint, err)
			continue
		}

		// Update Prometheus metrics
		diskUsage.WithLabelValues(partition.Mountpoint, "total").Set(float64(usage.Total))
		diskUsage.WithLabelValues(partition.Mountpoint, "used").Set(float64(usage.Used))
		diskUsage.WithLabelValues(partition.Mountpoint, "free").Set(float64(usage.Free))
		diskUsagePercent.WithLabelValues(partition.Mountpoint).Set(usage.UsedPercent)

		// Store metrics for JSON endpoint
		metrics.Partitions = append(metrics.Partitions, PartitionMetrics{
			Path:         partition.Mountpoint,
			Total:        usage.Total,
			Used:         usage.Used,
			Free:         usage.Free,
			UsagePercent: usage.UsedPercent,
		})
	}

	store.add(*metrics)
	return metrics, nil
}

func metricsHandler(w http.ResponseWriter, r *http.Request) {
	metrics, err := collectMetrics()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(metrics)
}

func grafanaHandler(w http.ResponseWriter, r *http.Request) {
	// Parse query parameters for filtering
	query := r.URL.Query()
	pathFilter := query.Get("path")

	// Parse time range parameters
	fromStr := query.Get("from")
	toStr := query.Get("to")

	// Parse Unix timestamps (Grafana sends milliseconds)
	fromMs, err := strconv.ParseInt(fromStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid 'from' parameter", http.StatusBadRequest)
		return
	}
	fromTime := time.Unix(fromMs/1000, 0)

	toMs, err := strconv.ParseInt(toStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid 'to' parameter", http.StatusBadRequest)
		return
	}
	toTime := time.Unix(toMs/1000, 0)

	// Get metrics for the time range
	metrics := store.getRange(fromTime, toTime)

	response := make([]TimeserieResponse, 0)

	// Group metrics by path
	pathMetrics := make(map[string][][]float64)

	for _, m := range metrics {
		timestamp := float64(m.Timestamp * 1000) // Convert to milliseconds

		for _, partition := range m.Partitions {
			// Filter by path if specified
			if pathFilter != "" && partition.Path != pathFilter {
				continue
			}

			// Initialize map entries if they don't exist
			usedKey := partition.Path + " - Used"
			freeKey := partition.Path + " - Free"
			percentKey := partition.Path + " - Usage %"

			if _, exists := pathMetrics[usedKey]; !exists {
				pathMetrics[usedKey] = make([][]float64, 0)
				pathMetrics[freeKey] = make([][]float64, 0)
				pathMetrics[percentKey] = make([][]float64, 0)
			}

			// Add datapoints
			pathMetrics[usedKey] = append(pathMetrics[usedKey], []float64{float64(partition.Used), timestamp})
			pathMetrics[freeKey] = append(pathMetrics[freeKey], []float64{float64(partition.Free), timestamp})
			pathMetrics[percentKey] = append(pathMetrics[percentKey], []float64{partition.UsagePercent, timestamp})
		}
	}

	// Convert map to response array
	for target, datapoints := range pathMetrics {
		response = append(response, TimeserieResponse{
			Target:     target,
			Datapoints: sortDatapoints(datapoints),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func parseSimpleTime(timeStr string) (time.Duration, error) {
	// Remove any whitespace
	timeStr = strings.TrimSpace(timeStr)

	// Check if the string is empty
	if timeStr == "" {
		return 0, fmt.Errorf("empty time string")
	}

	// Get the last character (unit) and the number
	unit := timeStr[len(timeStr)-1:]
	number := timeStr[:len(timeStr)-1]

	// Parse the number
	value, err := strconv.Atoi(number)
	if err != nil {
		return 0, fmt.Errorf("invalid number format: %s", number)
	}

	// Convert to time.Duration based on unit
	switch strings.ToLower(unit) {
	case "m":
		return time.Duration(value) * time.Minute, nil
	case "h":
		return time.Duration(value) * time.Hour, nil
	case "d":
		return time.Duration(value) * 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("unsupported time unit: %s", unit)
	}
}

func grafanaSimpleHandler(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	path := query.Get("path")
	simpleTime := query.Get("time")

	// Parse the simple time format
	duration, err := parseSimpleTime(simpleTime)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid time format: %v", err), http.StatusBadRequest)
		return
	}

	// Calculate the time range
	now := time.Now()
	from := now.Add(-duration)

	// Convert to milliseconds timestamps
	fromMs := from.UnixNano() / int64(time.Millisecond)
	toMs := now.UnixNano() / int64(time.Millisecond)

	// Redirect to the main grafana endpoint
	redirectURL := fmt.Sprintf("/grafana?path=%s&from=%d&to=%d", path, fromMs, toMs)
	http.Redirect(w, r, redirectURL, http.StatusTemporaryRedirect)
}

func main() {
	// Start metrics collection in background
	go func() {
		for {
			if _, err := collectMetrics(); err != nil {
				log.Printf("Error collecting metrics: %v", err)
			}
			time.Sleep(1 * time.Minute)
		}
	}()

	// Regular metrics endpoint
	http.HandleFunc("/metrics/disk", metricsHandler)

	// Grafana JSON datasource endpoints
	http.HandleFunc("/grafana", grafanaHandler)
	http.HandleFunc("/grafana/simple", grafanaSimpleHandler)

	// Prometheus metrics endpoint
	http.Handle("/metrics", promhttp.Handler())

	log.Println("Starting server on :8080")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatal(err)
	}
}

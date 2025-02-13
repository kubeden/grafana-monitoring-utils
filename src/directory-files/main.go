package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type FileCount struct {
	Directory string    `json:"directory"`
	Count     int       `json:"count"`
	Timestamp time.Time `json:"time"` // Changed from "timestamp" to "time" for Grafana
}

type FileResponse struct {
	Target     string          `json:"target"`
	Datapoints [][]interface{} `json:"datapoints"`
}

var db *sql.DB

func initDB() error {
	var err error
	db, err = sql.Open("sqlite3", "./filemonitor.db")
	if err != nil {
		return err
	}

	// Create table if it doesn't exist
	createTable := `
    CREATE TABLE IF NOT EXISTS file_counts (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        directory TEXT NOT NULL,
        count INTEGER NOT NULL,
        timestamp DATETIME NOT NULL
    );
    CREATE INDEX IF NOT EXISTS idx_directory_timestamp ON file_counts(directory, timestamp);
    `
	_, err = db.Exec(createTable)
	return err
}

func countFiles(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	return len(entries), nil
}

func monitorDirectory(dir string) {
	// Initial count
	count, err := countFiles(dir)
	if err != nil {
		log.Printf("Error counting files in %s: %v", dir, err)
	} else {
		_, err = db.Exec(
			"INSERT INTO file_counts (directory, count, timestamp) VALUES (?, ?, ?)",
			dir, count, time.Now(),
		)
		if err != nil {
			log.Printf("Error inserting into database: %v", err)
		}
	}

	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		count, err := countFiles(dir)
		if err != nil {
			log.Printf("Error counting files in %s: %v", dir, err)
			continue
		}

		_, err = db.Exec(
			"INSERT INTO file_counts (directory, count, timestamp) VALUES (?, ?, ?)",
			dir, count, time.Now(),
		)
		if err != nil {
			log.Printf("Error inserting into database: %v", err)
		}
	}
}

func handleFiles(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("dir")
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")

	if dir == "" {
		http.Error(w, "dir parameter is required", http.StatusBadRequest)
		return
	}

	fromTime, err := time.Parse(time.RFC3339, from)
	if err != nil {
		http.Error(w, "invalid from time format", http.StatusBadRequest)
		return
	}

	toTime, err := time.Parse(time.RFC3339, to)
	if err != nil {
		http.Error(w, "invalid to time format", http.StatusBadRequest)
		return
	}

	rows, err := db.Query(
		"SELECT directory, count, timestamp FROM file_counts WHERE directory = ? AND timestamp BETWEEN ? AND ? ORDER BY timestamp",
		dir, fromTime, toTime,
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	counts := make([]FileCount, 0)
	for rows.Next() {
		var count FileCount
		err := rows.Scan(&count.Directory, &count.Count, &count.Timestamp)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		counts = append(counts, count)
	}

	// Convert to Grafana format
	datapoints := make([][]interface{}, len(counts))
	for i, count := range counts {
		datapoints[i] = []interface{}{
			count.Count,
			count.Timestamp.Unix() * 1000, // Grafana expects milliseconds
		}
	}

	response := []FileResponse{
		{
			Target:     "file_count",
			Datapoints: datapoints,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func handleSimple(w http.ResponseWriter, r *http.Request) {
	duration := r.URL.Query().Get("duration")
	if duration == "" {
		duration = "1h" // default to last hour if not specified
	}

	d, err := time.ParseDuration(duration)
	if err != nil {
		http.Error(w, "invalid duration format", http.StatusBadRequest)
		return
	}

	fromTime := time.Now().Add(-d)
	log.Printf("Querying data from %v onwards", fromTime)

	rows, err := db.Query(
		"SELECT directory, count, timestamp FROM file_counts WHERE timestamp > ? ORDER BY timestamp",
		fromTime,
	)
	if err != nil {
		log.Printf("Database query error: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	counts := make([]FileCount, 0)
	for rows.Next() {
		var count FileCount
		err := rows.Scan(&count.Directory, &count.Count, &count.Timestamp)
		if err != nil {
			log.Printf("Row scan error: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		counts = append(counts, count)
	}

	if len(counts) == 0 {
		// If no historical data, get current count
		for _, dir := range []string{"."} {
			count, err := countFiles(dir)
			if err != nil {
				log.Printf("Error counting files in %s: %v", dir, err)
				continue
			}
			counts = append(counts, FileCount{
				Directory: dir,
				Count:     count,
				Timestamp: time.Now(),
			})
		}
	}

	log.Printf("Returning %d records", len(counts))

	// Convert to Grafana format
	datapoints := make([][]interface{}, len(counts))
	for i, count := range counts {
		datapoints[i] = []interface{}{
			count.Count,
			count.Timestamp.Unix() * 1000, // Grafana expects milliseconds
		}
	}

	response := []FileResponse{
		{
			Target:     "file_count",
			Datapoints: datapoints,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func main() {
	if err := initDB(); err != nil {
		log.Fatal("Error initializing database:", err)
	}
	defer db.Close()

	// Use provided directories or default to current directory
	dirs := os.Args[1:]
	if len(dirs) == 0 {
		dirs = []string{"."}
	}

	// Start monitoring each directory
	for _, dir := range dirs {
		go monitorDirectory(dir)
	}

	http.HandleFunc("/files", handleFiles)
	http.HandleFunc("/simple", handleSimple)

	log.Println("Server starting on :8080")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatal(err)
	}
}

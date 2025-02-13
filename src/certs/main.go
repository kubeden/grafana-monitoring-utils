// main.go
package main

import (
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type Config struct {
	URLs []string `json:"urls"`
}

type CertInfo struct {
	URL           string    `json:"url"`
	IssuedTo      string    `json:"issued_to"`
	IssuedBy      string    `json:"issued_by"`
	ValidFrom     time.Time `json:"valid_from"`
	ValidUntil    time.Time `json:"valid_until"`
	DaysRemaining int       `json:"days_remaining"`
	CheckedAt     time.Time `json:"checked_at"`
}

var db *sql.DB

func init() {
	var err error
	db, err = sql.Open("sqlite3", "./certs.db")
	if err != nil {
		log.Fatal(err)
	}

	// Create table if not exists
	createTable := `
    CREATE TABLE IF NOT EXISTS cert_checks (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        url TEXT,
        issued_to TEXT,
        issued_by TEXT,
        valid_from DATETIME,
        valid_until DATETIME,
        days_remaining INTEGER,
        checked_at DATETIME
    );`

	_, err = db.Exec(createTable)
	if err != nil {
		log.Fatal(err)
	}
}

func loadConfig() Config {
	f, err := os.ReadFile("./config.json")
	if err != nil {
		log.Fatal(err)
	}

	var cfg Config
	err = json.Unmarshal(f, &cfg)
	if err != nil {
		log.Fatal(err)
	}
	return cfg
}

func getCertInfo(url string) (*CertInfo, error) {
	conn, err := tls.Dial("tcp", url+":443", &tls.Config{
		InsecureSkipVerify: true,
	})
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	cert := conn.ConnectionState().PeerCertificates[0]
	now := time.Now()
	daysRemaining := int(cert.NotAfter.Sub(now).Hours() / 24)

	return &CertInfo{
		URL:           url,
		IssuedTo:      cert.Subject.CommonName,
		IssuedBy:      cert.Issuer.CommonName,
		ValidFrom:     cert.NotBefore,
		ValidUntil:    cert.NotAfter,
		DaysRemaining: daysRemaining,
		CheckedAt:     now,
	}, nil
}

func storeCertInfo(info *CertInfo) error {
	query := `
    INSERT INTO cert_checks (
        url, issued_to, issued_by, valid_from, valid_until, days_remaining, checked_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?)`

	_, err := db.Exec(query,
		info.URL,
		info.IssuedTo,
		info.IssuedBy,
		info.ValidFrom.Format(time.RFC3339),
		info.ValidUntil.Format(time.RFC3339),
		info.DaysRemaining,
		info.CheckedAt.Format(time.RFC3339),
	)
	return err
}

func handleMetrics(w http.ResponseWriter, r *http.Request) {
	// Query to get latest cert info for each URL
	query := `
    WITH RankedCerts AS (
        SELECT *,
            ROW_NUMBER() OVER (PARTITION BY url ORDER BY checked_at DESC) as rn
        FROM cert_checks
    )
    SELECT url, issued_to, issued_by, valid_from, valid_until, days_remaining, checked_at
    FROM RankedCerts
    WHERE rn = 1`

	rows, err := db.Query(query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var metrics []string
	for rows.Next() {
		var info CertInfo
		var validFromStr, validUntilStr, checkedAtStr string

		err := rows.Scan(
			&info.URL,
			&info.IssuedTo,
			&info.IssuedBy,
			&validFromStr,
			&validUntilStr,
			&info.DaysRemaining,
			&checkedAtStr,
		)
		if err != nil {
			log.Printf("Error scanning row: %v", err)
			continue
		}

		// Main metric for days remaining
		metrics = append(metrics, fmt.Sprintf(
			"ssl_cert_days_remaining{url=\"%s\",issued_to=\"%s\",issuer=\"%s\"} %d",
			info.URL,
			info.IssuedTo,
			info.IssuedBy,
			info.DaysRemaining,
		))

		// Add a metric for certificate validity (1 = valid, 0 = expired)
		isValid := 0
		if info.DaysRemaining > 0 {
			isValid = 1
		}
		metrics = append(metrics, fmt.Sprintf(
			"ssl_cert_valid{url=\"%s\",issued_to=\"%s\",issuer=\"%s\"} %d",
			info.URL,
			info.IssuedTo,
			info.IssuedBy,
			isValid,
		))

		// Add expiry timestamp as unix timestamp
		validUntil, _ := time.Parse(time.RFC3339, validUntilStr)
		metrics = append(metrics, fmt.Sprintf(
			"ssl_cert_expiry_timestamp{url=\"%s\",issued_to=\"%s\",issuer=\"%s\"} %d",
			info.URL,
			info.IssuedTo,
			info.IssuedBy,
			validUntil.Unix(),
		))
	}

	w.Header().Set("Content-Type", "text/plain")
	for _, m := range metrics {
		fmt.Fprintln(w, m)
	}
}

func handleSimpleCerts(w http.ResponseWriter, r *http.Request) {
	query := `
    WITH RankedCerts AS (
        SELECT *,
            ROW_NUMBER() OVER (PARTITION BY url ORDER BY checked_at DESC) as rn
        FROM cert_checks
    )
    SELECT url, issued_to, issued_by, valid_from, valid_until, days_remaining, checked_at
    FROM RankedCerts
    WHERE rn = 1
    ORDER BY days_remaining ASC` // Ordering by days_remaining to show most urgent first

	rows, err := db.Query(query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var results []CertInfo
	for rows.Next() {
		var info CertInfo
		var validFromStr, validUntilStr, checkedAtStr string

		err := rows.Scan(
			&info.URL,
			&info.IssuedTo,
			&info.IssuedBy,
			&validFromStr,
			&validUntilStr,
			&info.DaysRemaining,
			&checkedAtStr,
		)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Parse the time strings
		info.ValidFrom, _ = time.Parse(time.RFC3339, validFromStr)
		info.ValidUntil, _ = time.Parse(time.RFC3339, validUntilStr)
		info.CheckedAt, _ = time.Parse(time.RFC3339, checkedAtStr)

		results = append(results, info)
	}

	if results == nil {
		results = []CertInfo{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}

func checkCertsWorker(cfg Config) {
	for {
		for _, url := range cfg.URLs {
			info, err := getCertInfo(url)
			if err != nil {
				log.Printf("Error checking %s: %v", url, err)
				continue
			}

			err = storeCertInfo(info)
			if err != nil {
				log.Printf("Error storing cert info for %s: %v", url, err)
			} else {
				log.Printf("Successfully checked and stored cert info for %s", url)
			}
		}
		time.Sleep(24 * time.Hour)
	}
}

func main() {
	cfg := loadConfig()

	// Start background worker
	go checkCertsWorker(cfg)

	// Setup HTTP handlers
	http.HandleFunc("/metrics", handleMetrics)
	http.HandleFunc("/certs/simple", handleSimpleCerts)

	log.Println("Starting server on :8080...")
	log.Println("Background certificate checker running every 1 minute...")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

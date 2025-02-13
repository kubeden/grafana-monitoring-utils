package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"gopkg.in/gomail.v2"
)

type Config struct {
	GrafanaURL    string   `json:"grafanaUrl"`
	GrafanaAPIKey string   `json:"grafanaApiKey"`
	DashboardUIDs []string `json:"dashboardUids"`
	EmailFrom     string   `json:"emailFrom"`
	EmailTo       []string `json:"emailTo"`
	SMTPHost      string   `json:"smtpHost"`
	SMTPPort      int      `json:"smtpPort"`
	SMTPUser      string   `json:"smtpUser"`
	SMTPPassword  string   `json:"smtpPassword"`
	ScheduleTime  string   `json:"scheduleTime"` // Format: "15:04"
	TimeRange     string   `json:"timeRange"`    // e.g., "12h"
}

func loadConfig(path string) (*Config, error) {
	file, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var config Config
	if err := json.Unmarshal(file, &config); err != nil {
		return nil, err
	}

	return &config, nil
}

func getGrafanaScreenshot(config *Config, dashboardUID string) ([]byte, error) {
	url := fmt.Sprintf("%s/api/dashboards/uid/%s/png", config.GrafanaURL, dashboardUID)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	// Add required headers
	req.Header.Add("Authorization", "Bearer "+config.GrafanaAPIKey)

	// Add time range if specified
	if config.TimeRange != "" {
		q := req.URL.Query()
		q.Add("from", "now-"+config.TimeRange)
		q.Add("to", "now")
		req.URL.RawQuery = q.Encode()
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("grafana API returned status: %d", resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}

func sendEmail(config *Config, screenshots map[string][]byte) error {
	m := gomail.NewMessage()
	m.SetHeader("From", config.EmailFrom)
	m.SetHeader("To", config.EmailTo...)
	m.SetHeader("Subject", fmt.Sprintf("Grafana Dashboards Report - %s", time.Now().Format("2006-01-02")))

	// Add email body
	body := "Please find attached the latest dashboard screenshots."
	m.SetBody("text/plain", body)

	// Attach screenshots
	for uid, data := range screenshots {
		m.Attach(fmt.Sprintf("dashboard-%s.png", uid),
			gomail.SetCopyFunc(func(w io.Writer) error {
				_, err := w.Write(data)
				return err
			}))
	}

	d := gomail.NewDialer(config.SMTPHost, config.SMTPPort, config.SMTPUser, config.SMTPPassword)
	return d.DialAndSend(m)
}

func processScreenshots(config *Config) error {
	screenshots := make(map[string][]byte)

	for _, uid := range config.DashboardUIDs {
		screenshot, err := getGrafanaScreenshot(config, uid)
		if err != nil {
			log.Printf("Error getting screenshot for dashboard %s: %v", uid, err)
			continue
		}
		screenshots[uid] = screenshot
	}

	if len(screenshots) == 0 {
		return fmt.Errorf("no screenshots were captured successfully")
	}

	return sendEmail(config, screenshots)
}

func scheduleNextRun(scheduleTime string) time.Duration {
	now := time.Now()
	scheduledTime, err := time.Parse("15:04", scheduleTime)
	if err != nil {
		log.Fatal("Invalid schedule time format")
	}

	targetTime := time.Date(now.Year(), now.Month(), now.Day(),
		scheduledTime.Hour(), scheduledTime.Minute(), 0, 0, now.Location())

	if now.After(targetTime) {
		targetTime = targetTime.Add(24 * time.Hour)
	}

	return targetTime.Sub(now)
}

func main() {
	configPath := "config.json"
	if len(os.Args) > 1 {
		configPath = os.Args[1]
	}

	config, err := loadConfig(configPath)
	if err != nil {
		log.Fatalf("Error loading config: %v", err)
	}

	for {
		delay := scheduleNextRun(config.ScheduleTime)
		log.Printf("Next run scheduled in %v", delay)
		time.Sleep(delay)

		if err := processScreenshots(config); err != nil {
			log.Printf("Error processing screenshots: %v", err)
		} else {
			log.Printf("Successfully sent dashboard screenshots")
		}
	}
}

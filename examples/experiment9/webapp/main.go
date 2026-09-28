package main

import (
	"flag"
	"fmt"
	"os"
)

// readConfig reads database URL from config file.
// Returns empty string if file doesn't exist or parse fails.
func readConfig(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	// Simple YAML-like parser for database_url field
	for _, line := range splitLines(data) {
		if len(line) > 13 && line[:13] == "database_url:" {
			return trimSpace(line[13:])
		}
	}
	return ""
}

func main() {
	dbURL := ""

	// Mechanism A: environment variable
	if v := os.Getenv("DATABASE_URL"); v != "" {
		dbURL = v
		fmt.Println("Using DATABASE_URL environment variable")
	}

	// Mechanism B: config file
	if cfg := readConfig("config.yaml"); cfg != "" {
		dbURL = cfg
		fmt.Println("Using config.yaml")
	}

	// Mechanism C: command flag (overrides)
	flag.StringVar(&dbURL, "db-url", dbURL, "database connection URL")
	flag.Parse()

	if dbURL == "" {
		fmt.Println("ERROR: no database URL configured")
		os.Exit(1)
	}

	fmt.Printf("Connecting to database: %s\n", dbURL)
	fmt.Println("SUCCESS: database connection configured")
}

func splitLines(data []byte) []string {
	var lines []string
	start := 0
	for i, b := range data {
		if b == '\n' {
			lines = append(lines, string(data[start:i]))
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, string(data[start:]))
	}
	return lines
}

func trimSpace(s string) string {
	start := 0
	for start < len(s) && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	end := len(s)
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}

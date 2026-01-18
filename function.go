package schoolsout

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	firebase "firebase.google.com/go/v4"
	"github.com/GoogleCloudPlatform/functions-framework-go/functions"
)

func init() {
	functions.HTTP("SearchActivities", SearchActivities)
}

// AgeRange represents the age filter for activity search
type AgeRange struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

// DateRange represents the date filter for activity search
type DateRange struct {
	StartDate string `json:"startDate"` // ISO 8601 format: yyyy-MM-dd
	EndDate   string `json:"endDate"`
}

// SearchRequest represents the request model for activity search
type SearchRequest struct {
	Query     string     `json:"query"`
	Location  string     `json:"location,omitempty"`
	AgeRange  *AgeRange  `json:"ageRange,omitempty"`
	DateRange *DateRange `json:"dateRange,omitempty"`
}

// Activity represents a school holiday activity or event
type Activity struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Category    string `json:"category"`
	Location    string `json:"location,omitempty"`
	AgeRange    string `json:"ageRange,omitempty"`
	Date        string `json:"date,omitempty"`
	Price       string `json:"price,omitempty"`
	ImageURL    string `json:"imageUrl,omitempty"`
	BookingURL  string `json:"bookingUrl,omitempty"`
}

// SearchResponse represents the response model for activity search
type SearchResponse struct {
	Success    bool       `json:"success"`
	Activities []Activity `json:"activities,omitempty"`
	Message    string     `json:"message,omitempty"`
	Error      string     `json:"error,omitempty"`
}

// Rate limiting structures
type rateLimitEntry struct {
	count     int
	resetTime time.Time
}

var (
	rateLimitMap = make(map[string]*rateLimitEntry)
	rateLimitMux sync.Mutex

	// Configuration
	maxRequestsPerWindow = 1000      // Max requests per IP
	rateLimitWindow      = time.Hour // Time window
)

// getClientIP extracts the client IP from the request
func getClientIP(r *http.Request) string {
	// Check X-Forwarded-For header (Cloud Functions sets this)
	forwarded := r.Header.Get("X-Forwarded-For")
	if forwarded != "" {
		ips := strings.Split(forwarded, ",")
		return strings.TrimSpace(ips[0])
	}
	// Fallback to RemoteAddr
	ip := r.RemoteAddr
	if idx := strings.LastIndex(ip, ":"); idx != -1 {
		ip = ip[:idx]
	}
	return ip
}

// getAllowedIPs returns the list of IPs that bypass App Check verification
func getAllowedIPs() []string {
	ipsEnv := os.Getenv("ALLOWED_IPS")
	if ipsEnv == "" {
		return []string{} // Empty means no IPs bypass security
	}
	return strings.Split(ipsEnv, ",")
}

// isIPAllowed checks if the IP is in the allowlist (bypasses App Check)
func isIPAllowed(ip string, allowedIPs []string) bool {
	for _, allowed := range allowedIPs {
		if strings.TrimSpace(allowed) == ip {
			return true
		}
	}
	return false
}

// checkRateLimit checks if the IP has exceeded rate limit
func checkRateLimit(ip string) bool {
	rateLimitMux.Lock()
	defer rateLimitMux.Unlock()

	now := time.Now()
	entry, exists := rateLimitMap[ip]

	if !exists || now.After(entry.resetTime) {
		// Create new entry or reset expired one
		rateLimitMap[ip] = &rateLimitEntry{
			count:     1,
			resetTime: now.Add(rateLimitWindow),
		}
		return true
	}

	if entry.count >= maxRequestsPerWindow {
		log.Printf("Rate limit exceeded for IP: %s (%d requests)", ip, entry.count)
		return false
	}

	entry.count++
	return true
}

// cleanupRateLimitMap periodically removes expired entries
func cleanupRateLimitMap() {
	rateLimitMux.Lock()
	defer rateLimitMux.Unlock()

	now := time.Now()
	for ip, entry := range rateLimitMap {
		if now.After(entry.resetTime) {
			delete(rateLimitMap, ip)
		}
	}
}

// verifyAppCheckToken verifies the Firebase App Check token
func verifyAppCheckToken(r *http.Request) error {
	// Get App Check token from header
	appCheckToken := r.Header.Get("X-Firebase-AppCheck")
	if appCheckToken == "" {
		return fmt.Errorf("missing App Check token")
	}

	ctx := context.Background()

	// Initialize Firebase Admin SDK
	app, err := firebase.NewApp(ctx, nil)
	if err != nil {
		log.Printf("Error initializing Firebase app: %v", err)
		return fmt.Errorf("authentication service error")
	}

	// Get App Check client
	client, err := app.AppCheck(ctx)
	if err != nil {
		log.Printf("Error getting AppCheck client: %v", err)
		return fmt.Errorf("authentication service error")
	}

	// Verify the token
	_, err = client.VerifyToken(appCheckToken)
	if err != nil {
		log.Printf("Invalid App Check token: %v", err)
		return fmt.Errorf("invalid App Check token")
	}

	return nil
}

// SearchActivities is the HTTP Cloud Function entry point
func SearchActivities(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Handle CORS preflight
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Firebase-AppCheck")
		w.Header().Set("Access-Control-Max-Age", "3600")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Set CORS headers for actual requests
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Only accept POST requests
	if r.Method != http.MethodPost {
		sendErrorResponse(w, http.StatusMethodNotAllowed, "Method not allowed. Use POST.")
		return
	}

	// Get client IP and check if it's in the allowlist
	clientIP := getClientIP(r)
	allowedIPs := getAllowedIPs()
	ipIsAllowed := isIPAllowed(clientIP, allowedIPs)

	if ipIsAllowed {
		log.Printf("Request from allowlisted IP: %s - bypassing App Check", clientIP)
	} else {
		log.Printf("Request from IP: %s - App Check required", clientIP)
	}

	// Check rate limit (applies to all IPs)
	if !checkRateLimit(clientIP) {
		sendErrorResponse(w, http.StatusTooManyRequests, "Rate limit exceeded. Please try again later.")
		return
	}

	// Verify App Check token (skip for allowlisted IPs or if SKIP_APP_CHECK is set)
	if !ipIsAllowed && os.Getenv("SKIP_APP_CHECK") != "true" {
		if err := verifyAppCheckToken(r); err != nil {
			log.Printf("App Check verification failed for IP %s: %v", clientIP, err)
			sendErrorResponse(w, http.StatusUnauthorized, "Invalid or missing App Check token")
			return
		}
		log.Printf("App Check verification successful for IP: %s", clientIP)
	}

	// Parse request body
	var searchRequest SearchRequest
	if err := json.NewDecoder(r.Body).Decode(&searchRequest); err != nil {
		log.Printf("Invalid JSON: %v", err)
		sendErrorResponse(w, http.StatusBadRequest, "Invalid JSON format")
		return
	}
	defer r.Body.Close()

	// Log the complete request details
	bodyJSON, _ := json.Marshal(searchRequest)
	log.Printf("Incoming request - Method: %s, URL: %s, Query: %s, Body: %s",
		r.Method, r.URL.Path, r.URL.RawQuery, string(bodyJSON))

	// Validate request
	if strings.TrimSpace(searchRequest.Query) == "" {
		sendErrorResponse(w, http.StatusBadRequest, "Query parameter is required and cannot be empty")
		return
	}

	// Process search query
	log.Printf("Processing search query: %s", searchRequest.Query)
	activities := performSearch(&searchRequest)

	// Send success response
	response := SearchResponse{
		Success:    true,
		Activities: activities,
		Message:    fmt.Sprintf("Found %d activities", len(activities)),
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}

// performSearch searches for activities based on the query using Gemini API
func performSearch(req *SearchRequest) []Activity {
	log.Printf("Searching with query: '%s'", req.Query)

	if req.Location != "" {
		log.Printf("Location filter: %s", req.Location)
	}
	if req.AgeRange != nil {
		log.Printf("Age range filter: %d-%d", req.AgeRange.Min, req.AgeRange.Max)
	}
	if req.DateRange != nil {
		log.Printf("Date range filter: %s to %s", req.DateRange.StartDate, req.DateRange.EndDate)
	}

	// Create Gemini client and query for activity suggestions
	geminiClient := NewGeminiClient()
	activities, err := geminiClient.GenerateActivitiesSuggestions(req)

	if err != nil {
		log.Printf("Error querying Gemini API: %v", err)
		// TODO: Add proper exception handling/recovery mechanism to capture and handle errors gracefully
		// Return empty list instead of irrelevant fallback activities
		return []Activity{}
	}

	return activities
}

// sendErrorResponse sends an error response with the given status code and message
func sendErrorResponse(w http.ResponseWriter, statusCode int, errorMessage string) {
	response := SearchResponse{
		Success: false,
		Error:   errorMessage,
	}
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(response)
}

// init starts background cleanup of rate limit map
func init() {
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		for range ticker.C {
			cleanupRateLimitMap()
		}
	}()
}

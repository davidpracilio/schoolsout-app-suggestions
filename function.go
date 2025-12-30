package schoolsout

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

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

// SearchActivities is the HTTP Cloud Function entry point
func SearchActivities(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Only accept POST requests
	if r.Method != http.MethodPost {
		sendErrorResponse(w, http.StatusMethodNotAllowed, "Method not allowed. Use POST.")
		return
	}

	// Parse request body
	var searchRequest SearchRequest
	if err := json.NewDecoder(r.Body).Decode(&searchRequest); err != nil {
		log.Printf("Invalid JSON: %v", err)
		sendErrorResponse(w, http.StatusBadRequest, "Invalid JSON format")
		return
	}
	defer r.Body.Close()

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

// performSearch searches for activities based on the query
//
// TODO: Implement actual search logic:
// - Connect to database (Firestore, Cloud SQL)
// - Call external APIs for event data
// - Use NLP/ML for semantic search
// - Apply filters (location, age range, date range)
// - Rank and sort results
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

	// For now, return mock data
	// You'll replace this with actual search logic later
	return []Activity{
		{
			ID:          "1",
			Title:       "Science Museum Workshop",
			Description: "Hands-on science experiments and interactive exhibits for kids",
			Category:    "Educational",
			Location:    "City Science Museum",
			AgeRange:    "6-12 years",
			Date:        "2025-12-20",
			Price:       "$15",
			ImageURL:    "https://example.com/science-museum.jpg",
			BookingURL:  "https://example.com/book/1",
		},
		{
			ID:          "2",
			Title:       "Kids Cooking Class",
			Description: "Learn to make healthy snacks and treats",
			Category:    "Cooking",
			Location:    "Community Kitchen",
			AgeRange:    "8-14 years",
			Date:        "2025-12-22",
			Price:       "$25",
			ImageURL:    "https://example.com/cooking.jpg",
			BookingURL:  "https://example.com/book/2",
		},
		{
			ID:          "3",
			Title:       "Outdoor Adventure Camp",
			Description: "Rock climbing, hiking, and nature exploration",
			Category:    "Outdoor",
			Location:    "Adventure Park",
			AgeRange:    "10-16 years",
			Date:        "2025-12-27",
			Price:       "$45",
			ImageURL:    "https://example.com/adventure.jpg",
			BookingURL:  "https://example.com/book/3",
		},
	}
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

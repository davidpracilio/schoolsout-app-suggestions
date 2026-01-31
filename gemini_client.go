package schoolsout

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	secretmanagerpb "cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
)

// GeminiRequest represents the request structure for Gemini API
type GeminiRequest struct {
	SystemInstruction *SystemInstruction `json:"system_instruction,omitempty"`
	Contents          []Content          `json:"contents"`
	Tools             []Tool             `json:"tools,omitempty"`
}

// SystemInstruction represents the system instruction for Gemini
type SystemInstruction struct {
	Parts []Part `json:"parts"`
}

// Tool represents a tool configuration (e.g., Google Search)
type Tool struct {
	GoogleSearch *GoogleSearchTool `json:"google_search,omitempty"`
}

// GoogleSearchTool represents the Google Search tool configuration
type GoogleSearchTool struct{}

// Content represents the content structure in Gemini request
type Content struct {
	Parts []Part `json:"parts"`
}

// Part represents a part of the content (text or other media)
type Part struct {
	Text string `json:"text"`
}

// GeminiResponse represents the response from Gemini API
type GeminiResponse struct {
	Candidates []Candidate `json:"candidates"`
}

// Candidate represents a candidate response from Gemini
type Candidate struct {
	Content       CandidateContent `json:"content"`
	FinishReason  string           `json:"finishReason,omitempty"`
	SafetyRatings []SafetyRating   `json:"safetyRatings,omitempty"`
}

// CandidateContent represents the content in a candidate response
type CandidateContent struct {
	Parts []Part `json:"parts"`
}

// SafetyRating represents safety ratings from Gemini
type SafetyRating struct {
	Category    string `json:"category"`
	Probability string `json:"probability"`
}

// GeminiClient handles communication with the Gemini API
type GeminiClient struct {
	APIKey string
	Model  string
}

// getSecretValue retrieves a secret value from Google Cloud Secret Manager
func getSecretValue(ctx context.Context, projectID, secretName string) (string, error) {
	client, err := secretmanager.NewClient(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to create secret manager client: %w", err)
	}
	defer client.Close()

	// Build the resource name: projects/{project}/secrets/{secret}/versions/latest
	name := fmt.Sprintf("projects/%s/secrets/%s/versions/latest", projectID, secretName)

	// Access the secret version
	req := &secretmanagerpb.AccessSecretVersionRequest{
		Name: name,
	}

	result, err := client.AccessSecretVersion(ctx, req)
	if err != nil {
		return "", fmt.Errorf("failed to access secret version: %w", err)
	}

	return string(result.Payload.Data), nil
}

// NewGeminiClient creates a new Gemini API client
func NewGeminiClient() *GeminiClient {
	ctx := context.Background()
	projectID := os.Getenv("GOOGLE_CLOUD_PROJECT") // Cloud Functions Gen2 sets this automatically

	var apiKey string
	var err error

	// Fetch from Secret Manager only
	if projectID != "" {
		log.Printf("Using project ID: %s", projectID)
		apiKey, err = getSecretValue(ctx, projectID, "gemini-api-key")
		if err != nil {
			log.Printf("Error: Failed to fetch API key from Secret Manager: %v", err)
		}
	} else {
		log.Println("Error: No GCP project ID found in environment (GOOGLE_CLOUD_PROJECT or GCP_PROJECT_ID)")
	}

	if apiKey == "" {
		log.Println("Warning: GEMINI_API_KEY not found in Secret Manager")
	}

	return &GeminiClient{
		APIKey: apiKey,
		Model:  "gemini-2.0-flash",
	}
}

// GenerateActivitiesSuggestions queries Gemini API to generate activity suggestions
// This uses a two-stage approach:
// 1. Search mode with Google Search to find activities with valid URLs
// 2. JSON conversion to structure the results properly
func (c *GeminiClient) GenerateActivitiesSuggestions(req *SearchRequest) ([]Activity, error) {
	if c.APIKey == "" {
		return nil, fmt.Errorf("Gemini API key not configured")
	}

	// Stage 1: Search mode with Google Search
	searchResults, err := c.searchWithGoogleSearch(req)
	if err != nil {
		return nil, fmt.Errorf("failed to search for activities: %w", err)
	}

	if searchResults == "" {
		return nil, fmt.Errorf("empty search results from Stage 1")
	}

	log.Printf("Search results from Stage 1: %s", searchResults)

	// Stage 2: Convert search results to structured JSON
	activities, err := c.convertToStructuredJSON(searchResults, req)
	if err != nil {
		return nil, fmt.Errorf("failed to convert to structured JSON: %w", err)
	}

	return activities, nil
}

// searchWithGoogleSearch performs Stage 1: Search mode with Google Search
func (c *GeminiClient) searchWithGoogleSearch(req *SearchRequest) (string, error) {
	// Build the search prompt
	searchPrompt := c.buildSearchPrompt(req)

	log.Printf("Stage 1 Search Prompt: %s", searchPrompt)

	// Create the Gemini API request with Google Search tool
	geminiReq := GeminiRequest{
		SystemInstruction: &SystemInstruction{
			Parts: []Part{
				{
					Text: "You are a technical data extraction agent. Your primary goal is to find specific events and their official source URLs. When using Google Search, you must extract the landing page URL from the search result metadata. Never state that a URL is 'not available' if a relevant search result is present.",
				},
			},
		},
		Contents: []Content{
			{
				Parts: []Part{
					{Text: searchPrompt},
				},
			},
		},
		Tools: []Tool{
			{
				GoogleSearch: &GoogleSearchTool{},
			},
		},
	}

	// Send request to Gemini
	responseText, err := c.sendGeminiRequest(geminiReq)
	if err != nil {
		return "", err
	}

	return responseText, nil
}

// convertToStructuredJSON performs Stage 2: Convert search results to structured JSON
func (c *GeminiClient) convertToStructuredJSON(searchResults string, req *SearchRequest) ([]Activity, error) {
	// Build the conversion prompt
	conversionPrompt := c.buildConversionPrompt(searchResults, req)

	log.Printf("Stage 2 Conversion Prompt: %s", conversionPrompt)

	// Create the Gemini API request without tools
	geminiReq := GeminiRequest{
		SystemInstruction: &SystemInstruction{
			Parts: []Part{
				{
					Text: "You are a data reformatting assistant. Parse the provided Search Results text and convert it exactly into a JSON array. Do not generate new information, perform searches, or modify any details. Preserve all URLs and text verbatim from the provided data.",
				},
			},
		},
		Contents: []Content{
			{
				Parts: []Part{
					{Text: conversionPrompt},
				},
			},
		},
	}

	// Send request to Gemini
	responseText, err := c.sendGeminiRequest(geminiReq)
	if err != nil {
		return nil, err
	}

	log.Printf("Stage 2 JSON conversion response: %s", responseText)

	// Parse the JSON response
	var activities []Activity
	if err := json.Unmarshal([]byte(responseText), &activities); err != nil {
		// If direct parsing fails, try to extract JSON from markdown code blocks
		activities, err = c.extractJSONFromMarkdown(responseText)
		if err != nil {
			return nil, fmt.Errorf("failed to parse activities from response: %w", err)
		}
	}

	log.Printf("Parsed activities: %+v", activities)

	return activities, nil
}

// sendGeminiRequest sends a request to Gemini API and returns the response text
func (c *GeminiClient) sendGeminiRequest(geminiReq GeminiRequest) (string, error) {
	// Marshal request to JSON
	jsonData, err := json.Marshal(geminiReq)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	log.Printf("Gemini request: %s", string(jsonData))

	// Build the API URL
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s",
		c.Model, c.APIKey)

	// Create HTTP request
	httpReq, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	// Send request
	client := &http.Client{}
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	// Check for non-200 status codes
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Gemini API error (status %d): %s", resp.StatusCode, string(body))
	}

	log.Printf("Gemini response body: %s", string(body))

	// Parse response
	var geminiResp GeminiResponse
	if err := json.Unmarshal(body, &geminiResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	// Extract text from all parts, skipping the first if it's just an intro
	var texts []string
	parts := geminiResp.Candidates[0].Content.Parts
	if len(parts) > 1 && strings.Contains(parts[0].Text, "Okay, I will search") {
		// Skip the intro part
		parts = parts[1:]
	}
	for _, part := range parts {
		texts = append(texts, part.Text)
	}
	fullText := strings.Join(texts, "")

	if fullText == "" {
		return "", fmt.Errorf("empty response text from Gemini")
	}
	return fullText, nil
}

// buildSearchPrompt constructs the search prompt for Stage 1 (Google Search mode)
func (c *GeminiClient) buildSearchPrompt(req *SearchRequest) string {
	// Build the main search query
	prompt := fmt.Sprintf("Search for 5-10 %s activities", req.Query)

	if req.AgeRange != nil {
		prompt += fmt.Sprintf(" for kids aged %d-%d", req.AgeRange.Min, req.AgeRange.Max)
	}

	if req.Location != "" {
		prompt += fmt.Sprintf(" in %s", req.Location)
	}

	// Determine the year to use in the search
	var searchYear string
	if req.DateRange != nil {
		searchYear = req.DateRange.StartDate[:4] // Extract year from date range
	} else {
		searchYear = fmt.Sprintf("%d", time.Now().Year()) // Use current year
	}
	prompt += fmt.Sprintf(" for school holidays in %s.\n\n", searchYear)

	// Add critical instructions - simplified and focused
	prompt += `### CRITICAL INSTRUCTIONS FOR URLS:
1. For every activity identified, you MUST provide the direct 'official' URL (e.g., the website of the park, zoo, or organizer).
2. Look specifically at the 'source' link or 'metadata' attached to each search result snippet to find these URLs.
3. DO NOT state that the URL is 'not available' if a search result exists.
4. Format each entry as: 
   - Name: [Activity Name]
   - Description: [1-2 sentences]
   - URL: [Direct Web Link]
   - Category: [Category type if available]
   - Location: [Specific venue/location name if available]
   - Price: [Price if available]`

	return prompt
}

// buildConversionPrompt constructs the conversion prompt for Stage 2 (JSON formatting)
func (c *GeminiClient) buildConversionPrompt(searchResults string, req *SearchRequest) string {
	prompt := fmt.Sprintf(`Convert the following activity search results into a JSON array. DO NOT perform any new searches, generate new activities, or modify any information. Only parse and reformat the exact data provided in the Search Results section below into the specified JSON structure. Preserve all URLs exactly as they appear in the search results.

Search Results:
%s

Please respond with ONLY a JSON array of activities in the following format (no additional text, no markdown):
[
  {
    "id": "unique-id",
    "title": "Activity Title",
    "description": "Brief description of the activity",
    "category": "Category (e.g., Educational, Sports, Arts, Outdoor)",
    "location": "Location name",
    "ageRange": "Age range (e.g., 6-12 years)",
    "date": "Date in yyyy-MM-dd format or empty string if not available",
    "price": "Price (e.g., Free, $20, $10-$30) or empty string if not available",
    "imageUrl": "https://example.com/image.jpg or empty string if not available",
    "bookingUrl": "[Extracted URL from search results] - MUST be the exact URL from the Search Results above"
  }
]

CRITICAL REQUIREMENTS:
- Generate a unique ID for each activity (e.g., "activity-1", "activity-2")
- Use the EXACT URLs from the search results for bookingUrl - copy them verbatim without changes
- Category: Extract from the search results only (Educational, Sports, Arts, Outdoor, Entertainment, Technology, Science, etc.)
- Location: Extract the specific venue/location name from the search results only
- Price: Extract price information from the search results only (e.g., "Free", "$25", "$15-$30", "From $20")
- If date is not available in search results, use an empty string ""
- If price is not mentioned in search results, use an empty string ""
- If imageUrl is not available, use an empty string ""
- Ensure all JSON is valid and properly formatted
- DO NOT add, remove, or invent any information not present in the Search Results`, searchResults)

	return prompt
}

// extractJSONFromMarkdown attempts to extract JSON from markdown code blocks
func (c *GeminiClient) extractJSONFromMarkdown(text string) ([]Activity, error) {
	// Look for JSON between ```json and ``` or ``` and ```
	start := -1
	end := -1

	// Try ```json marker
	jsonMarker := "```json"
	if idx := bytes.Index([]byte(text), []byte(jsonMarker)); idx != -1 {
		start = idx + len(jsonMarker)
		// Find the closing ```
		if endIdx := bytes.Index([]byte(text[start:]), []byte("```")); endIdx != -1 {
			end = start + endIdx
		}
	}

	// Try plain ``` marker if json marker not found
	if start == -1 {
		marker := "```"
		if idx := bytes.Index([]byte(text), []byte(marker)); idx != -1 {
			start = idx + len(marker)
			// Find the closing ```
			if endIdx := bytes.Index([]byte(text[start:]), []byte(marker)); endIdx != -1 {
				end = start + endIdx
			}
		}
	}

	// If we found markers, extract and parse
	if start != -1 && end != -1 {
		jsonText := text[start:end]
		var activities []Activity
		if err := json.Unmarshal([]byte(jsonText), &activities); err != nil {
			return nil, err
		}
		return activities, nil
	}

	// If no markdown, try to parse the whole text
	var activities []Activity
	if err := json.Unmarshal([]byte(text), &activities); err != nil {
		return nil, err
	}
	return activities, nil
}

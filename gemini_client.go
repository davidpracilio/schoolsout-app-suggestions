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

	// Create the Gemini API request without tools
	geminiReq := GeminiRequest{
		SystemInstruction: &SystemInstruction{
			Parts: []Part{
				{
					Text: "You are a JSON formatting assistant. Convert the provided activity information into a valid JSON array following the exact structure specified. Ensure all URLs are preserved exactly as provided.",
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

	log.Printf("JSON conversion response: %s", responseText)

	// Parse the JSON response
	var activities []Activity
	if err := json.Unmarshal([]byte(responseText), &activities); err != nil {
		// If direct parsing fails, try to extract JSON from markdown code blocks
		activities, err = c.extractJSONFromMarkdown(responseText)
		if err != nil {
			return nil, fmt.Errorf("failed to parse activities from response: %w", err)
		}
	}

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

	// Parse response
	var geminiResp GeminiResponse
	if err := json.Unmarshal(body, &geminiResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	// Extract text from response
	if len(geminiResp.Candidates) == 0 {
		return "", fmt.Errorf("no candidates in response")
	}

	if len(geminiResp.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("no parts in candidate content")
	}

	return geminiResp.Candidates[0].Content.Parts[0].Text, nil
}

// buildSearchPrompt constructs the search prompt for Stage 1 (Google Search mode)
func (c *GeminiClient) buildSearchPrompt(req *SearchRequest) string {
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
	prompt += fmt.Sprintf(" for school holidays in %s", searchYear)

	prompt += `

### CRITICAL INSTRUCTIONS:
1. For every activity identified, you MUST provide the direct 'official' URL (e.g., the website of the park, zoo, or organizer).
2. Look specifically at the 'source' link or 'metadata' attached to each search result snippet to find these URLs.
3. DO NOT state that the URL is 'not available' if a search result exists.
4. Extract as much information as possible from the search results including:
   - Category (Educational, Sports, Arts, Outdoor, Entertainment, etc.)
   - Specific location/venue name and address
   - Price information (look for cost, pricing, admission fees in the search results)
   - Date information if available
5. Format each entry as: 
   - Name: [Activity Name]
   - Description: [1-2 sentences about the activity]
   - URL: [Direct Web Link from search result]
   - Category: [Category type - REQUIRED, infer from activity type if not explicitly stated]
   - Location: [Specific venue/location name - REQUIRED]
   - Age Range: [Age range suitable for the activity]
   - Date: [Specific date or date range if available]
   - Price: [Price information - look for this in search snippets, e.g., "Free", "$25", "$15-$30", "From $20"]

### ADDITIONAL REQUIREMENTS:
- Prioritise venues that allow drop and leave activities (but don't mention this in descriptions)
- Only provide suggestions where the activity is current or upcoming, and published or updated from the past 12 months or less than one year
- For Category: Analyze the activity type and assign appropriate category (Educational, Sports, Arts, Outdoor, Entertainment, Technology, Science, etc.)
- For Location: Include the specific venue name, not just the city
- For Price: Search the snippets carefully for pricing information - it's often mentioned in event descriptions`

	return prompt
}

// buildConversionPrompt constructs the conversion prompt for Stage 2 (JSON formatting)
func (c *GeminiClient) buildConversionPrompt(searchResults string, req *SearchRequest) string {
	prompt := fmt.Sprintf(`Convert the following activity search results into a JSON array. Preserve all URLs and information exactly as provided.

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
    "bookingUrl": "https://example.com/book - MUST be the official URL from search results"
  }
]

CRITICAL REQUIREMENTS:
- Generate a unique ID for each activity (e.g., "activity-1", "activity-2")
- Use the exact URLs from the search results for bookingUrl - DO NOT modify or omit them
- Category: MUST be populated from the search results (Educational, Sports, Arts, Outdoor, Entertainment, Technology, Science, etc.)
- Location: MUST include the specific venue/location name from the search results
- Price: MUST be populated if mentioned in search results (e.g., "Free", "$25", "$15-$30", "From $20")
- If date is not available in search results, use an empty string
- If price is not mentioned in search results, use an empty string
- If imageUrl is not available, use an empty string
- Ensure all JSON is valid and properly formatted
- DO NOT make up or infer information that wasn't in the search results`, searchResults)

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

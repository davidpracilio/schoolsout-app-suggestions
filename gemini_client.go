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

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	secretmanagerpb "cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
)

// GeminiRequest represents the request structure for Gemini API
type GeminiRequest struct {
	Contents []Content `json:"contents"`
}

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
func (c *GeminiClient) GenerateActivitiesSuggestions(req *SearchRequest) ([]Activity, error) {
	if c.APIKey == "" {
		return nil, fmt.Errorf("Gemini API key not configured")
	}

	// Build the prompt for Gemini
	prompt := c.buildPrompt(req)

	// Create the Gemini API request
	geminiReq := GeminiRequest{
		Contents: []Content{
			{
				Parts: []Part{
					{Text: prompt},
				},
			},
		},
	}

	// Marshal request to JSON
	jsonData, err := json.Marshal(geminiReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Build the API URL
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s",
		c.Model, c.APIKey)

	// Create HTTP request
	httpReq, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	// Send request
	client := &http.Client{}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Check for non-200 status codes
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Gemini API error (status %d): %s", resp.StatusCode, string(body))
	}

	// Parse response
	var geminiResp GeminiResponse
	if err := json.Unmarshal(body, &geminiResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	// Extract and parse activities from response
	activities, err := c.parseActivitiesFromResponse(&geminiResp)
	if err != nil {
		return nil, fmt.Errorf("failed to parse activities: %w", err)
	}

	return activities, nil
}

// buildPrompt constructs the prompt for Gemini based on search parameters
func (c *GeminiClient) buildPrompt(req *SearchRequest) string {
	prompt := fmt.Sprintf(`You are a helpful assistant that suggests school holiday activities for children. 
Based on the following search criteria, suggest 5-10 relevant activities in JSON format.

Search Query: %s`, req.Query)

	if req.Location != "" {
		prompt += fmt.Sprintf("\nLocation: %s", req.Location)
	}

	if req.AgeRange != nil {
		prompt += fmt.Sprintf("\nAge Range: %d-%d years", req.AgeRange.Min, req.AgeRange.Max)
	}

	if req.DateRange != nil {
		prompt += fmt.Sprintf("\nDate Range: %s to %s", req.DateRange.StartDate, req.DateRange.EndDate)
	}

	prompt += `

Include venues that allow drop and leave activities, but don't provide that in the description.

Provide a link to the venue or organiser's main website if there is one, as the booking url.

Please respond with ONLY a JSON array of activities in the following format (no additional text):
[
  {
    "id": "unique-id",
    "title": "Activity Title",
    "description": "Brief description of the activity",
    "category": "Category (e.g., Educational, Sports, Arts, Outdoor)",
    "location": "Location name",
    "ageRange": "Age range (e.g., 6-12 years)",
    "date": "Date in yyyy-MM-dd format",
    "price": "Price (e.g., Free, $20, $10-$30)",
    "imageUrl": "https://example.com/image.jpg",
    "bookingUrl": "https://example.com/book"
  }
]`

	return prompt
}

// parseActivitiesFromResponse extracts activities from Gemini's response
func (c *GeminiClient) parseActivitiesFromResponse(resp *GeminiResponse) ([]Activity, error) {
	if len(resp.Candidates) == 0 {
		return nil, fmt.Errorf("no candidates in response")
	}

	// Get the text from the first candidate
	if len(resp.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("no parts in candidate content")
	}

	responseText := resp.Candidates[0].Content.Parts[0].Text
	log.Printf("Gemini response text: %s", responseText)

	// Try to parse as JSON array
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

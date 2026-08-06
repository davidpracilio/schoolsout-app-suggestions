package schoolsout

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	neturl "net/url"
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
}

// SystemInstruction represents the system instruction for Gemini
type SystemInstruction struct {
	Parts []Part `json:"parts"`
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

// ValidTags contains all valid tag IDs organized by category
type ValidTags struct {
	AgeGroups   []string
	Facilities  []string
	Environment []string
}

// GetValidTags returns the complete set of valid tag IDs
func GetValidTags() ValidTags {
	return ValidTags{
		AgeGroups: []string{
			"baby_infant",
			"toddler",
			"preschooler",
			"school_kids",
			"teens_big_kids",
			"all_ages",
		},
		Facilities: []string{
			"toilets",
			"baby_change",
			"cafe_coffee",
			"picnic_area",
			"easy_parking",
			"accessible",
			"free_wifi",
			"fenced_in",
			"grip_socks",
			"drop_and_leave",
		},
		Environment: []string{
			"outdoor",
			"indoor",
			"air_con",
			"shaded",
			"quiet_zone",
			"high_energy",
			"water_activity",
			"sports",
		},
	}
}

// IsValidTag checks if a tag ID is in the valid tags list
func IsValidTag(tag string) bool {
	validTags := GetValidTags()
	for _, t := range validTags.AgeGroups {
		if t == tag {
			return true
		}
	}
	for _, t := range validTags.Facilities {
		if t == tag {
			return true
		}
	}
	for _, t := range validTags.Environment {
		if t == tag {
			return true
		}
	}
	return false
}

// ValidateAndFilterTags filters tags to only include valid ones
func ValidateAndFilterTags(tags []string) []string {
	var validatedTags []string
	seen := make(map[string]bool)
	for _, tag := range tags {
		if IsValidTag(tag) && !seen[tag] {
			validatedTags = append(validatedTags, tag)
			seen[tag] = true
		}
	}
	return validatedTags
}

// getSecretValue retrieves a secret value from Google Cloud Secret Manager
func getSecretValue(ctx context.Context, projectID, secretName string) (string, error) {
	client, err := secretmanager.NewClient(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to create secret manager client: %w", err)
	}
	defer client.Close()

	name := fmt.Sprintf("projects/%s/secrets/%s/versions/latest", projectID, secretName)

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
		Model:  "gemini-3.6-flash",
	}
}

// GenerateActivitiesSuggestions queries Gemini API to generate activity suggestions
// using a two-stage approach:
// 1. Search stage: prompt Gemini to find activities with URLs
// 2. JSON conversion stage: reformat the results into structured JSON
//
// TODO - Check if the search terms entered are relevant or in line with an activity search?
func (c *GeminiClient) GenerateActivitiesSuggestions(req *SearchRequest) ([]Activity, error) {
	if c.APIKey == "" {
		return nil, fmt.Errorf("Gemini API key not configured")
	}

	searchResults, err := c.searchActivities(req)
	if err != nil {
		return nil, fmt.Errorf("failed to search for activities: %w", err)
	}

	if searchResults == "" {
		return nil, fmt.Errorf("empty search results from Stage 1")
	}

	log.Printf("Search results from Stage 1: %s", searchResults)

	activities, err := c.convertToStructuredJSON(searchResults, req)
	if err != nil {
		return nil, fmt.Errorf("failed to convert to structured JSON: %w", err)
	}

	return activities, nil
}

// searchActivities performs Stage 1: prompts Gemini to find activities with URLs
func (c *GeminiClient) searchActivities(req *SearchRequest) (string, error) {
	searchPrompt := c.buildSearchPrompt(req)

	log.Printf("Stage 1 Search Prompt: %s", searchPrompt)

	geminiReq := GeminiRequest{
		SystemInstruction: &SystemInstruction{
			Parts: []Part{
				{
					Text: "You are a helpful assistant that finds school holiday programs for families. Identify specific holiday programs, camps, and vacation care offerings with accurate details including names, descriptions, locations, prices, and relevant facilities. Prioritize structured, bookable programs over one-off attractions or general venues.",
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
	}

	responseText, err := c.sendGeminiRequest(geminiReq)
	if err != nil {
		return "", err
	}

	return responseText, nil
}

// convertToStructuredJSON performs Stage 2: converts search results to structured JSON
func (c *GeminiClient) convertToStructuredJSON(searchResults string, req *SearchRequest) ([]Activity, error) {
	conversionPrompt := c.buildConversionPrompt(searchResults, req)

	log.Printf("Stage 2 Conversion Prompt: %s", conversionPrompt)

	geminiReq := GeminiRequest{
		SystemInstruction: &SystemInstruction{
			Parts: []Part{
				{
					Text: "You are a data reformatting assistant for school holiday programs. Parse the provided Search Results text and convert it exactly into a JSON array. Do not generate new information, perform searches, or modify any details. Preserve all URLs and text verbatim from the provided data.",
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

	responseText, err := c.sendGeminiRequest(geminiReq)
	if err != nil {
		return nil, err
	}

	log.Printf("Stage 2 JSON conversion response: %s", responseText)

	var activities []Activity
	if err := json.Unmarshal([]byte(responseText), &activities); err != nil {
		activities, err = c.extractJSONFromMarkdown(responseText)
		if err != nil {
			return nil, fmt.Errorf("failed to parse activities from response: %w", err)
		}
	}

	log.Printf("Parsed activities: %+v", activities)

	activities = c.validateActivityTags(activities)
	activities = generateGoogleSearchURLs(activities, req)

	return activities, nil
}

// sendGeminiRequest sends a request to Gemini API and returns the response text
func (c *GeminiClient) sendGeminiRequest(geminiReq GeminiRequest) (string, error) {
	jsonData, err := json.Marshal(geminiReq)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	log.Printf("Gemini request: %s", string(jsonData))

	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s",
		c.Model, c.APIKey)

	httpReq, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Gemini API error (status %d): %s", resp.StatusCode, string(body))
	}

	log.Printf("Gemini response body: %s", string(body))

	var geminiResp GeminiResponse
	if err := json.Unmarshal(body, &geminiResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	if len(geminiResp.Candidates) == 0 {
		log.Printf("Warning: No candidates returned from Gemini API")
		return "", fmt.Errorf("no candidates in Gemini response")
	}

	candidate := geminiResp.Candidates[0]

	if candidate.FinishReason != "" {
		log.Printf("Gemini finish reason: %s", candidate.FinishReason)
	}

	var texts []string
	parts := candidate.Content.Parts
	if len(parts) > 1 && strings.Contains(parts[0].Text, "Okay, I will search") {
		parts = parts[1:]
	}
	for _, part := range parts {
		texts = append(texts, part.Text)
	}
	fullText := strings.Join(texts, "")

	if fullText == "" {
		log.Printf("Warning: Empty response text from Gemini. Finish reason: %s, Number of parts: %d", candidate.FinishReason, len(candidate.Content.Parts))
		return "", fmt.Errorf("empty response text from Gemini (finish reason: %s)", candidate.FinishReason)
	}

	return fullText, nil
}

// buildSearchPrompt constructs the search prompt for Stage 1
func (c *GeminiClient) buildSearchPrompt(req *SearchRequest) string {
	prompt := fmt.Sprintf("Search for 5-10 %s school holiday programs", req.Query)

	if req.AgeRange != nil {
		prompt += fmt.Sprintf(" for kids aged %d-%d", req.AgeRange.Min, req.AgeRange.Max)
	}

	if req.Location != "" {
		prompt += fmt.Sprintf(" in %s", req.Location)
	}

	var searchYear string
	if req.DateRange != nil {
		searchYear = req.DateRange.StartDate[:4]
	} else {
		searchYear = fmt.Sprintf("%d", time.Now().Year())
	}
	prompt += fmt.Sprintf(" running during the %s school holidays and list the prices.\n\n", searchYear)

	prompt += `### INSTRUCTIONS:
1. Focus specifically on structured, bookable school holiday programs (e.g. holiday camps, vacation care, holiday clinics, day programs, workshop series). EXCLUDE one-off attractions, general venues, or things that are open year-round with no holiday-specific program.
2. PRIORITIZE programs that offer "drop and leave" or "drop-off" facilities, as these are highly valued by parents during school holidays.
3. Format each entry as:
   - Name: [Program Name]
   - Description: [1-2 sentences]
   - Category: [Category type if available]
   - Location: [Specific venue/location name if available]
   - Price: [Price if available]
   - Drop and Leave: [Yes/No - if mentioned in search results]`

	return prompt
}

// buildConversionPrompt constructs the conversion prompt for Stage 2 (JSON formatting)
func (c *GeminiClient) buildConversionPrompt(searchResults string, req *SearchRequest) string {
	validTags := GetValidTags()
	tagsDescription := fmt.Sprintf(`
AVAILABLE TAGS (select only from these exact IDs):
Age Groups: %v
Facilities: %v
Environment: %v`, validTags.AgeGroups, validTags.Facilities, validTags.Environment)

	prompt := fmt.Sprintf(`Convert the following school holiday program search results into a JSON array. DO NOT perform any new searches, generate new programs, or modify any information. Only parse and reformat the exact data provided in the Search Results section below into the specified JSON structure.

Search Results:
%s

Please respond with ONLY a JSON array of school holiday programs in the following format (no additional text, no markdown):
[
  {
    "id": "unique-id",
    "title": "Program Title",
    "description": "Brief description of the program",
    "category": "Category (e.g., Holiday Camp, Vacation Care, Educational, Sports, Arts, Outdoor)",
    "location": "Location name",
    "ageRange": "Age range (e.g., 6-12 years)",
    "date": "Date in yyyy-MM-dd format or empty string if not available",
    "price": "Price (e.g., Free, $20, $10-$30) or empty string if not available",
    "imageUrl": "",
    "bookingUrl": "",
    "tags": ["tag-id-1", "tag-id-2"]
  }
]

TAG SELECTION REQUIREMENTS:
- Analyze the program description and details to suggest relevant tags
- Only use tag IDs from the available tags list below
- Select multiple tags if they apply (e.g., an outdoor program for school kids might have: ["outdoor", "school_kids"])
- PRIORITY: If a program has the "drop_and_leave" tag, ALWAYS include it as it is a highly valued facility feature
- If no tags apply, use an empty array []
- Do NOT invent or use tag IDs not in the available list%s

OTHER REQUIREMENTS:
- Generate a unique ID for each program (e.g., "program-1", "program-2")
- Category: Extract from the search results only (Holiday Camp, Vacation Care, Educational, Sports, Arts, Outdoor, Entertainment, Technology, Science, etc.)
- Location: Extract the specific venue/location name from the search results only
- Price: Extract price information from the search results only (e.g., "Free", "$25", "$15-$30", "From $20")
- If date is not available in search results, use an empty string ""
- If price is not mentioned in search results, use an empty string ""
- If imageUrl is not available, use an empty string ""
- Ensure all JSON is valid and properly formatted
- DO NOT add, remove, or invent any information not present in the Search Results`, searchResults, tagsDescription)

	return prompt
}

// extractJSONFromMarkdown attempts to extract JSON from markdown code blocks
func (c *GeminiClient) extractJSONFromMarkdown(text string) ([]Activity, error) {
	start := -1
	end := -1

	jsonMarker := "```json"
	if idx := bytes.Index([]byte(text), []byte(jsonMarker)); idx != -1 {
		start = idx + len(jsonMarker)
		if endIdx := bytes.Index([]byte(text[start:]), []byte("```")); endIdx != -1 {
			end = start + endIdx
		}
	}

	if start == -1 {
		marker := "```"
		if idx := bytes.Index([]byte(text), []byte(marker)); idx != -1 {
			start = idx + len(marker)
			if endIdx := bytes.Index([]byte(text[start:]), []byte(marker)); endIdx != -1 {
				end = start + endIdx
			}
		}
	}

	if start != -1 && end != -1 {
		jsonText := text[start:end]
		var activities []Activity
		if err := json.Unmarshal([]byte(jsonText), &activities); err != nil {
			return nil, err
		}
		return activities, nil
	}

	var activities []Activity
	if err := json.Unmarshal([]byte(text), &activities); err != nil {
		return nil, err
	}
	return activities, nil
}

// generateGoogleSearchURLs sets each activity's BookingURL to a Google search for the activity title and location.
func generateGoogleSearchURLs(activities []Activity, req *SearchRequest) []Activity {
	year := fmt.Sprintf("%d", time.Now().Year())
	if req.DateRange != nil && len(req.DateRange.StartDate) >= 4 {
		year = req.DateRange.StartDate[:4]
	}
	for i := range activities {
		query := activities[i].Title + " school holidays " + year
		if req.Location != "" {
			query += " " + req.Location
		}
		activities[i].BookingURL = "https://www.google.com/search?q=" + neturl.QueryEscape(query)
	}
	return activities
}

// validateActivityTags validates and filters tags for all activities
func (c *GeminiClient) validateActivityTags(activities []Activity) []Activity {
	for i := range activities {
		if len(activities[i].Tags) > 0 {
			activities[i].Tags = ValidateAndFilterTags(activities[i].Tags)
			log.Printf("Activity '%s' tags after validation: %v", activities[i].Title, activities[i].Tags)
		}
	}
	return activities
}

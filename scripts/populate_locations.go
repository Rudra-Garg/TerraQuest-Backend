package main

import (
	"encoding/json" // To parse JSON response from Google API
	"fmt"
	"io" // To read response body
	"log"
	"net/http" // To make HTTP requests
	"net/url"  // To build URLs safely
	"os"
	"time" // For adding delays

	"github.com/joho/godotenv" // For loading .env file
	"gorm.io/gorm"             // GORM for database interaction

	// Adjust import paths based on your go.mod file name
	"geoguessr-backend/internal/database"
	"geoguessr-backend/internal/models"
)

// --- Define Candidate Locations (Start with our mock list, expand later) ---
// This list will be checked against the Metadata API
var candidateLocations = []models.Location{
	// Americas
	{Latitude: 40.75798, Longitude: -73.9855, Description: "Times Square, NYC, USA"},
	{Latitude: 34.0522, Longitude: -118.2437, Description: "Los Angeles, CA, USA"},
	{Latitude: -22.9068, Longitude: -43.1729, Description: "Rio de Janeiro, Brazil"},
	{Latitude: 45.5017, Longitude: -73.5673, Description: "Montreal, QC, Canada"},
	{Latitude: -34.6037, Longitude: -58.3816, Description: "Buenos Aires, Argentina"},
	{Latitude: 19.4326, Longitude: -99.1332, Description: "Mexico City, Mexico"},
	{Latitude: 37.7749, Longitude: -122.4194, Description: "San Francisco, CA, USA"},
	{Latitude: 49.2827, Longitude: -123.1207, Description: "Vancouver, BC, Canada"},
	{Latitude: 64.1466, Longitude: -21.9426, Description: "Reykjavík, Iceland"},

	// Europe
	{Latitude: 48.8584, Longitude: 2.2945, Description: "Eiffel Tower, Paris, France"},
	{Latitude: 51.5007, Longitude: -0.1246, Description: "Near Big Ben, London, UK"},
	{Latitude: 41.9028, Longitude: 12.4964, Description: "Rome, Italy"},
	{Latitude: 52.5200, Longitude: 13.4050, Description: "Berlin, Germany"},
	{Latitude: 59.9139, Longitude: 10.7522, Description: "Oslo, Norway"},
	{Latitude: 38.7223, Longitude: -9.1393, Description: "Lisbon, Portugal"},
	{Latitude: 50.0755, Longitude: 14.4378, Description: "Prague, Czech Republic"},
	{Latitude: 52.3676, Longitude: 4.9041, Description: "Amsterdam, Netherlands"},
	{Latitude: 40.4168, Longitude: -3.7038, Description: "Madrid, Spain"},

	// Asia
	{Latitude: 35.6586, Longitude: 139.7454, Description: "Near Tokyo Tower, Japan"},
	{Latitude: 22.3193, Longitude: 114.1694, Description: "Hong Kong"},
	{Latitude: 1.3521, Longitude: 103.8198, Description: "Singapore"},
	{Latitude: 37.5665, Longitude: 126.9780, Description: "Seoul, South Korea"},
	{Latitude: 13.7563, Longitude: 100.5018, Description: "Bangkok, Thailand"},
	{Latitude: 25.0330, Longitude: 121.5654, Description: "Taipei, Taiwan"},
	{Latitude: 28.6139, Longitude: 77.2090, Description: "New Delhi, India"},
	{Latitude: 31.2304, Longitude: 121.4737, Description: "Shanghai, China"},

	// Africa
	{Latitude: -33.9249, Longitude: 18.4241, Description: "Cape Town, South Africa"},
	{Latitude: -26.2041, Longitude: 28.0473, Description: "Johannesburg, South Africa"},
	{Latitude: 30.0444, Longitude: 31.2357, Description: "Cairo, Egypt"},
	{Latitude: -1.2921, Longitude: 36.8219, Description: "Nairobi, Kenya"},
	{Latitude: 6.5244, Longitude: 3.3792, Description: "Lagos, Nigeria"},

	// Oceania
	{Latitude: -33.8568, Longitude: 151.2153, Description: "Sydney Opera House, Australia"},
	{Latitude: -37.8136, Longitude: 144.9631, Description: "Melbourne, VIC, Australia"},
	{Latitude: -41.2865, Longitude: 174.7762, Description: "Wellington, New Zealand"},
	{Latitude: -36.8485, Longitude: 174.7633, Description: "Auckland, New Zealand"},

	// Add MANY more candidate coordinates here from lists you find or generate
}

// --- Structs to match Google API JSON response ---
type StreetViewMetadataResponse struct {
	Copyright string        `json:"copyright"`
	Date      string        `json:"date"`
	Location  *LatLngLiteral `json:"location"` // Pointer to handle potential null
	PanoID    string        `json:"pano_id"`
	Status    string        `json:"status"` // "OK", "ZERO_RESULTS", etc.
}

type LatLngLiteral struct {
	Latitude  float64 `json:"lat"`
	Longitude float64 `json:"lng"`
}

// --- Main Script Logic ---
func main() {
	log.Println("Starting location population script...")

	// 1. Load Environment Variables from .env file
	err := godotenv.Load() // Load from .env in current directory
	if err != nil {
		log.Println("Warning: Could not load .env file. Using environment variables directly.", err)
	}

	apiKey := os.Getenv("GOOGLE_MAPS_API_KEY")
	if apiKey == "" {
		log.Fatal("Error: GOOGLE_MAPS_API_KEY environment variable not set.")
	}

	// 2. Connect to the Database (uses connection logic from internal/database)
	if err := database.Connect(); err != nil {
		log.Fatalf("Error connecting to database: %v", err)
	}
	db := database.GetDB()

	// 3. Iterate through candidates and validate
	validatedCount := 0
	skippedCount := 0
	errorCount := 0
	apiDelay := 50 * time.Millisecond // Delay between API calls to avoid hitting limits

	for i, candidate := range candidateLocations {
		log.Printf("Processing candidate %d/%d: %s (%f, %f)",
			i+1, len(candidateLocations), candidate.Description, candidate.Latitude, candidate.Longitude)

		// Check if a location with very similar coords already exists (simple check)
		var existing models.Location
		// Use a small tolerance for floating point comparisons
		tolerance := 0.0001
		result := db.Where("latitude BETWEEN ? AND ?", candidate.Latitude-tolerance, candidate.Latitude+tolerance).
			Where("longitude BETWEEN ? AND ?", candidate.Longitude-tolerance, candidate.Longitude+tolerance).
			First(&existing)

		if result.Error == nil {
			// Found a close match already in DB
			log.Printf("--> Skipping: Location already exists (ID: %d)", existing.ID)
			skippedCount++
			time.Sleep(5 * time.Millisecond) // Shorter delay if skipping
			continue
		} else if result.Error != gorm.ErrRecordNotFound {
			// Genuine DB error during check
			log.Printf("--> DB Error checking existence: %v", result.Error)
			errorCount++
			time.Sleep(apiDelay) // Still pause on error
			continue
		}
		// Record not found, proceed with API validation

		// --- Call Metadata API ---
		metadata, err := getStreetViewMetadata(apiKey, candidate.Latitude, candidate.Longitude)
		if err != nil {
			log.Printf("--> API Error for %s: %v", candidate.Description, err)
			errorCount++
			time.Sleep(apiDelay) // Wait after an error too
			continue
		}

		// --- Check Status ---
		if metadata.Status == "OK" && metadata.Location != nil {
			log.Printf("--> Status OK. Validated Coords: (%f, %f)", metadata.Location.Latitude, metadata.Location.Longitude)

			// --- Insert into Database ---
			newLocation := models.Location{
				// Use coordinates returned by Google API for better accuracy
				Latitude:    metadata.Location.Latitude,
				Longitude:   metadata.Location.Longitude,
				Description: candidate.Description, // Keep original description
				// We could potentially add Country/Region here if we did reverse geocoding (later feature)
			}
			if err := db.Create(&newLocation).Error; err != nil {
				log.Printf("--> DB Error inserting location: %v", err)
				errorCount++
			} else {
				log.Printf("--> Successfully inserted location ID: %d", newLocation.ID)
				validatedCount++
			}
		} else {
			log.Printf("--> Status %s. Skipping.", metadata.Status)
			skippedCount++
		}

		// --- Delay to respect potential rate limits ---
		time.Sleep(apiDelay)
	}

	log.Println("------------------------------------")
	log.Printf("Population script finished.")
	log.Printf("Validated and Inserted: %d", validatedCount)
	log.Printf("Skipped (No Coverage or Existed): %d", skippedCount)
	log.Printf("Errors: %d", errorCount)
	log.Println("------------------------------------")
}

// --- Helper function to call Google API ---
func getStreetViewMetadata(apiKey string, lat, lng float64) (*StreetViewMetadataResponse, error) {
	baseURL := "https://maps.googleapis.com/maps/api/streetview/metadata"

	// Build URL with query parameters
	params := url.Values{}
	params.Add("location", fmt.Sprintf("%f,%f", lat, lng))
	params.Add("key", apiKey)
	params.Add("source", "outdoor") // IMPORTANT: Filter for official coverage

	fullURL := baseURL + "?" + params.Encode()

	// Make HTTP GET request
	resp, err := http.Get(fullURL)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// Check for non-200 status codes (e.g., 4xx, 5xx from Google)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("google API returned non-OK status: %s - Body: %s", resp.Status, string(body))
	}

	// Parse JSON response
	var metadata StreetViewMetadataResponse
	if err := json.Unmarshal(body, &metadata); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON response: %w", err)
	}

	return &metadata, nil
}
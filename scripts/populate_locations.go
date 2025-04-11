// scripts/populate_locations.go
package main

import (
	"encoding/json"
	// "encoding/xml" // Keep commented unless using OSM
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand" // For shuffling
	"net/http"
	"net/url"
	"os"
	"runtime" // For NumCPU
	"sync"
	"sync/atomic"
	"time"

	"github.com/joho/godotenv"
	"gorm.io/gorm"

	// Use internal packages
	"geoguessr-backend/internal/database"
	"geoguessr-backend/internal/models"
)

// --- Struct Definitions ---

// Region defines a geographical area (from regions.json)
type Region struct {
	Name        string  `json:"name"`
	MinLat      float64 `json:"minLat"`
	MaxLat      float64 `json:"maxLat"`
	MinLng      float64 `json:"minLng"`
	MaxLng      float64 `json:"maxLng"`
	Description string  `json:"description"`
	GridDensity float64 `json:"gridDensity"`
	HasOSMData  bool    `json:"hasOSMData"` // Flag for potential future OSM integration
}

// CandidateLocation represents a potential location to verify (from files or generation)
type CandidateLocation struct {
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	Description string  `json:"description"`
	Source      string  `json:"source"` // e.g., "manual", "grid", "osm"
}

// StreetViewMetadataResponse represents the relevant parts of the Google API response
type StreetViewMetadataResponse struct {
	Status    string         `json:"status"`    // "OK", "ZERO_RESULTS", etc.
	Location  *LatLngLiteral `json:"location"`  // Pointer to handle potential null on non-OK status
	Copyright string         `json:"copyright"` // Can add if needed
	Date      string         `json:"date"`      // Can add if needed
	PanoID    string         `json:"pano_id"`   // Can add if needed
}

// LatLngLiteral represents a latitude-longitude pair from Google API
type LatLngLiteral struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

// --- Helper Functions ---

// loadJSONFile reads and unmarshals a JSON file into the target interface
func loadJSONFile(filePath string, target interface{}) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		// Treat missing manual file as non-fatal, just return empty
		if os.IsNotExist(err) && filePath == "./scripts/manual_locations.json" {
			log.Printf("Info: %s not found, skipping manual locations.", filePath)
			return nil // Return nil to indicate okay, just no data
		}
		return fmt.Errorf("error reading %s: %w", filePath, err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("error parsing %s: %w", filePath, err)
	}
	return nil
}

// generateGridCoordinates generates grid-based candidate locations for all regions
func generateGridCoordinates(regions []Region) []CandidateLocation {
	var candidates []CandidateLocation
	log.Println("Generating grid coordinates...")
	totalGenerated := 0
	for _, region := range regions {
		regionGenerated := 0
		latSteps := int((region.MaxLat - region.MinLat) / region.GridDensity)
		lngSteps := int((region.MaxLng - region.MinLng) / region.GridDensity)

		// Estimate points to prevent huge loops if density is tiny
		estimatedPoints := (latSteps + 1) * (lngSteps + 1)
		if estimatedPoints > 1000000 { // Safety cap per region
			log.Printf("Warning: Estimated points for region '%s' (%d) exceeds safety cap. Adjust density or bounds.", region.Name, estimatedPoints)
			continue
		}

		for i := 0; i <= latSteps; i++ {
			for j := 0; j <= lngSteps; j++ {
				lat := region.MinLat + float64(i)*region.GridDensity
				lng := region.MinLng + float64(j)*region.GridDensity
				// Ensure point is within precise bounds (floating point safety)
				if lat <= region.MaxLat && lng <= region.MaxLng {
					candidates = append(candidates, CandidateLocation{
						Latitude:    lat,
						Longitude:   lng,
						Description: fmt.Sprintf("Grid point in %s", region.Description),
						Source:      "grid",
					})
					regionGenerated++
				}
			}
		}
		// log.Printf("Generated %d grid points for region '%s'", regionGenerated, region.Name)
		totalGenerated += regionGenerated
	}
	log.Printf("Finished generating grid coordinates. Total raw grid points: %d", totalGenerated)
	return candidates
}

// limitGridLocations randomly samples a subset if the list is too large
func limitGridLocations(locations []CandidateLocation, maxCount int) []CandidateLocation {
	if maxCount <= 0 || len(locations) <= maxCount {
		return locations // No limiting needed or disabled
	}
	log.Printf("Limiting %d grid locations down to %d points using random sampling...", len(locations), maxCount)
	// rand.Seed is handled in main now
	rand.Shuffle(len(locations), func(i, j int) {
		locations[i], locations[j] = locations[j], locations[i]
	})
	return locations[:maxCount]
}

// locationExistsInDB checks if a similar location exists within tolerance
func locationExistsInDB(db *gorm.DB, lat, lng float64) (bool, uint) {
	const tolerance = 0.001 // ~110 meters latitude, varies longitude
	var location models.Location
	// Only select ID for efficiency when just checking existence
	err := db.Select("id").Where("latitude BETWEEN ? AND ?", lat-tolerance, lat+tolerance).
		Where("longitude BETWEEN ? AND ?", lng-tolerance, lng+tolerance).
		First(&location).Error

	if err == nil {
		return true, location.ID // Found existing
	}
	if err != gorm.ErrRecordNotFound {
		// Log actual DB errors, not just "not found"
		log.Printf("DB Check Error: %v for coords (%f, %f)", err, lat, lng)
	}
	return false, 0
}

// saveLocationToDB saves a validated location to the database using the internal model
func saveLocationToDB(db *gorm.DB, metadata *StreetViewMetadataResponse, description string) (uint, error) {
	location := models.Location{ // Use the model from internal/models
		Latitude:    metadata.Location.Lat, // Use validated coordinates from Google
		Longitude:   metadata.Location.Lng,
		Description: description, // Keep original or generate based on source
		// Country/Region could be added via reverse geocoding later
	}
	result := db.Create(&location) // Capture GORM result
	if result.Error != nil {
		return 0, result.Error
	}
	return location.ID, nil // Return the ID of the newly created record
}

// getStreetViewMetadata calls the Google Street View Metadata API
func getStreetViewMetadata(apiKey string, lat, lng float64) (*StreetViewMetadataResponse, error) {
	baseURL := "https://maps.googleapis.com/maps/api/streetview/metadata"
	params := url.Values{}
	params.Add("location", fmt.Sprintf("%.6f,%.6f", lat, lng)) // Use precision
	params.Add("key", apiKey)
	params.Add("source", "outdoor") // Filter for official Google coverage
	fullURL := baseURL + "?" + params.Encode()

	// Use default HTTP client (or configure one with timeout)
	resp, err := http.Get(fullURL)
	if err != nil {
		return nil, fmt.Errorf("http GET failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed reading response body: %w", err)
	}

	// Try to unmarshal into the response struct regardless of HTTP status
	// This helps capture API-level errors like OVER_QUERY_LIMIT in the JSON body
	var metadata StreetViewMetadataResponse
	jsonErr := json.Unmarshal(body, &metadata)

	// Check HTTP status first
	if resp.StatusCode != http.StatusOK {
		errMsg := fmt.Sprintf("api http status: %s", resp.Status)
		// If JSON parsing worked and we have a status, include it
		if jsonErr == nil && metadata.Status != "" {
			errMsg += fmt.Sprintf(" (API Status: %s)", metadata.Status)
		} else {
			// Otherwise include raw body for debugging non-JSON errors
			errMsg += fmt.Sprintf(", body: %s", string(body))
		}
		return &metadata, fmt.Errorf(errMsg) // Return partial metadata if available + error
	}

	// HTTP OK, but check JSON parsing and internal status
	if jsonErr != nil {
		return nil, fmt.Errorf("failed unmarshalling OK response body: %w, body: %s", jsonErr, string(body))
	}
	if metadata.Status != "OK" {
		// API call succeeded at HTTP level, but Google couldn't find imagery/etc.
		return &metadata, fmt.Errorf("api status: %s", metadata.Status)
	}
	if metadata.Location == nil {
		// Should not happen if status is OK, but defensively check
		return &metadata, fmt.Errorf("api status OK but missing location data")
	}

	// If we reach here, all checks passed
	return &metadata, nil
}
func loadRegions() []Region {
	var regions []Region // Initialize empty slice
	filePath := "./scripts/regions.json"
	err := loadJSONFile(filePath, &regions) // <<< Use helper, pass pointer
	if err != nil {
		// Make region loading fatal as it's likely required
		log.Fatalf("FATAL: %v", err)
	}
	log.Printf("Loaded %d regions from %s", len(regions), filePath)
	return regions
}

// loadManualLocations loads manual locations using the helper
func loadManualLocations() []CandidateLocation {
	var locations []CandidateLocation // Initialize empty slice
	filePath := "./scripts/manual_locations.json"
	err := loadJSONFile(filePath, &locations) // <<< Use helper, pass pointer
	if err != nil {
		// Warn but don't make it fatal for optional manual locations
		log.Printf("Warning: Could not load/parse %s: %v", filePath, err)
		return []CandidateLocation{} // Return empty slice on error
	}
	// Handle case where file exists but is empty "[]" or if loadJSONFile returned nil error for non-existent file
	if locations == nil {
		locations = []CandidateLocation{}
	}
	log.Printf("Loaded %d manual locations from %s", len(locations), filePath)
	return locations
}

// --- Main Logic ---

func main() {
	// --- Flags ---
	batchSize := flag.Int("batch-size", 20000, "Max number of candidate locations to process (0 for all)")
	maxGridPerRegion := flag.Int("max-grid", 20000, "Maximum number of grid points per region to generate (adjust based on density)") // Increased default
	workerCount := flag.Int("workers", runtime.NumCPU(), "Number of concurrent workers")
	apiDelayMs := flag.Int("delay", 30, "Base delay in milliseconds between API calls PER WORKER (increase if rate limited)")
	progressInterval := flag.Int("progress", 500, "Log progress every N processed locations")
	flag.Parse()

	log.Printf("Starting population script with BatchSize=%d, MaxGridPerRegion=%d, Workers=%d, APIDelay=%dms",
		*batchSize, *maxGridPerRegion, *workerCount, *apiDelayMs)

	// --- Env & DB Setup ---
	err := godotenv.Load() // Load .env file from current directory
	if err != nil {
		// Don't treat as fatal, might use system env vars
		log.Println("Info: .env file not found or failed to load. Will rely on system environment variables.")
	}
	apiKey := os.Getenv("GOOGLE_MAPS_API_KEY")
	if apiKey == "" {
		log.Fatal("FATAL: GOOGLE_MAPS_API_KEY environment variable not set.")
	}

	if err := database.Connect(); err != nil { // Connect uses GORM logger settings from database package
		log.Fatalf("FATAL: Error connecting to database: %v", err)
	}
	db := database.GetDB() // Use the getter from the database package
	log.Println("Database connection ready.")

	// --- Load Candidates ---
	regions := loadRegions() // Calls the refactored version
	manualLocations := loadManualLocations()
	log.Printf("Loaded %d regions and %d manual locations.", len(regions), len(manualLocations))

	gridLocations := generateGridCoordinates(regions)
	gridLocations = limitGridLocations(gridLocations, *maxGridPerRegion*len(regions)) // Limit total grid points globally

	allCandidates := append(manualLocations, gridLocations...)
	log.Printf("Total candidate locations generated: %d", len(allCandidates))

	// Shuffle and Slice candidates
	// rand.Seed(time.Now().UnixNano()) // Seed globally once
	rand.New(rand.NewSource(time.Now().UnixNano())) // Use non-deprecated way if needed, though Shuffle uses global
	rand.Shuffle(len(allCandidates), func(i, j int) {
		allCandidates[i], allCandidates[j] = allCandidates[j], allCandidates[i]
	})

	// Determine actual number to process based on batchSize flag
	numToProcess := len(allCandidates)
	if *batchSize > 0 && *batchSize < len(allCandidates) {
		numToProcess = *batchSize
		allCandidates = allCandidates[:numToProcess]
		log.Printf("Processing first %d shuffled candidates based on batch-size flag.", numToProcess)
	} else {
		log.Printf("Processing all %d shuffled candidates.", numToProcess)
	}
	if numToProcess == 0 {
		log.Println("No candidates to process. Exiting.")
		return
	}

	// --- Setup Concurrency & Stats ---
	var processed, validated, skipped, errors int32 // Atomic counters
	startTime := time.Now()
	apiCallDelay := time.Duration(*apiDelayMs) * time.Millisecond

	jobs := make(chan CandidateLocation, numToProcess) // Buffered channel for jobs
	resultsLog := make(chan string, *workerCount)      // Optional: channel for concise results logging
	var wg sync.WaitGroup

	// --- Start Workers ---
	log.Printf("Launching %d workers...", *workerCount)
	for i := 0; i < *workerCount; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			// Small initial stagger for workers
			time.Sleep(time.Duration(rand.Intn(50*workerID+10)) * time.Millisecond)

			for candidate := range jobs {
				currentProcessed := atomic.AddInt32(&processed, 1) // Increment processed count first

				// 1. Check DB Existence
				exists, existingID := locationExistsInDB(db, candidate.Latitude, candidate.Longitude)
				if exists {
					atomic.AddInt32(&skipped, 1)
					resultsLog <- fmt.Sprintf("W%d SkipExist: %d", workerID, existingID) // Optional detailed log
					continue                                                             // Skip to next job
				}

				// 2. Call Google Metadata API (with delay)
				time.Sleep(apiCallDelay) // Apply delay before each API call for this worker
				metadata, err := getStreetViewMetadata(apiKey, candidate.Latitude, candidate.Longitude)

				// 3. Handle API Response/Error
				if err != nil {
					errMsg := err.Error()
					statusMsg := ""
					if metadata != nil { // Metadata might be non-nil even on error (e.g., status errors)
						statusMsg = metadata.Status
					}

					// Check if it's just ZERO_RESULTS (expected skip)
					if statusMsg == "ZERO_RESULTS" || errMsg == "api status: ZERO_RESULTS" {
						atomic.AddInt32(&skipped, 1)
						// resultsLog <- fmt.Sprintf("W%d SkipAPI(ZERO): %.4f,%.4f", workerID, candidate.Latitude, candidate.Longitude)
					} else {
						// Log other errors more visibly
						log.Printf("Worker %d API Error: %v (Status: %s) for Candidate: %.4f,%.4f (%s)",
							workerID, err, statusMsg, candidate.Latitude, candidate.Longitude, candidate.Source)
						atomic.AddInt32(&errors, 1)
					}
					continue // Skip to next job
				}

				// 4. Save Valid Location to DB (Status is guaranteed OK here)
				newID, dbErr := saveLocationToDB(db, metadata, candidate.Description)
				if dbErr != nil {
					log.Printf("Worker %d DB Error saving %.4f,%.4f: %v", workerID, metadata.Location.Lat, metadata.Location.Lng, dbErr)
					atomic.AddInt32(&errors, 1)
				} else {
					atomic.AddInt32(&validated, 1)
					resultsLog <- fmt.Sprintf("Saved %d: %.4f,%.4f (%s)", newID, metadata.Location.Lat, metadata.Location.Lng, candidate.Description) // Log success
				}

				// --- Log Progress Periodically (from worker 0) ---
				if workerID == 0 && int(currentProcessed)%(*progressInterval) == 0 {
					elapsed := time.Since(startTime).Round(time.Second)
					percent := float64(currentProcessed) / float64(numToProcess) * 100
					v := atomic.LoadInt32(&validated)
					s := atomic.LoadInt32(&skipped)
					e := atomic.LoadInt32(&errors)
					log.Printf("Progress: %d/%d (%.1f%%) | Valid: %d | Skip: %d | Err: %d | Elapsed: %v",
						currentProcessed, numToProcess, percent, v, s, e, elapsed)
				}
			}
		}(i)
	}

	// --- Feed Jobs ---
	log.Printf("Feeding %d jobs to workers...", numToProcess)
	for i := 0; i < numToProcess; i++ {
		jobs <- allCandidates[i]
	}
	close(jobs) // Signal no more jobs are coming
	log.Println("All jobs sent.")

	// --- Process Results Log (Optional) ---
	// Goroutine to handle logging results concurrently without blocking workers
	var logWg sync.WaitGroup
	logWg.Add(1)
	go func() {
		defer logWg.Done()
		count := 0
		for res := range resultsLog {
			log.Println(res) // Log each saved item
			count++
		}
		log.Printf("Result logger finished after receiving %d messages.", count)
	}()

	// --- Wait & Final Stats ---
	log.Println("Waiting for workers to finish processing...")
	wg.Wait()         // Wait for all workers in the main WaitGroup
	close(resultsLog) // Close the results channel *after* all workers are done sending
	logWg.Wait()      // Wait for the result logger goroutine to finish processing the channel
	log.Println("All workers finished.")

	duration := time.Since(startTime).Round(time.Second)
	finalProcessed := atomic.LoadInt32(&processed)
	finalValidated := atomic.LoadInt32(&validated)
	finalSkipped := atomic.LoadInt32(&skipped)
	finalErrors := atomic.LoadInt32(&errors)

	log.Println("\n========== FINAL STATISTICS ==========")
	log.Printf("Total Candidates Processed: %d", finalProcessed)
	log.Printf("Successfully Validated & Saved: %d", finalValidated)
	log.Printf("Skipped (Exists or No Coverage): %d", finalSkipped)
	log.Printf("Errors (API or DB): %d", finalErrors)
	log.Printf("Total Runtime: %v", duration)
	if finalProcessed > 0 {
		log.Printf("Avg. Time/Candidate: %v", duration/time.Duration(finalProcessed))
	}
	log.Println("======================================")
}

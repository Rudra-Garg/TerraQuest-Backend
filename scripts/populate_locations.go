// scripts/populate_locations.go
package main

import (
	"context"
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
	"strings"
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
	HasOSMData  bool    `json:"hasOSMData"`
	Include     bool    `json:"include"` // New field to determine if the region should be included
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
	Status   string         `json:"status"`   // "OK", "ZERO_RESULTS", etc.
	Location *LatLngLiteral `json:"location"` // Pointer to handle potential null on non-OK status
	// Copyright string         `json:"copyright"` // Can add if needed
	// Date      string         `json:"date"`      // Can add if needed
	// PanoID    string         `json:"pano_id"`   // Can add if needed
}

// LatLngLiteral represents a latitude-longitude pair from Google API
type LatLngLiteral struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

// LocationBatch collects locations for batch insertion
type LocationBatch struct {
	locations []models.Location
	mutex     sync.Mutex
	batchSize int
	db        *gorm.DB
}

// NewLocationBatch creates a batch processor
func NewLocationBatch(db *gorm.DB, batchSize int) *LocationBatch {
	return &LocationBatch{
		locations: make([]models.Location, 0, batchSize),
		batchSize: batchSize,
		db:        db,
	}
}

// flushInternal performs the actual database insertion.
// IMPORTANT: It assumes the caller (Add or Flush) already holds b.mutex.
func (b *LocationBatch) flushInternal() error {
	if len(b.locations) == 0 {
		return nil
	}

	// Create a new transaction for this batch flush
	tx := b.db.Begin()
	if tx.Error != nil {
		log.Printf("ERROR: Failed to begin transaction in flushInternal: %v", tx.Error)
		// Return the error so the calling function knows the flush failed
		return fmt.Errorf("failed to begin transaction: %w", tx.Error)
	}

	// Insert the current batch
	// Use len(b.locations) because it might be less than b.batchSize if Flush() is called manually.
	result := tx.CreateInBatches(b.locations, len(b.locations))
	if result.Error != nil {
		log.Printf("ERROR: CreateInBatches failed in flushInternal: %v. Rolling back.", result.Error)
		// Attempt to rollback, log if rollback fails but return the original error
		if rbErr := tx.Rollback().Error; rbErr != nil {
			log.Printf("ERROR: Failed to rollback transaction after CreateInBatches error: %v", rbErr)
		}
		return fmt.Errorf("CreateInBatches failed: %w", result.Error)
	}

	// Commit the transaction
	if err := tx.Commit().Error; err != nil {
		log.Printf("ERROR: Failed to commit transaction in flushInternal: %v", err)
		// Don't clear the batch if commit fails, return the error
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	// Clear the batch only after successful commit
	b.locations = b.locations[:0]
	// log.Printf("DEBUG: Batch flushed successfully, size reset to 0.") // Optional debug log
	return nil
}

// Add adds a location to the batch, flushing automatically if the batch size is reached.
func (b *LocationBatch) Add(location models.Location) error {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	b.locations = append(b.locations, location)
	// log.Printf("DEBUG: Added location, batch size now %d/%d", len(b.locations), b.batchSize) // Optional debug log

	if len(b.locations) >= b.batchSize {
		// log.Printf("DEBUG: Batch size %d reached, calling flushInternal", b.batchSize) // Optional debug log
		// Call the internal flush method which assumes the lock is held
		err := b.flushInternal()
		if err != nil {
			// Log the error specifically from the auto-flush scenario
			log.Printf("ERROR: Automatic batch flush failed within Add(): %v", err)
			// Return the error so the worker loop can handle it (e.g., increment error count)
			return err
		}
	}
	return nil
}

// Flush writes any remaining pending locations to the database.
// This is typically called after all processing is done.
func (b *LocationBatch) Flush() error {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	// log.Printf("DEBUG: Manual Flush() called, current batch size %d", len(b.locations)) // Optional debug log
	// Call the internal method that performs the flush operation
	return b.flushInternal()
}

// LocationCache provides a simple in-memory cache for location existence checks
type LocationCache struct {
	cache map[string]uint // key: "lat:lng", value: ID
	mutex sync.RWMutex
}

// NewLocationCache creates a new location cache
func NewLocationCache(initialCapacity int) *LocationCache {
	return &LocationCache{
		cache: make(map[string]uint, initialCapacity),
	}
}

// Key generates a cache key from lat/lng with reduced precision
func (c *LocationCache) Key(lat, lng float64) string {
	// Reduce precision to group nearby points (matching our tolerance)
	// Round to 3 decimal places (about 110m precision)
	return fmt.Sprintf("%.3f:%.3f", lat, lng)
}

// Exists checks if coordinates exist in cache
func (c *LocationCache) Exists(lat, lng float64) (bool, uint) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	key := c.Key(lat, lng)
	id, exists := c.cache[key]
	return exists, id
}

// Add adds coordinates to cache
func (c *LocationCache) Add(lat, lng float64, id uint) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	key := c.Key(lat, lng)
	c.cache[key] = id
}

// PrePopulateFromDB loads existing locations from the DB into the cache
func (c *LocationCache) PrePopulateFromDB(db *gorm.DB, regions []Region) error {
	log.Println("Pre-populating location cache from database...")
	var existingLocations []struct {
		ID        uint
		Latitude  float64
		Longitude float64
	}

	// Build a query that only selects locations within the bounding boxes of included regions
	query := db.Model(&models.Location{})
	var conditions []string
	var args []interface{}

	hasIncludedRegions := false
	for _, region := range regions {
		if region.Include {
			hasIncludedRegions = true
			conditions = append(conditions, "(latitude BETWEEN ? AND ? AND longitude BETWEEN ? AND ?)")
			args = append(args, region.MinLat, region.MaxLat, region.MinLng, region.MaxLng)
		}
	}

	if hasIncludedRegions && len(conditions) > 0 {
		query = query.Where(strings.Join(conditions, " OR "), args...)
	} else {
		log.Println("No specific regions marked for inclusion for cache pre-population, or no regions defined. Will attempt to load ALL locations (this might be slow/memory intensive for very large DBs).")
	}

	if err := query.Select("id, latitude, longitude").Find(&existingLocations).Error; err != nil {
		return fmt.Errorf("failed to query existing locations: %w", err)
	}

	startCacheAdd := time.Now()
	c.mutex.Lock() // Lock cache for bulk add
	for _, loc := range existingLocations {
		key := c.Key(loc.Latitude, loc.Longitude)
		c.cache[key] = loc.ID
	}
	c.mutex.Unlock() // Unlock cache
	cacheAddDuration := time.Since(startCacheAdd)

	log.Printf("Pre-populated cache with %d existing locations in %v.", len(existingLocations), cacheAddDuration)
	return nil
}

// --- Helper Functions ---

// loadJSONFile reads and unmarshals a JSON file into the target interface
func loadJSONFile(filePath string, target interface{}) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) && filePath == "./scripts/manual_locations.json" {
			log.Printf("Info: %s not found, skipping manual locations.", filePath)
			return nil
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
		if !region.Include {
			log.Printf("Skipping region '%s' as it is marked to be excluded.", region.Name)
			continue
		}
		regionGenerated := 0
		latSteps := int((region.MaxLat - region.MinLat) / region.GridDensity)
		lngSteps := int((region.MaxLng - region.MinLng) / region.GridDensity)

		estimatedPoints := (latSteps + 1) * (lngSteps + 1)
		if estimatedPoints > 1000000 { // Safety cap per region
			log.Printf("Warning: Estimated points for region '%s' (%d) exceeds safety cap. Adjust density or bounds.", region.Name, estimatedPoints)
			continue
		}

		for i := 0; i <= latSteps; i++ {
			for j := 0; j <= lngSteps; j++ {
				lat := region.MinLat + float64(i)*region.GridDensity
				lng := region.MinLng + float64(j)*region.GridDensity
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
		totalGenerated += regionGenerated
	}
	log.Printf("Finished generating grid coordinates. Total raw grid points: %d", totalGenerated)
	return candidates
}

// limitGridLocations randomly samples a subset if the list is too large
func limitGridLocations(locations []CandidateLocation, maxCount int) []CandidateLocation {
	if maxCount <= 0 || len(locations) <= maxCount {
		return locations
	}
	log.Printf("Limiting %d grid locations down to %d points using random sampling...", len(locations), maxCount)
	rand.Shuffle(len(locations), func(i, j int) {
		locations[i], locations[j] = locations[j], locations[i]
	})
	return locations[:maxCount]
}

// getStreetViewMetadata calls the Google Street View Metadata API using a shared HTTP client
func getStreetViewMetadata(client *http.Client, apiKey string, lat, lng float64) (*StreetViewMetadataResponse, error) {
	baseURL := "https://maps.googleapis.com/maps/api/streetview/metadata"
	params := url.Values{}
	params.Add("location", fmt.Sprintf("%.6f,%.6f", lat, lng))
	params.Add("key", apiKey)
	params.Add("source", "outdoor")
	fullURL := baseURL + "?" + params.Encode()

	req, err := http.NewRequestWithContext(context.Background(), "GET", fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http GET failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed reading response body: %w", err)
	}

	var metadata StreetViewMetadataResponse
	jsonErr := json.Unmarshal(body, &metadata)

	if resp.StatusCode != http.StatusOK {
		errMsg := fmt.Sprintf("api http status: %s", resp.Status)
		if jsonErr == nil && metadata.Status != "" {
			errMsg += fmt.Sprintf(" (API Status: %s)", metadata.Status)
		} else {
			errMsg += fmt.Sprintf(", body: %s", string(body))
		}
		return &metadata, fmt.Errorf(errMsg)
	}

	if jsonErr != nil {
		return nil, fmt.Errorf("failed unmarshaling OK response body: %w, body: %s", jsonErr, string(body))
	}
	if metadata.Status != "OK" {
		return &metadata, fmt.Errorf("api status: %s", metadata.Status)
	}
	if metadata.Location == nil {
		return &metadata, fmt.Errorf("api status OK but missing location data")
	}

	return &metadata, nil
}

func loadRegions() []Region {
	var regions []Region
	filePath := "./scripts/regions.json"
	if err := loadJSONFile(filePath, &regions); err != nil {
		log.Fatalf("FATAL: %v", err)
	}
	log.Printf("Loaded %d regions from %s", len(regions), filePath)
	return regions
}

func loadManualLocations() []CandidateLocation {
	var locations []CandidateLocation
	filePath := "./scripts/manual_locations.json"
	if err := loadJSONFile(filePath, &locations); err != nil {
		log.Printf("Warning: Could not load/parse %s: %v. Proceeding without manual locations.", filePath, err)
		return []CandidateLocation{}
	}
	if locations == nil { // Handle empty JSON array "[]"
		locations = []CandidateLocation{}
	}
	log.Printf("Loaded %d manual locations from %s", len(locations), filePath)
	return locations
}

func optimizeDBSettings(db *gorm.DB) {
	sqlDB, err := db.DB()
	if err != nil {
		log.Printf("Warning: Could not access underlying DB to optimize settings: %v", err)
		return
	}
	sqlDB.SetMaxIdleConns(20)  // Increased slightly
	sqlDB.SetMaxOpenConns(120) // Increased slightly
	sqlDB.SetConnMaxLifetime(time.Hour)
	log.Println("Database connection pool optimized.")
}

func createSpatialIndex(db *gorm.DB) error {
	// Composite index is generally good for lat/lng queries.
	// For PostGIS, a GIST index on a geometry/geography column is superior.
	// // indexName := "idx_locations_coordinates"
	// if err := db.Migrator().CreateIndex(&models.Location{}, "latitude"); err != nil {
	// 	log.Printf("Warning: could not create index on latitude: %v", err)
	// }
	// if err := db.Migrator().CreateIndex(&models.Location{}, "longitude"); err != nil {
	// 	log.Printf("Warning: could not create index on longitude: %v", err)
	// }
	// Attempt to create composite index (syntax might vary slightly by DB, GORM tries to abstract)
	// For SQLite, separate indexes are often better than composite for BETWEEN queries.
	// For PostgreSQL/MySQL, composite (latitude, longitude) is good.
	// GORM's AutoMigrate might create these, but explicit ensures it.
	// Let's use GORM's preferred way if simple, otherwise raw SQL for composite.
	// db.Migrator().CreateIndex(&models.Location{}, "latitude", "longitude") // GORM's way for composite index
	// Using raw SQL for composite index for better control:
	// Note: GORM will not drop this index automatically if the model changes.
	// Check your DB dialect for the exact syntax if this fails.
	// For PostgreSQL:
	// err := db.Exec("CREATE INDEX IF NOT EXISTS idx_locations_lat_lng ON locations (latitude, longitude);").Error
	// For SQLite (often prefers separate indexes for BETWEEN):
	// No specific action for composite here beyond the individual ones above.
	// For MySQL:
	// err := db.Exec("CREATE INDEX IF NOT EXISTS idx_locations_lat_lng ON locations (latitude, longitude);").Error
	// Since we are checking cache first, this index is now for pre-population query.
	log.Printf("Ensured indexes on latitude and longitude exist for efficient cache pre-population.")

	// PostgreSQL with PostGIS example (keep commented unless using PostGIS)
	/*
		if db.Dialector.Name() == "postgres" {
			log.Println("Attempting to create PostGIS spatial index...")
			err := db.Exec(`
				DO $$
				BEGIN
					IF NOT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'postgis') THEN
						CREATE EXTENSION IF NOT EXISTS postgis;
					END IF;
				END
				$$;
				ALTER TABLE locations ADD COLUMN IF NOT EXISTS geom GEOMETRY(Point, 4326);
				-- Ensure existing data is updated, only if geom is null
				UPDATE locations SET geom = ST_SetSRID(ST_MakePoint(longitude, latitude), 4326) WHERE geom IS NULL AND latitude IS NOT NULL AND longitude IS NOT NULL;
				-- Create spatial index if it doesn't exist
				IF NOT EXISTS (
					SELECT 1
					FROM   pg_class c
					JOIN   pg_namespace n ON n.oid = c.relnamespace
					WHERE  c.relname = 'idx_locations_geom_gist'
					AND    n.nspname = 'public' -- or your specific schema
				) THEN
					CREATE INDEX idx_locations_geom_gist ON locations USING GIST (geom);
					log.Println("PostGIS GIST index created on geom column.");
				ELSE
					log.Println("PostGIS GIST index on geom column already exists.");
				END IF;
			`).Error
			if err != nil {
				log.Printf("Warning: Could not create PostGIS spatial index: %v (This is fine if not using PostGIS or if it already exists)", err)
			}
		}
	*/
	return nil
}

// --- Main Logic ---

// WorkerStats holds statistics for each worker
type WorkerStats struct {
	Processed int32
	Validated int32
	Skipped   int32
	Errors    int32
}

func main() {
	// --- Flags ---
	batchSizeFlag := flag.Int("batch-size", 0, "Max number of candidate locations to process (0 for all)")
	maxGridPerRegion := flag.Int("max-grid", 20000, "Maximum number of grid points per region to generate")
	workerCount := flag.Int("workers", runtime.NumCPU(), "Number of concurrent workers")
	apiDelayMs := flag.Int("delay", 20, "Base delay in milliseconds between API calls PER WORKER (increase if rate limited)") // Slightly increased default
	logIntervalSec := flag.Int("log-interval", 5, "Interval in seconds for logging progress")
	dbBatchInsertSize := flag.Int("db-batch-size", 100, "Number of locations to batch insert into DB at once")
	apiTimeoutSec := flag.Int("api-timeout", 20, "Timeout in seconds for Google API calls")
	flag.Parse()

	log.Printf("Starting population script: BatchSize=%d, MaxGridPerRegion=%d, Workers=%d, APIDelay=%dms, DBBatchInsert=%d, LogInterval=%ds, APITimeout=%ds",
		*batchSizeFlag, *maxGridPerRegion, *workerCount, *apiDelayMs, *dbBatchInsertSize, *logIntervalSec, *apiTimeoutSec)

	// --- Env & DB Setup ---
	if err := godotenv.Load(); err != nil {
		log.Println("Info: .env file not found or failed to load. Relying on system environment variables.")
	}
	apiKey := os.Getenv("GOOGLE_MAPS_API_KEY")
	if apiKey == "" {
		log.Fatal("FATAL: GOOGLE_MAPS_API_KEY environment variable not set.")
	}

	if err := database.Connect(); err != nil {
		log.Fatalf("FATAL: Error connecting to database: %v", err)
	}
	db := database.GetDB()
	optimizeDBSettings(db)
	if err := createSpatialIndex(db); err != nil {
		// This is not fatal, but good to know. The cache pre-population might be slower.
		log.Printf("Warning: Could not ensure/create spatial index: %v", err)
	}

	// --- Create Shared HTTP Client ---
	sharedHttpClient := &http.Client{
		Timeout: time.Duration(*apiTimeoutSec) * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        *workerCount * 2,
			MaxIdleConnsPerHost: *workerCount * 2, // Important for single API endpoint
			IdleConnTimeout:     90 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second,
		},
	}

	// --- Load Regions & Candidate Locations ---
	regions := loadRegions()
	manualLocations := loadManualLocations()
	gridLocations := generateGridCoordinates(regions)
	gridLocations = limitGridLocations(gridLocations, (*maxGridPerRegion)*len(regions)) // Limit total grid points

	allCandidates := append(manualLocations, gridLocations...)
	log.Printf("Total candidate locations generated: %d", len(allCandidates))

	rand.New(rand.NewSource(time.Now().UnixNano())) // Seed rand
	rand.Shuffle(len(allCandidates), func(i, j int) {
		allCandidates[i], allCandidates[j] = allCandidates[j], allCandidates[i]
	})

	numToProcess := len(allCandidates)
	if *batchSizeFlag > 0 && *batchSizeFlag < len(allCandidates) {
		numToProcess = *batchSizeFlag
		allCandidates = allCandidates[:numToProcess]
		log.Printf("Processing first %d shuffled candidates based on batch-size flag.", numToProcess)
	} else if numToProcess > 0 {
		log.Printf("Processing all %d shuffled candidates.", numToProcess)
	}

	if numToProcess == 0 {
		log.Println("No candidates to process. Exiting.")
		return
	}

	// --- Pre-populate Cache ---
	// Initial capacity can be estimated based on expected unique locations or total candidates
	// Max of numToProcess or a reasonable upper bound like 500k
	cacheCapacity := numToProcess
	if cacheCapacity < 10000 {
		cacheCapacity = 10000
	} else if cacheCapacity > 500000 {
		cacheCapacity = 500000 // Cap initial map allocation
	}
	locationCache := NewLocationCache(cacheCapacity)
	if err := locationCache.PrePopulateFromDB(db, regions); err != nil {
		log.Fatalf("FATAL: Failed to pre-populate location cache: %v", err)
	}

	batchProcessors := make([]*LocationBatch, *workerCount)
	for i := 0; i < *workerCount; i++ {
		batchProcessors[i] = NewLocationBatch(db, *dbBatchInsertSize)
	}

	// --- Setup Concurrency, Stats & Logging ---
	var totalProcessed, totalValidated, totalSkipped, totalErrors int32 // Global atomic counters
	startTime := time.Now()
	apiCallDelay := time.Duration(*apiDelayMs) * time.Millisecond

	jobs := make(chan CandidateLocation, numToProcess)
	var workerWg sync.WaitGroup // WaitGroup for workers

	workerStats := make([]WorkerStats, *workerCount) // Per-worker stats

	// --- Start Dedicated Logging Goroutine ---
	var loggerWg sync.WaitGroup
	loggerWg.Add(1)
	loggingDoneChan := make(chan struct{})

	go func() {
		defer loggerWg.Done()
		ticker := time.NewTicker(time.Duration(*logIntervalSec) * time.Second)
		defer ticker.Stop()

		// Initial short delay before first log to allow some processing
		time.Sleep(1 * time.Second)

		for {
			select {
			case <-ticker.C:
				currentProcessed := atomic.LoadInt32(&totalProcessed)
				if currentProcessed == 0 && time.Since(startTime) < time.Duration(*logIntervalSec+2)*time.Second {
					// Don't log too early if nothing processed yet, unless significant time passed
					continue
				}

				elapsed := time.Since(startTime).Round(time.Second)
				percent := 0.0
				if numToProcess > 0 {
					percent = (float64(currentProcessed) / float64(numToProcess)) * 100
				}

				v := atomic.LoadInt32(&totalValidated)
				s := atomic.LoadInt32(&totalSkipped)
				e := atomic.LoadInt32(&totalErrors)
				//Calculate estimated remaining time
				remaining := time.Duration(0)
				if currentProcessed > 0 && elapsed > 0 {
					pps := float64(currentProcessed) / elapsed.Seconds()
					remaining = time.Duration(float64(int32(numToProcess)-currentProcessed)/pps) * time.Second
				}
				// Use fmt.Printf for direct console output, log.Printf adds prefixes/timestamps
				// which can be noisy for frequent updates.
				// Ensure this block is printed atomically or manage cursor if overwriting.
				// Printing a new block is simpler.
				var sb strings.Builder
				sb.WriteString(fmt.Sprintf("\n--- Progress @ %s ---\n", time.Now().Format("15:04:05")))
				sb.WriteString(fmt.Sprintf("Processed: %d/%d (%.1f%%) | Valid: %d | Skipped: %d | Errors: %d | Elapsed: %v | Remaining: %v\n",
					currentProcessed, numToProcess, percent, v, s, e, elapsed, remaining))

				if currentProcessed > 0 && elapsed > 0 {
					pps := float64(currentProcessed) / elapsed.Seconds()
					sb.WriteString(fmt.Sprintf("Rate: %.2f candidates/sec\n", pps))
				}

				sb.WriteString("Worker Status:\n")
				sb.WriteString(fmt.Sprintf("%-7s | %-9s | %-8s | %-7s | %-6s\n", "Worker", "Processed", "Valid", "Skipped", "Errors"))
				sb.WriteString(strings.Repeat("-", 50) + "\n")

				activeWorkers := 0
				for i := 0; i < *workerCount; i++ {
					wp := atomic.LoadInt32(&workerStats[i].Processed)
					// Only show active workers or workers that have processed something
					if wp > 0 || (time.Since(startTime) < 30*time.Second && i < *workerCount) { // Show all for first 30s
						activeWorkers++
						wv := atomic.LoadInt32(&workerStats[i].Validated)
						ws := atomic.LoadInt32(&workerStats[i].Skipped)
						we := atomic.LoadInt32(&workerStats[i].Errors)
						sb.WriteString(fmt.Sprintf("%-7d | %-9d | %-8d | %-7d | %-6d\n", i, wp, wv, ws, we))
					}
				}
				if activeWorkers == 0 && currentProcessed > 0 { // If processing started but no worker stats yet
					sb.WriteString(" (Worker stats populating...)\n")
				}
				sb.WriteString(strings.Repeat("-", 50) + "\n")
				fmt.Print(sb.String())

			case <-loggingDoneChan:
				log.Println("Logging goroutine shutting down.")
				return
			}
		}
	}()

	// --- Start Workers ---
	log.Printf("Launching %d workers...", *workerCount)
	for i := 0; i < *workerCount; i++ {
		workerWg.Add(1)
		go func(workerID int) {
			defer workerWg.Done()
			// Use this worker's dedicated batch processor
			workerBatch := batchProcessors[workerID]

			// Small initial stagger for workers
			time.Sleep(time.Duration(rand.Intn(50*workerID+50)) * time.Millisecond)

			localProcessed := 0
			localValidated := 0
			localSkipped := 0
			localErrors := 0

			// Last time this worker updated its global stats
			workerLastStatUpdate := time.Now()

			for candidate := range jobs {
				atomic.AddInt32(&totalProcessed, 1)
				localProcessed++

				// 1. Check Cache (already pre-populated)
				exists, _ := locationCache.Exists(candidate.Latitude, candidate.Longitude)
				if exists {
					atomic.AddInt32(&totalSkipped, 1)
					localSkipped++
					continue
				}

				// 2. Call Google Metadata API (with delay)
				time.Sleep(apiCallDelay)
				metadata, err := getStreetViewMetadata(sharedHttpClient, apiKey, candidate.Latitude, candidate.Longitude)

				// 3. Handle API Response/Error
				if err != nil {
					statusMsg := ""
					if metadata != nil {
						statusMsg = metadata.Status
					}

					if statusMsg == "ZERO_RESULTS" || strings.Contains(err.Error(), "ZERO_RESULTS") {
						atomic.AddInt32(&totalSkipped, 1)
						localSkipped++
					} else if statusMsg == "OVER_QUERY_LIMIT" || strings.Contains(err.Error(), "OVER_QUERY_LIMIT") {
						log.Printf("Worker %d: Received OVER_QUERY_LIMIT. Consider increasing -delay or reducing -workers.", workerID)
						// Optionally, could implement a backoff strategy here for this worker
						atomic.AddInt32(&totalErrors, 1)
						localErrors++
						time.Sleep(5 * time.Second) // Pause this worker a bit longer
					} else {
						// Log other errors less frequently to reduce spam
						if localErrors%20 == 0 || time.Since(startTime) < 60*time.Second { // Log more frequently at start
							log.Printf("Worker %d API Error: %v (Candidate: %.4f,%.4f, Source: %s)", workerID, err, candidate.Latitude, candidate.Longitude, candidate.Source)
						}
						atomic.AddInt32(&totalErrors, 1)
						localErrors++
					}
					continue
				}

				// 4. Add Valid Location to Batch (cache will be updated by next pre-population run)
				newLocation := models.Location{
					Latitude:    metadata.Location.Lat,
					Longitude:   metadata.Location.Lng,
					Description: candidate.Description,
				}
				if dbErr := workerBatch.Add(newLocation); dbErr != nil {
					if localErrors%10 == 0 {
						log.Printf("Worker %d DB Batch Add Error: %v", workerID, dbErr)
					}
					atomic.AddInt32(&totalErrors, 1)
					localErrors++
				} else {
					atomic.AddInt32(&totalValidated, 1)
					localValidated++
					// Optionally add to cache immediately to prevent duplicate processing within THIS run
					// if another candidate is extremely close and processed by another worker before batch flush.
					// locationCache.Add(newLocation.Latitude, newLocation.Longitude, 0) // ID 0 as it's not persisted yet
				}

				// Update this worker's contribution to global workerStats periodically
				if time.Since(workerLastStatUpdate) > 2*time.Second { // More frequent updates for finer-grained view
					atomic.StoreInt32(&workerStats[workerID].Processed, int32(localProcessed))
					atomic.StoreInt32(&workerStats[workerID].Validated, int32(localValidated))
					atomic.StoreInt32(&workerStats[workerID].Skipped, int32(localSkipped))
					atomic.StoreInt32(&workerStats[workerID].Errors, int32(localErrors))
					workerLastStatUpdate = time.Now()
				}
			}

			// Final update of this worker's stats
			atomic.StoreInt32(&workerStats[workerID].Processed, int32(localProcessed))
			atomic.StoreInt32(&workerStats[workerID].Validated, int32(localValidated))
			atomic.StoreInt32(&workerStats[workerID].Skipped, int32(localSkipped))
			atomic.StoreInt32(&workerStats[workerID].Errors, int32(localErrors))

			log.Printf("Worker %d finished. Processed: %d, Valid: %d, Skipped: %d, Errors: %d",
				workerID, localProcessed, localValidated, localSkipped, localErrors)
		}(i)
	}

	// --- Feed Jobs ---
	log.Printf("Feeding %d jobs to workers...", numToProcess)
	for i := 0; i < numToProcess; i++ {
		jobs <- allCandidates[i]
	}
	close(jobs)
	log.Println("All jobs sent.")

	// --- Wait for Workers & Logger, then Final Stats ---
	log.Println("Waiting for workers to finish processing...")
	workerWg.Wait()
	log.Println("All workers finished.")

	// Signal logger to stop and wait for it
	close(loggingDoneChan)
	loggerWg.Wait()
	log.Println("Logging goroutine finished.")

	// Flush all batch processors
	log.Println("Flushing any remaining locations in DB batch processors...")
	var flushErrors int
	for i, processor := range batchProcessors {
		if err := processor.Flush(); err != nil {
			log.Printf("Error flushing worker %d batch: %v", i, err)
			flushErrors++
		}
	}
	log.Printf("DB Batch processors flushed (%d errors).", flushErrors)

	duration := time.Since(startTime).Round(time.Second)
	finalProcessed := atomic.LoadInt32(&totalProcessed)
	finalValidated := atomic.LoadInt32(&totalValidated)
	finalSkipped := atomic.LoadInt32(&totalSkipped)
	finalErrors := atomic.LoadInt32(&totalErrors)

	// Final summary printed with log.Printf for standard log output
	log.Println("\n========== FINAL STATISTICS ==========")
	log.Printf("Total Candidates Processed: %d / %d", finalProcessed, numToProcess)
	log.Printf("Successfully Validated & Saved: %d", finalValidated)
	log.Printf("Skipped (Exists in Cache or No StreetView): %d", finalSkipped)
	log.Printf("Errors (API or DB): %d", finalErrors)
	log.Printf("Total Runtime: %v", duration)
	if finalProcessed > 0 && duration > 0 {
		pps := float64(finalProcessed) / duration.Seconds()
		log.Printf("Overall Rate: %.2f candidates/sec", pps)
	}
	log.Println("======================================")
	log.Println("Population script finished.")
}

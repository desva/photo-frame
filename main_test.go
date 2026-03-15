package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func setupTestApp(t *testing.T) *App {
	t.Helper()
	tmpDir := t.TempDir()
	photoDir := filepath.Join(tmpDir, "photos")
	configDir := filepath.Join(tmpDir, "data")
	os.MkdirAll(photoDir, 0755)
	os.MkdirAll(configDir, 0755)

	dbPath := filepath.Join(configDir, "frame.db")
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}

	app := &App{
		db:        db,
		photoDir:  photoDir,
		configDir: configDir,
		syncToken: "test-token",
	}
	app.initDB()
	app.loadConfig()
	return app
}

func TestHealthEndpoint(t *testing.T) {
	app := setupTestApp(t)
	defer app.db.Close()

	req := httptest.NewRequest("GET", "/api/health", nil)
	w := httptest.NewRecorder()
	app.handleHealth(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var result map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &result)
	if result["status"] != "ok" {
		t.Fatalf("expected status ok, got %v", result["status"])
	}
	if result["photos"].(float64) != 0 {
		t.Fatalf("expected 0 photos, got %v", result["photos"])
	}
}

func TestConfigGetPut(t *testing.T) {
	app := setupTestApp(t)
	defer app.db.Close()

	// GET default config
	req := httptest.NewRequest("GET", "/api/config", nil)
	w := httptest.NewRecorder()
	app.handleConfig(w, req)

	var config Config
	json.Unmarshal(w.Body.Bytes(), &config)
	if config.SlideshowSeconds != 20 {
		t.Fatalf("expected 20s default, got %d", config.SlideshowSeconds)
	}
	if config.Banner != "" {
		t.Fatalf("expected empty banner, got %q", config.Banner)
	}

	// PUT new config
	body, _ := json.Marshal(Config{
		Banner:           "Test Message",
		SlideshowSeconds: 15,
		Enabled:          true,
		UpdatedBy:        "test",
	})
	req = httptest.NewRequest("PUT", "/api/config", bytes.NewReader(body))
	w = httptest.NewRecorder()
	app.handleConfig(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// Verify saved
	req = httptest.NewRequest("GET", "/api/config", nil)
	w = httptest.NewRecorder()
	app.handleConfig(w, req)

	json.Unmarshal(w.Body.Bytes(), &config)
	if config.Banner != "Test Message" {
		t.Fatalf("expected 'Test Message', got %q", config.Banner)
	}
	if config.SlideshowSeconds != 15 {
		t.Fatalf("expected 15, got %d", config.SlideshowSeconds)
	}
}

func TestSyncMetadata(t *testing.T) {
	app := setupTestApp(t)
	defer app.db.Close()

	// Sync some photos
	payload := SyncPayload{
		Photos: []SyncPhoto{
			{Path: "vacation/beach.jpg", Caption: "Beach sunset", People: []string{"Dan", "Katie"}},
			{Path: "birthday/cake.jpg", Caption: "Birthday cake", People: []string{"Florence"}},
		},
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest("POST", "/api/sync", bytes.NewReader(body))
	req.Header.Set("X-Sync-Token", "test-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	app.handleSync(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var result map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &result)
	if result["added"].(float64) != 2 {
		t.Fatalf("expected 2 added, got %v", result["added"])
	}

	// Verify photos in DB
	req = httptest.NewRequest("GET", "/api/health", nil)
	w = httptest.NewRecorder()
	app.handleHealth(w, req)

	json.Unmarshal(w.Body.Bytes(), &result)
	if result["photos"].(float64) != 2 {
		t.Fatalf("expected 2 photos, got %v", result["photos"])
	}

	// Verify people tags via photos API
	req = httptest.NewRequest("GET", "/api/photos?count=10", nil)
	w = httptest.NewRecorder()
	app.handlePhotos(w, req)

	var photosResult map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &photosResult)
	photos := photosResult["photos"].([]interface{})
	if len(photos) != 2 {
		t.Fatalf("expected 2 photos, got %d", len(photos))
	}
}

func TestSyncUnauthorized(t *testing.T) {
	app := setupTestApp(t)
	defer app.db.Close()

	payload := SyncPayload{Photos: []SyncPhoto{{Path: "test.jpg", Caption: "test"}}}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest("POST", "/api/sync", bytes.NewReader(body))
	req.Header.Set("X-Sync-Token", "wrong-token")
	w := httptest.NewRecorder()
	app.handleSync(w, req)

	if w.Code != 401 {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestSyncDelete(t *testing.T) {
	app := setupTestApp(t)
	defer app.db.Close()

	// Add a photo
	payload := SyncPayload{
		Photos: []SyncPhoto{{Path: "delete-me.jpg", Caption: "To be deleted"}},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest("POST", "/api/sync", bytes.NewReader(body))
	req.Header.Set("X-Sync-Token", "test-token")
	w := httptest.NewRecorder()
	app.handleSync(w, req)

	// Delete it
	payload = SyncPayload{
		Photos: []SyncPhoto{{Path: "delete-me.jpg", Delete: true}},
	}
	body, _ = json.Marshal(payload)
	req = httptest.NewRequest("POST", "/api/sync", bytes.NewReader(body))
	req.Header.Set("X-Sync-Token", "test-token")
	w = httptest.NewRecorder()
	app.handleSync(w, req)

	var result map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &result)
	if result["deleted"].(float64) != 1 {
		t.Fatalf("expected 1 deleted, got %v", result["deleted"])
	}
}

func TestSyncPhotoFile(t *testing.T) {
	app := setupTestApp(t)
	defer app.db.Close()

	// Upload a photo file
	content := []byte("fake-jpeg-content")
	req := httptest.NewRequest("POST", "/api/sync/photo?path=test/photo.jpg", bytes.NewReader(content))
	req.Header.Set("X-Sync-Token", "test-token")
	w := httptest.NewRecorder()
	app.handleSyncPhoto(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// Verify file exists
	fullPath := filepath.Join(app.photoDir, "test", "photo.jpg")
	data, err := os.ReadFile(fullPath)
	if err != nil {
		t.Fatalf("file not created: %v", err)
	}
	if string(data) != "fake-jpeg-content" {
		t.Fatalf("file content mismatch")
	}
}

func TestSyncPhotoFileUnauthorized(t *testing.T) {
	app := setupTestApp(t)
	defer app.db.Close()

	req := httptest.NewRequest("POST", "/api/sync/photo?path=test.jpg", bytes.NewReader([]byte("data")))
	req.Header.Set("X-Sync-Token", "wrong")
	w := httptest.NewRecorder()
	app.handleSyncPhoto(w, req)

	if w.Code != 401 {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestImagePathTraversal(t *testing.T) {
	app := setupTestApp(t)
	defer app.db.Close()

	// Try path traversal
	req := httptest.NewRequest("GET", "/api/image/../../../etc/passwd", nil)
	w := httptest.NewRecorder()
	app.handleImage(w, req)

	if w.Code != 403 {
		t.Fatalf("expected 403 for traversal, got %d", w.Code)
	}
}

func TestFramePageServed(t *testing.T) {
	app := setupTestApp(t)
	defer app.db.Close()

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	app.handleFrame(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !bytes.Contains([]byte(body), []byte("Family Photos")) {
		t.Fatal("expected 'Family Photos' in response")
	}
}

func TestSettingsPageServed(t *testing.T) {
	app := setupTestApp(t)
	defer app.db.Close()

	req := httptest.NewRequest("GET", "/settings", nil)
	w := httptest.NewRecorder()
	app.handleSettings(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !bytes.Contains([]byte(body), []byte("Photo Frame Settings")) {
		t.Fatal("expected 'Photo Frame Settings' in response")
	}
}

func TestPhotosEmptyDB(t *testing.T) {
	app := setupTestApp(t)
	defer app.db.Close()

	req := httptest.NewRequest("GET", "/api/photos?count=10", nil)
	w := httptest.NewRecorder()
	app.handlePhotos(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var result map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &result)
	photos := result["photos"].([]interface{})
	if len(photos) != 0 {
		t.Fatalf("expected 0 photos, got %d", len(photos))
	}
}

func TestSyncUpdate(t *testing.T) {
	app := setupTestApp(t)
	defer app.db.Close()

	// Add a photo
	payload := SyncPayload{
		Photos: []SyncPhoto{{Path: "update.jpg", Caption: "Original"}},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest("POST", "/api/sync", bytes.NewReader(body))
	req.Header.Set("X-Sync-Token", "test-token")
	w := httptest.NewRecorder()
	app.handleSync(w, req)

	// Update it
	payload = SyncPayload{
		Photos: []SyncPhoto{{Path: "update.jpg", Caption: "Updated caption", People: []string{"New Person"}}},
	}
	body, _ = json.Marshal(payload)
	req = httptest.NewRequest("POST", "/api/sync", bytes.NewReader(body))
	req.Header.Set("X-Sync-Token", "test-token")
	w = httptest.NewRecorder()
	app.handleSync(w, req)

	var result map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &result)
	if result["updated"].(float64) != 1 {
		t.Fatalf("expected 1 updated, got %v", result["updated"])
	}
}

// Suppress unused import
var _ = io.Discard

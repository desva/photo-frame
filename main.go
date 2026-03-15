package main

import (
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

//go:embed static/*
var staticFS embed.FS

type Config struct {
	Banner           string `json:"banner"`
	SlideshowSeconds int    `json:"slideshow_seconds"`
	Enabled          bool   `json:"enabled"`
	LastUpdated      string `json:"last_updated"`
	UpdatedBy        string `json:"updated_by"`
}

type Photo struct {
	Path    string   `json:"path"`
	Caption string   `json:"caption"`
	People  []string `json:"people"`
}

type SyncPayload struct {
	Photos []SyncPhoto `json:"photos"`
	Config *Config     `json:"config,omitempty"`
}

type SyncPhoto struct {
	Path    string   `json:"path"`
	Caption string   `json:"caption"`
	People  []string `json:"people"`
	Delete  bool     `json:"delete,omitempty"`
}

type App struct {
	db        *sql.DB
	config    Config
	configMu  sync.RWMutex
	photoDir  string
	configDir string
	syncToken string
}

func main() {
	port := "8085"
	photoDir := "./photos"
	configDir := "./data"
	syncToken := os.Getenv("SYNC_TOKEN")

	if p := os.Getenv("PORT"); p != "" {
		port = p
	}
	if d := os.Getenv("PHOTO_DIR"); d != "" {
		photoDir = d
	}
	if d := os.Getenv("CONFIG_DIR"); d != "" {
		configDir = d
	}
	if syncToken == "" {
		syncToken = "changeme"
	}

	os.MkdirAll(photoDir, 0755)
	os.MkdirAll(configDir, 0755)

	dbPath := filepath.Join(configDir, "frame.db")
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}

	app := &App{
		db:        db,
		photoDir:  photoDir,
		configDir: configDir,
		syncToken: syncToken,
	}
	app.initDB()
	app.loadConfig()

	mux := http.NewServeMux()

	// Main pages
	mux.HandleFunc("/", app.handleFrame)
	mux.HandleFunc("/settings", app.handleSettings)

	// APIs
	mux.HandleFunc("/api/config", app.handleConfig)
	mux.HandleFunc("/api/photos", app.handlePhotos)
	mux.HandleFunc("/api/image/", app.handleImage)
	mux.HandleFunc("/api/sync", app.handleSync)
	mux.HandleFunc("/api/sync/photo", app.handleSyncPhoto)
	mux.HandleFunc("/api/health", app.handleHealth)

	log.Printf("Photo Frame starting on :%s", port)
	log.Printf("  Photo dir: %s", photoDir)
	log.Printf("  Config dir: %s", configDir)
	log.Printf("  Sync token: %s...", syncToken[:min(4, len(syncToken))])
	log.Fatal(http.ListenAndServe(":"+port, mux))
}

func (a *App) initDB() {
	_, err := a.db.Exec(`
		CREATE TABLE IF NOT EXISTS photos (
			path TEXT PRIMARY KEY,
			caption TEXT DEFAULT '',
			updated_at TEXT DEFAULT (datetime('now'))
		);
		CREATE TABLE IF NOT EXISTS people_tags (
			photo_path TEXT NOT NULL,
			person_name TEXT NOT NULL,
			PRIMARY KEY (photo_path, person_name)
		);
		CREATE INDEX IF NOT EXISTS idx_people_photo ON people_tags(photo_path);
		CREATE TABLE IF NOT EXISTS config (
			key TEXT PRIMARY KEY,
			value TEXT
		);
	`)
	if err != nil {
		log.Fatalf("Failed to init database: %v", err)
	}
}

func (a *App) loadConfig() {
	a.config = Config{
		Banner:           "",
		SlideshowSeconds: 20,
		Enabled:          true,
		LastUpdated:      time.Now().UTC().Format(time.RFC3339),
		UpdatedBy:        "default",
	}
	rows, err := a.db.Query("SELECT key, value FROM config")
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		rows.Scan(&k, &v)
		switch k {
		case "banner":
			a.config.Banner = v
		case "slideshow_seconds":
			fmt.Sscanf(v, "%d", &a.config.SlideshowSeconds)
		case "enabled":
			a.config.Enabled = v == "true"
		case "last_updated":
			a.config.LastUpdated = v
		case "updated_by":
			a.config.UpdatedBy = v
		}
	}
}

func (a *App) saveConfig() {
	a.config.LastUpdated = time.Now().UTC().Format(time.RFC3339)
	a.db.Exec("INSERT OR REPLACE INTO config(key,value) VALUES('banner',?)", a.config.Banner)
	a.db.Exec("INSERT OR REPLACE INTO config(key,value) VALUES('slideshow_seconds',?)", fmt.Sprintf("%d", a.config.SlideshowSeconds))
	a.db.Exec("INSERT OR REPLACE INTO config(key,value) VALUES('enabled',?)", fmt.Sprintf("%v", a.config.Enabled))
	a.db.Exec("INSERT OR REPLACE INTO config(key,value) VALUES('last_updated',?)", a.config.LastUpdated)
	a.db.Exec("INSERT OR REPLACE INTO config(key,value) VALUES('updated_by',?)", a.config.UpdatedBy)
}

func (a *App) handleFrame(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := staticFS.ReadFile("static/frame.html")
	if err != nil {
		http.Error(w, "Internal error", 500)
		return
	}
	a.configMu.RLock()
	seconds := a.config.SlideshowSeconds
	banner := a.config.Banner
	a.configMu.RUnlock()

	html := strings.ReplaceAll(string(data), "{{SECONDS}}", fmt.Sprintf("%d", seconds))
	html = strings.ReplaceAll(html, "{{BANNER}}", banner)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write([]byte(html))
}

func (a *App) handleSettings(w http.ResponseWriter, r *http.Request) {
	data, err := staticFS.ReadFile("static/settings.html")
	if err != nil {
		http.Error(w, "Internal error", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(data)
}

func (a *App) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		a.configMu.RLock()
		defer a.configMu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(a.config)
	} else if r.Method == "PUT" {
		var req Config
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"bad json"}`, 400)
			return
		}
		a.configMu.Lock()
		if req.Banner != "" || r.URL.Query().Get("clear_banner") == "true" {
			a.config.Banner = req.Banner
		}
		if req.SlideshowSeconds > 0 {
			a.config.SlideshowSeconds = req.SlideshowSeconds
		}
		a.config.Enabled = req.Enabled
		a.config.UpdatedBy = req.UpdatedBy
		if a.config.UpdatedBy == "" {
			a.config.UpdatedBy = "admin"
		}
		a.saveConfig()
		a.configMu.Unlock()

		a.configMu.RLock()
		defer a.configMu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "config": a.config})
	} else {
		http.Error(w, "Method not allowed", 405)
	}
}

func (a *App) handlePhotos(w http.ResponseWriter, r *http.Request) {
	countStr := r.URL.Query().Get("count")
	count := 20
	if countStr != "" {
		fmt.Sscanf(countStr, "%d", &count)
	}
	if count < 1 {
		count = 1
	}
	if count > 50 {
		count = 50
	}

	rows, err := a.db.Query("SELECT path, caption FROM photos ORDER BY RANDOM() LIMIT ?", count)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"photos": []Photo{}})
		return
	}
	defer rows.Close()

	var photos []Photo
	for rows.Next() {
		var p Photo
		rows.Scan(&p.Path, &p.Caption)
		// Get people tags
		tagRows, err := a.db.Query("SELECT DISTINCT person_name FROM people_tags WHERE photo_path = ?", p.Path)
		if err == nil {
			for tagRows.Next() {
				var name string
				tagRows.Scan(&name)
				p.People = append(p.People, name)
			}
			tagRows.Close()
		}
		photos = append(photos, p)
	}

	if photos == nil {
		photos = []Photo{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"photos": photos})
}

func (a *App) handleImage(w http.ResponseWriter, r *http.Request) {
	// Path is /api/image/{relative-path}
	relPath := strings.TrimPrefix(r.URL.Path, "/api/image/")
	if relPath == "" {
		http.Error(w, "No path specified", 400)
		return
	}

	// Security: clean the path to prevent traversal
	cleanPath := filepath.Clean(relPath)
	if strings.Contains(cleanPath, "..") {
		http.Error(w, "Forbidden", 403)
		return
	}

	fullPath := filepath.Join(a.photoDir, cleanPath)
	if !strings.HasPrefix(fullPath, a.photoDir) {
		http.Error(w, "Forbidden", 403)
		return
	}

	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		http.Error(w, "Not found", 404)
		return
	}

	ext := strings.ToLower(filepath.Ext(fullPath))
	mimeTypes := map[string]string{
		".jpg": "image/jpeg", ".jpeg": "image/jpeg",
		".png": "image/png", ".gif": "image/gif",
		".bmp": "image/bmp", ".webp": "image/webp",
	}
	contentType := mimeTypes[ext]
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFile(w, r, fullPath)
}

func (a *App) handleSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", 405)
		return
	}

	token := r.Header.Get("X-Sync-Token")
	if token != a.syncToken {
		http.Error(w, "Unauthorized", 401)
		return
	}

	var payload SyncPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, `{"error":"bad json"}`, 400)
		return
	}

	added, updated, deleted := 0, 0, 0

	tx, err := a.db.Begin()
	if err != nil {
		http.Error(w, `{"error":"db error"}`, 500)
		return
	}

	for _, p := range payload.Photos {
		if p.Delete {
			tx.Exec("DELETE FROM photos WHERE path = ?", p.Path)
			tx.Exec("DELETE FROM people_tags WHERE photo_path = ?", p.Path)
			// Remove photo file
			os.Remove(filepath.Join(a.photoDir, p.Path))
			deleted++
			continue
		}

		var exists int
		tx.QueryRow("SELECT COUNT(*) FROM photos WHERE path = ?", p.Path).Scan(&exists)
		if exists > 0 {
			tx.Exec("UPDATE photos SET caption = ?, updated_at = datetime('now') WHERE path = ?", p.Caption, p.Path)
			updated++
		} else {
			tx.Exec("INSERT INTO photos(path, caption) VALUES(?, ?)", p.Path, p.Caption)
			added++
		}

		// Update people tags
		tx.Exec("DELETE FROM people_tags WHERE photo_path = ?", p.Path)
		for _, name := range p.People {
			tx.Exec("INSERT OR IGNORE INTO people_tags(photo_path, person_name) VALUES(?, ?)", p.Path, name)
		}
	}

	tx.Commit()

	// Update config if provided
	if payload.Config != nil {
		a.configMu.Lock()
		if payload.Config.Banner != "" {
			a.config.Banner = payload.Config.Banner
		}
		if payload.Config.SlideshowSeconds > 0 {
			a.config.SlideshowSeconds = payload.Config.SlideshowSeconds
		}
		a.config.UpdatedBy = "sync"
		a.saveConfig()
		a.configMu.Unlock()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":      true,
		"added":   added,
		"updated": updated,
		"deleted": deleted,
	})
}

func (a *App) handleSyncPhoto(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", 405)
		return
	}

	token := r.Header.Get("X-Sync-Token")
	if token != a.syncToken {
		http.Error(w, "Unauthorized", 401)
		return
	}

	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		http.Error(w, "Missing path parameter", 400)
		return
	}

	cleanPath := filepath.Clean(relPath)
	if strings.Contains(cleanPath, "..") {
		http.Error(w, "Forbidden", 403)
		return
	}

	fullPath := filepath.Join(a.photoDir, cleanPath)
	os.MkdirAll(filepath.Dir(fullPath), 0755)

	f, err := os.Create(fullPath)
	if err != nil {
		http.Error(w, "Failed to create file", 500)
		return
	}
	defer f.Close()

	written, err := io.Copy(f, r.Body)
	if err != nil {
		http.Error(w, "Failed to write file", 500)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":    true,
		"path":  cleanPath,
		"bytes": written,
	})
}

func (a *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	var photoCount int
	a.db.QueryRow("SELECT COUNT(*) FROM photos").Scan(&photoCount)

	a.configMu.RLock()
	defer a.configMu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":    "ok",
		"photos":    photoCount,
		"banner":    a.config.Banner,
		"slideshow": a.config.SlideshowSeconds,
		"uptime":    time.Since(startTime).String(),
	})
}

var startTime = time.Now()

// Suppress unused import warning for rand
var _ = rand.Int

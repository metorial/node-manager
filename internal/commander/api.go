package commander

import (
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strconv"
)

//go:embed web/static/*
var staticFiles embed.FS

type API struct {
	db     *DB
	server *Server
}

func NewAPI(db *DB, server *Server) *API {
	return &API{db: db, server: server}
}

func (api *API) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/hosts", api.handleHosts)
	mux.HandleFunc("/api/v1/hosts/", api.handleHostOrTags)
	mux.HandleFunc("/api/v1/stats", api.handleStats)
	mux.HandleFunc("/api/v1/health", api.handleHealth)
	mux.HandleFunc("/api/v1/tags", api.handleTags)
	mux.HandleFunc("/", api.handleUI)
}

func (api *API) handleHosts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	hosts, err := api.db.GetAllHosts()
	if err != nil {
		log.Printf("Error getting hosts: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"hosts": hosts,
		"count": len(hosts),
	})
}

func (api *API) handleHostOrTags(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path[len("/api/v1/hosts/"):]

	if path == "tags" {
		api.handleHostTags(w, r)
		return
	}

	api.handleHost(w, r)
}

func (api *API) handleHost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	hostname := r.URL.Path[len("/api/v1/hosts/"):]
	if hostname == "" || hostname == "tags" {
		http.Error(w, "Hostname required", http.StatusBadRequest)
		return
	}

	host, err := api.db.GetHost(hostname)
	if err == sql.ErrNoRows {
		http.Error(w, "Host not found", http.StatusNotFound)
		return
	}
	if err != nil {
		log.Printf("Error getting host %s: %v", hostname, err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	limit := 100
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 1000 {
			limit = l
		}
	}

	usage, err := api.db.GetHostUsage(hostname, limit)
	if err != nil {
		log.Printf("Error getting usage for %s: %v", hostname, err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	tags, err := api.db.GetHostTags(hostname)
	if err != nil {
		log.Printf("Error getting tags for %s: %v", hostname, err)
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"host":  host,
		"usage": usage,
		"tags":  tags,
	})
}

func (api *API) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	stats, err := api.db.GetClusterStats()
	if err != nil {
		log.Printf("Error getting cluster stats: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	respondJSON(w, http.StatusOK, stats)
}

func (api *API) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := api.db.conn.Ping(); err != nil {
		http.Error(w, fmt.Sprintf("Database unhealthy: %v", err), http.StatusServiceUnavailable)
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"status":   "healthy",
		"database": "connected",
	})
}

func respondJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("Error encoding JSON response: %v", err)
	}
}

func (api *API) handleTags(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	tags, err := api.db.GetAllTags()
	if err != nil {
		log.Printf("Error getting tags: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"tags":  tags,
		"count": len(tags),
	})
}

func (api *API) handleHostTags(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Hostname string `json:"hostname"`
		Tag      string `json:"tag"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Hostname == "" || req.Tag == "" {
		http.Error(w, "Hostname and tag are required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodPost:
		if err := api.db.AddHostTag(req.Hostname, req.Tag); err != nil {
			log.Printf("Error adding tag to host: %v", err)
			http.Error(w, "Failed to add tag", http.StatusInternalServerError)
			return
		}
		respondJSON(w, http.StatusOK, map[string]string{
			"message": "Tag added successfully",
		})

	case http.MethodDelete:
		if err := api.db.RemoveHostTag(req.Hostname, req.Tag); err != nil {
			log.Printf("Error removing tag from host: %v", err)
			http.Error(w, "Failed to remove tag", http.StatusInternalServerError)
			return
		}
		respondJSON(w, http.StatusOK, map[string]string{
			"message": "Tag removed successfully",
		})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (api *API) handleUI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	content, err := fs.ReadFile(staticFiles, "web/static/index.html")
	if err != nil {
		log.Printf("Error reading embedded file: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write(content)
}

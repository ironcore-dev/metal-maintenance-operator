// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package mock

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
)

var (
	//go:embed data/**
	dataFS embed.FS

	// odataKeyRegex converts OData key expressions to path segments:
	// Baselines(20) → Baselines/20, Sessions('abc') → Sessions/abc, Jobs(100) → Jobs/100.
	odataKeyRegex = regexp.MustCompile(`(\w+)\(\'?([^)']+)\'?\)`)
)

type MockServer struct {
	log       logr.Logger
	addr      string
	handler   http.Handler
	mu        sync.RWMutex
	overrides map[string]any
}

func NewMockServer(log logr.Logger, addr string) *MockServer {
	mux := http.NewServeMux()
	server := &MockServer{
		addr:      addr,
		log:       log,
		overrides: make(map[string]any),
	}

	mux.HandleFunc("/", server.consolehHandler)
	server.handler = mux

	return server
}

func (s *MockServer) consolehHandler(w http.ResponseWriter, r *http.Request) {
	s.log.Info("Received request", "method", r.Method, "path", r.URL.Path)

	switch r.Method {
	case http.MethodGet:
		s.handleRedfishGET(w, r)
	case http.MethodPost:
		s.handleRedfishPOST(w, r)
	case http.MethodPatch:
		s.handleConsolePATCH(w, r)
	case http.MethodPut:
		s.handleConsolePUT(w, r)
	case http.MethodDelete:
		s.handleConsoleDELETE(w, r)
	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

func (s *MockServer) handleRedfishPOST(w http.ResponseWriter, r *http.Request) {
	urlPath := resolvePath(r.URL.Path)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Invalid body", http.StatusBadRequest)
		return
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			s.log.Error(err, "Failed to close request body")
		}
	}(r.Body)

	// Action endpoints (paths containing /Actions/) return 204 with no body.
	if strings.Contains(r.URL.Path, "/Actions/") {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	var update map[string]any
	if err := json.Unmarshal(body, &update); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	s.log.Info("POST body received", "body", string(body))
	s.mu.RLock()
	cached, hasOverride := s.overrides[urlPath]
	s.mu.RUnlock()
	if hasOverride {
		resp, _ := json.MarshalIndent(cached, "", "  ")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		if _, err := w.Write(resp); err != nil {
			s.log.Error(err, "Failed to write response")
		}
		return
	}

	s.log.Info("Using embedded data for POST", "path", urlPath)
	// Prefer a post.json sidecar so POST responses don't corrupt the GET (index.json) data.
	postPath := strings.TrimSuffix(urlPath, "index.json") + "post.json"
	postData, postErr := dataFS.ReadFile(postPath)
	if postErr == nil {
		var postBody map[string]any
		if err := json.Unmarshal(postData, &postBody); err != nil {
			http.Error(w, "Invalid JSON in post.json", http.StatusInternalServerError)
			return
		}
		resp, _ := json.MarshalIndent(postBody, "", "  ")
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/SessionService/Sessions") {
			if token, ok := postBody["Token"].(string); ok {
				w.Header().Set("X-Auth-Token", token)
			}
		}
		w.WriteHeader(http.StatusCreated)
		if _, err := w.Write(resp); err != nil {
			s.log.Error(err, "Failed to write response")
		}
		return
	}

	data, err := dataFS.ReadFile(urlPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var existing map[string]any
	if err := json.Unmarshal(data, &existing); err != nil {
		http.Error(w, "Invalid JSON in embedded data", http.StatusInternalServerError)
		return
	}
	maps.Copy(existing, update)
	s.mu.Lock()
	s.overrides[urlPath] = existing
	s.mu.Unlock()

	resp, _ := json.MarshalIndent(existing, "", "  ")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	if _, err = w.Write(resp); err != nil {
		s.log.Error(err, "Failed to write response")
	}
}

func (s *MockServer) handleRedfishGET(w http.ResponseWriter, r *http.Request) {
	urlPath := resolvePath(r.URL.Path)

	s.mu.RLock()
	cached, hasOverride := s.overrides[urlPath]
	s.mu.RUnlock()

	var content []byte
	if hasOverride {
		content, _ = json.Marshal(cached)
	} else {
		var err error
		content, err = dataFS.ReadFile(urlPath)
		if err != nil {
			http.NotFound(w, r)
			return
		}
	}

	// Apply simple Identifier= query filter for the Devices endpoint.
	if strings.Contains(r.URL.Path, "/DeviceService/Devices") {
		if identifiers := r.URL.Query()["Identifier"]; len(identifiers) > 0 {
			content = filterDevicesByIdentifier(content, identifiers)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(content); err != nil {
		s.log.Error(err, "Failed to write response")
	}
}

func filterDevicesByIdentifier(data []byte, identifiers []string) []byte {
	var list struct {
		ODataContext string           `json:"@odata.context,omitempty"`
		ODataCount   int              `json:"@odata.count"`
		Value        []map[string]any `json:"value"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return data
	}
	idSet := make(map[string]bool, len(identifiers))
	for _, id := range identifiers {
		idSet[id] = true
	}
	filtered := list.Value[:0]
	for _, dev := range list.Value {
		if id, ok := dev["Identifier"].(string); ok && idSet[id] {
			filtered = append(filtered, dev)
		}
	}
	list.Value = filtered
	list.ODataCount = len(filtered)
	out, _ := json.Marshal(list)
	return out
}

func (s *MockServer) handleConsolePATCH(w http.ResponseWriter, r *http.Request) {
	s.log.Info("PATCH request received", "path", r.URL.Path)
	w.WriteHeader(http.StatusNotImplemented)
	if _, err := w.Write([]byte("PATCH not implemented")); err != nil {
		s.log.Error(err, "Failed to write response")
	}
}

func (s *MockServer) handleConsolePUT(w http.ResponseWriter, r *http.Request) {
	urlPath := resolvePath(r.URL.Path)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Invalid body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close() //nolint:errcheck

	var update map[string]any
	if err := json.Unmarshal(body, &update); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Merge onto existing data so fields not present in the PUT body (e.g. TaskId) are preserved.
	existing := map[string]any{}
	s.mu.RLock()
	if cached, ok := s.overrides[urlPath]; ok {
		if m, ok := cached.(map[string]any); ok {
			for k, v := range m {
				existing[k] = v
			}
		}
	}
	s.mu.RUnlock()
	if len(existing) == 0 {
		if data, err := dataFS.ReadFile(urlPath); err == nil {
			_ = json.Unmarshal(data, &existing)
		}
	}
	maps.Copy(existing, update)

	s.mu.Lock()
	s.overrides[urlPath] = existing
	s.mu.Unlock()

	resp, _ := json.MarshalIndent(existing, "", "  ")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(resp); err != nil {
		s.log.Error(err, "Failed to write response")
	}
}

func (s *MockServer) handleConsoleDELETE(w http.ResponseWriter, r *http.Request) {
	urlPath := resolvePath(r.URL.Path)
	s.mu.Lock()
	delete(s.overrides, urlPath)
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// SetOverride sets a fixed response body for a given URL path (used by tests).
func (s *MockServer) SetOverride(urlPath string, value any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.overrides[urlPath] = value
}

// ClearOverride removes a previously set override.
func (s *MockServer) ClearOverride(urlPath string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.overrides, urlPath)
}

func (s *MockServer) Start(ctx context.Context) error {
	if s.handler == nil {
		return fmt.Errorf("mock redfish handler is nil")
	}
	srv := &http.Server{
		Addr:    s.addr,
		Handler: s.handler,
	}
	done := make(chan struct{})
	go func() {
		s.log.Info("Started mock server", "address", s.addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Error(err, "Server failed")
		}
		close(done)
	}()

	<-ctx.Done()
	s.log.Info("Shutting down mock server")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		s.log.Error(err, "Mock server shutdown failed")
	}
	return nil
}

func resolvePath(urlPath string) string {
	if urlPath == "/" {
		return "data/index.json"
	}
	if after, found := strings.CutPrefix(urlPath, "/api"); found {
		after = strings.Trim(after, "/")
		// Strip OData key expressions like Baselines(20) → Baselines,
		// Sessions('abc') → Sessions, Jobs(100) → Jobs.
		after = odataKeyRegex.ReplaceAllString(after, "$1/$2")
		return path.Join("data", "dell", after, "index.json")
	}
	return "data/index.json"
}

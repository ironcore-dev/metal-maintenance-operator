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
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
)

var (
	//go:embed data/**
	dataFS embed.FS
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
		_, err := w.Write(resp)
		if err != nil {
			s.log.Error(err, "Failed to write response")
		}
		return
	} else {
		s.log.Info("Using embedded data for POST", "path", urlPath)
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
		for k, v := range update {
			existing[k] = v
		}
		s.mu.Lock()
		s.overrides[urlPath] = existing
		s.mu.Unlock()
		resp, _ := json.MarshalIndent(existing, "", "  ")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, err = w.Write(resp)
		if err != nil {
			s.log.Error(err, "Failed to write response")
		}
		return
	}
}

func (s *MockServer) handleRedfishGET(w http.ResponseWriter, r *http.Request) {
	urlPath := resolvePath(r.URL.Path)

	s.mu.RLock()
	cached, hasOverride := s.overrides[urlPath]
	s.mu.RUnlock()

	if hasOverride {
		resp, _ := json.MarshalIndent(cached, "", "  ")
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write(resp)
		if err != nil {
			s.log.Error(err, "Failed to write response")
		}
		return
	}

	content, err := dataFS.ReadFile(urlPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, err = w.Write(content)
	if err != nil {
		s.log.Error(err, "Failed to write response")
	}
}

func (s *MockServer) handleConsolePATCH(w http.ResponseWriter, r *http.Request) {
	// Implement your PATCH handling logic here
	w.WriteHeader(http.StatusNotImplemented)
	w.Write([]byte("PATCH not implemented"))
}

func (s *MockServer) handleConsoleDELETE(w http.ResponseWriter, r *http.Request) {
	// Implement your DELETE handling logic here
	w.WriteHeader(http.StatusNotImplemented)
	w.Write([]byte("DELETE not implemented"))
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
	trimmed := ""
	if strings.HasPrefix(urlPath, "/api") {
		trimmed := strings.TrimPrefix(urlPath, "/api")
		trimmed = strings.Trim(trimmed, "/")
		// add dell path
		return path.Join("data", "dell", trimmed, "index.json")
	}

	return path.Join("data", trimmed, "index.json")
}

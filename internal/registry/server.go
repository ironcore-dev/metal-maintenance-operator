// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

// Package registry implements the HTTP endpoint the metalprobe agent posts
// discovery data to, plus the in-memory store the ServerDiscovery reconciler
// reads from. Store and reconciler live in the same process, so no HTTP
// loopback is needed to consume registrations.
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-logr/logr"

	discoveryv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/discovery/v1alpha1"
)

const DefaultStaleEntryTTL = 10 * time.Minute

// RegistryServer serves the /register endpoint and holds the registered discovery data.
type RegistryServer struct {
	addr string
	mux  *http.ServeMux
	log  logr.Logger

	mu   sync.RWMutex
	data map[string]discoveryv1alpha1.Metadata

	staleEntryTTL time.Duration
}

// NewServer initializes and returns a new Server instance.
func NewServer(logger logr.Logger, addr string) *RegistryServer {
	server := &RegistryServer{
		addr:          addr,
		mux:           http.NewServeMux(),
		log:           logger,
		data:          map[string]discoveryv1alpha1.Metadata{},
		staleEntryTTL: DefaultStaleEntryTTL,
	}
	server.mux.HandleFunc("POST /register", server.registerHandler)
	return server
}

func (s *RegistryServer) registerHandler(w http.ResponseWriter, r *http.Request) {
	var reg RegistrationPayload
	if err := json.NewDecoder(r.Body).Decode(&reg); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if reg.SystemUUID == "" {
		http.Error(w, "systemUUID must not be empty", http.StatusBadRequest)
		return
	}

	s.Store(reg.SystemUUID, reg.Data)
	s.log.Info("Registered system UUID", "uuid", reg.SystemUUID)
	w.WriteHeader(http.StatusCreated)
}

// Store puts discovery data for the given system UUID into the store.
func (s *RegistryServer) Store(systemUUID string, data discoveryv1alpha1.Metadata) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[systemUUID] = data
}

// Load returns the discovery data registered for the given system UUID.
func (s *RegistryServer) Load(systemUUID string) (discoveryv1alpha1.Metadata, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, ok := s.data[systemUUID]
	return data, ok
}

// Delete removes the discovery data for the given system UUID.
func (s *RegistryServer) Delete(systemUUID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, systemUUID)
}

// sweep removes entries whose payload timestamp is older than staleEntryTTL.
func (s *RegistryServer) sweep() {
	cutoff := time.Now().Add(-s.staleEntryTTL)
	s.mu.Lock()
	defer s.mu.Unlock()
	for uuid, data := range s.data {
		if data.Timestamp == nil || data.Timestamp.Time.Before(cutoff) {
			delete(s.data, uuid)
			s.log.V(1).Info("Swept stale registry entry", "uuid", uuid)
		}
	}
}

// Start starts the HTTP server and shuts it down when ctx is done.
func (s *RegistryServer) Start(ctx context.Context) error {
	s.log.Info("Starting registry server", "address", s.addr)
	srv := &http.Server{Addr: s.addr, Handler: s.mux, ReadHeaderTimeout: 10 * time.Second}

	res := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			res <- fmt.Errorf("HTTP registry server ListenAndServe: %w", err)
			return
		}
		res <- nil
	}()

	ticker := time.NewTicker(s.staleEntryTTL)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.log.Info("Shutting down registry server")
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdownCtx)
			return <-res
		case err := <-res:
			return err
		case <-ticker.C:
			s.sweep()
		}
	}
}

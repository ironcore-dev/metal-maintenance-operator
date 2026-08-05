// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package mock

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"embed"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/big"
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
		if strings.Contains(urlPath, "SessionService/Sessions") {
			w.Header().Set("X-Auth-Token", "mock-token")
		}
		w.WriteHeader(http.StatusCreated)
		_, err := w.Write(resp)
		if err != nil {
			s.log.Error(err, "Failed to write response")
		}
		return
	}
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
	maps.Copy(existing, update)
	s.mu.Lock()
	s.overrides[urlPath] = existing
	s.mu.Unlock()
	resp, _ := json.MarshalIndent(existing, "", "  ")
	w.Header().Set("Content-Type", "application/json")
	if strings.Contains(urlPath, "SessionService/Sessions") {
		w.Header().Set("X-Auth-Token", "mock-token")
	}
	w.WriteHeader(http.StatusCreated)
	_, err = w.Write(resp)
	if err != nil {
		s.log.Error(err, "Failed to write response")
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
	s.log.Info("PATCH request received", "path", r.URL.Path)
	w.WriteHeader(http.StatusNotImplemented)
	if _, err := w.Write([]byte("PATCH not implemented")); err != nil {
		s.log.Error(err, "Failed to write response")
	}
}

func (s *MockServer) handleConsoleDELETE(w http.ResponseWriter, r *http.Request) {
	// Implement your DELETE handling logic here
	s.log.Info("DELETE request received", "path", r.URL.Path)
	w.WriteHeader(http.StatusNotImplemented)
	if _, err := w.Write([]byte("DELETE not implemented")); err != nil {
		s.log.Error(err, "Failed to write response")
	}
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

// StartTLS starts a TLS server on addr using a self-signed certificate.
// It is intended for testing InsecureSkipVerify behaviour.
func (s *MockServer) StartTLS(ctx context.Context, addr string) error {
	if s.handler == nil {
		return fmt.Errorf("mock redfish handler is nil")
	}
	cert, err := generateSelfSignedCert()
	if err != nil {
		return fmt.Errorf("failed to generate self-signed cert: %w", err)
	}
	srv := &http.Server{
		Addr:    addr,
		Handler: s.handler,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		},
	}
	go func() {
		s.log.Info("Started mock TLS server", "address", addr)
		if err := srv.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Error(err, "TLS server failed")
		}
	}()

	<-ctx.Done()
	s.log.Info("Shutting down mock TLS server")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		s.log.Error(err, "Mock TLS server shutdown failed")
	}
	return nil
}

func generateSelfSignedCert() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "mock-console"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(24 * time.Hour),
		DNSNames:     []string{"localhost"},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return tls.X509KeyPair(certPEM, keyPEM)
}

func resolvePath(urlPath string) string {
	if urlPath == "/" {
		return "data/index.json"
	}
	if after, found := strings.CutPrefix(urlPath, "/api"); found {
		after = strings.Trim(after, "/")
		return path.Join("data", "dell", after, "index.json")
	}

	return "data/index.json"
}

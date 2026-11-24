package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/adrijshikhar/actr/internal/vault"
)

type setSecretRequest struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Scope string `json:"scope"`
}

type secretResponse struct {
	Status string `json:"status"`
	Key    string `json:"key"`
}

// handleSecretsList returns a JSON list of metadata for all secrets in the vault.
// It never exposes secret values. Returns 503 if SecretManager is not configured.
func (s *Server) handleSecretsList(w http.ResponseWriter, r *http.Request) {
	if s.cfg.SecretManager == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "secret manager not configured")
		return
	}

	secrets, err := s.cfg.SecretManager.ListSecrets(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if secrets == nil {
		secrets = []vault.SecretMetadata{}
	}

	writeJSON(w, http.StatusOK, secrets)
}

// handleSecretSet creates or updates a secret in the vault.
// Validates key and value non-empty, and returns 201 on success.
func (s *Server) handleSecretSet(w http.ResponseWriter, r *http.Request) {
	if s.cfg.SecretManager == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "secret manager not configured")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	var req setSecretRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req.Key = strings.TrimSpace(req.Key)
	if req.Key == "" {
		writeJSONError(w, http.StatusBadRequest, "secret key is required")
		return
	}
	if req.Value == "" {
		writeJSONError(w, http.StatusBadRequest, "secret value is required")
		return
	}
	req.Scope = strings.TrimSpace(req.Scope)

	if err := s.cfg.SecretManager.SetSecret(r.Context(), req.Key, req.Value, req.Scope); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if s.cfg.Masker != nil {
		s.cfg.Masker.AddMask(req.Value)
	}

	writeJSON(w, http.StatusCreated, secretResponse{
		Status: "saved",
		Key:    req.Key,
	})
}

// handleSecretDelete removes a secret by key from the vault.
// Returns 200 on success, 404 if not found, 400 if key is empty.
func (s *Server) handleSecretDelete(w http.ResponseWriter, r *http.Request) {
	if s.cfg.SecretManager == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "secret manager not configured")
		return
	}

	key := strings.TrimSpace(r.PathValue("key"))
	if key == "" {
		writeJSONError(w, http.StatusBadRequest, "secret key is required")
		return
	}

	if err := s.cfg.SecretManager.DeleteSecret(r.Context(), key); err != nil {
		if errors.Is(err, vault.ErrSecretNotFound) {
			writeJSONError(w, http.StatusNotFound, "secret not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, secretResponse{
		Status: "deleted",
		Key:    key,
	})
}

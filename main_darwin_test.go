//go:build darwin

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMacHandlerConnectsAndReadsSettings(t *testing.T) {
	instrument := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok || username != "operator" || password != "secret" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		switch r.URL.Path {
		case "/api/status":
			w.WriteHeader(http.StatusOK)
		case "/api/settings":
			_, _ = w.Write([]byte(`{"analogOutputs":[{"high":100000,"log":false,"low":0,"source":"TCC"},{"high":100,"log":false,"low":0,"source":"HNAP"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer instrument.Close()

	body := `{"operation":"connect","address":"` + instrument.URL + `","username":"operator","password":"secret","timeoutSeconds":10}`
	request := httptest.NewRequest(http.MethodPost, "/test-token/operation", strings.NewReader(body))
	response := httptest.NewRecorder()

	newMacHandler("test-token", func() {}).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
	var result macOperationResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Status != http.StatusOK || len(result.Outputs) != 2 || !strings.Contains(result.Message, "Connected successfully") {
		t.Fatalf("result = %#v", result)
	}
}

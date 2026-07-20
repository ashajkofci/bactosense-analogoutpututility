//go:build darwin

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMacHandlerRunsConnectionTest(t *testing.T) {
	device := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if r.URL.Path != "/api/status" || !ok || username != "operator" || password != "secret" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer device.Close()

	body := `{"operation":"test","address":"` + device.URL + `","username":"operator","password":"secret","timeoutSeconds":10}`
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
	if result.Status != http.StatusOK || !strings.Contains(result.Message, "Connection successful") {
		t.Fatalf("result = %#v", result)
	}
}

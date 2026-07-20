package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNormalizeBaseURL(t *testing.T) {
	tests := map[string]string{
		"192.168.10.20":                   "http://192.168.10.20/api/",
		"192.168.10.20:8080":              "http://192.168.10.20:8080/api/",
		"https://device.local":            "https://device.local/api/",
		"https://device.local/api":        "https://device.local/api/",
		"https://device.local/custom/api": "https://device.local/custom/api/",
		"2001:db8::1":                     "http://[2001:db8::1]/api/",
	}
	for input, expected := range tests {
		actual, err := normalizeBaseURL(input)
		if err != nil {
			t.Fatalf("normalizeBaseURL(%q): %v", input, err)
		}
		if actual.String() != expected {
			t.Errorf("normalizeBaseURL(%q) = %q, want %q", input, actual.String(), expected)
		}
	}
}

func TestBuildAnalogOutputsPayloadContainsOnlyTargetField(t *testing.T) {
	desired := []AnalogOutput{
		{High: 20, Log: false, Low: 4, Source: "HNAP"},
		{High: 200, Log: true, Low: 10, Source: "LNAC"},
	}
	payload, err := buildAnalogOutputsPayload(desired)
	if err != nil {
		t.Fatal(err)
	}
	var result settingsDocument
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 {
		t.Fatalf("payload contains %d fields, want 1", len(result))
	}
	var actual []AnalogOutput
	if err := json.Unmarshal(result["analogOutputs"], &actual); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, desired) {
		t.Fatalf("analog outputs = %#v, want %#v", actual, desired)
	}
}

func TestClientReadUpdateAndVerify(t *testing.T) {
	const username = "operator"
	const password = "secret"

	var mu sync.Mutex
	settings := settingsDocument{
		"analogOutputs": json.RawMessage(`[{"high":20,"log":false,"low":4,"source":"TCC"},{"high":100,"log":true,"low":1,"source":"ICC"}]`),
		"unrelated":     json.RawMessage(`{"must":"remain","value":9007199254740993}`),
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != username || pass != password {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/status":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"state":"ready"}`))
		case "/api/settings":
			mu.Lock()
			defer mu.Unlock()
			if r.Method == http.MethodGet {
				_ = json.NewEncoder(w).Encode(settings)
				return
			}
			if r.Method == http.MethodPost {
				var incoming settingsDocument
				if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				if len(incoming) != 1 || incoming["analogOutputs"] == nil {
					http.Error(w, "request must contain only analogOutputs", http.StatusBadRequest)
					return
				}
				settings["analogOutputs"] = append(json.RawMessage(nil), incoming["analogOutputs"]...)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.WriteHeader(http.StatusMethodNotAllowed)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	base, err := normalizeBaseURL(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client, err := newAPIClient(ConnectionConfig{
		BaseURL:  base,
		Username: username,
		Password: password,
		Timeout:  3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	status, err := client.TestConnection(context.Background())
	if err != nil || status != http.StatusOK {
		t.Fatalf("TestConnection() = %d, %v", status, err)
	}

	initial, err := client.ReadAnalogOutputs(context.Background())
	if err != nil || len(initial) != 2 {
		t.Fatalf("ReadAnalogOutputs() = %#v, %v", initial, err)
	}

	desired := []AnalogOutput{
		{High: 50, Log: false, Low: 5, Source: "HNAC"},
		{High: 500, Log: true, Low: 0.5, Source: "LNAC"},
	}
	actual, err := client.UpdateAnalogOutputs(context.Background(), desired)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, desired) {
		t.Fatalf("actual = %#v, want %#v", actual, desired)
	}
}

func TestHTTPErrorIncludesStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
	}))
	defer server.Close()

	parsed, _ := url.Parse(server.URL + "/api/")
	client, err := newAPIClient(ConnectionConfig{
		BaseURL:  parsed,
		Username: "wrong",
		Password: "wrong",
		Timeout:  time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, err = client.ReadAnalogOutputs(context.Background())
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected 401 error, got %v", err)
	}
}

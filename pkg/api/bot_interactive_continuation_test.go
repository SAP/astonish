package api

import (
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBotContinueInteractiveRouteRequiresAuthentication(t *testing.T) {
	router := mux.NewRouter()
	registerBotRunRoutes(router)
	path := "/api/studio/bots/" + uuid.NewString() + "/runs/" + uuid.NewString() + "/continue-interactive"
	request := httptest.NewRequest(http.MethodPost, path, nil)
	var match mux.RouteMatch
	if !router.Match(request, &match) {
		t.Fatal("continuation route missing")
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code < 400 {
		t.Fatalf("anonymous continuation admitted: %d", response.Code)
	}
}

package control

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestHTTPClickHouseRestoreAdminValidateDatabaseRequestsSingleCheckResult(t *testing.T) {
	var checkQueries []string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			http.Error(response, err.Error(), http.StatusBadRequest)
			return
		}
		query := string(body)
		switch {
		case strings.HasPrefix(query, "SELECT engine "):
			_, _ = io.WriteString(response, "Atomic\n")
		case strings.HasPrefix(query, "SELECT count() "):
			_, _ = io.WriteString(response, "1\n")
		case strings.HasPrefix(query, "CHECK TABLE "):
			checkQueries = append(checkQueries, query)
			_, _ = io.WriteString(response, "1\n")
		default:
			http.Error(response, "unexpected query: "+query, http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)

	admin := HTTPClickHouseRestoreAdmin{Endpoint: server.URL, Client: server.Client()}
	if err := admin.ValidateDatabase(context.Background(), "restore_test"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"CHECK TABLE restore_test.cdn_access_logs SETTINGS check_query_single_value_result=1 FORMAT TSVRaw",
		"CHECK TABLE restore_test.cdn_site_minute SETTINGS check_query_single_value_result=1 FORMAT TSVRaw",
	}
	if !reflect.DeepEqual(checkQueries, want) {
		t.Fatalf("CHECK TABLE queries = %#v, want %#v", checkQueries, want)
	}
}

package edge

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestDecodeNginxLogIncludesRequestDetails(t *testing.T) {
	line := []byte(`{"request_id":"request-1","timestamp":"2026-07-18T10:20:30Z","site_id":"site-1","client_ip":"203.0.113.5","host":"cdn.example.test","scheme":"https","protocol":"HTTP/2.0","method":"GET","path":"/asset.js?token=secret","status":404,"request_bytes":512,"bytes":2048,"duration_seconds":0.037,"upstream":"192.0.2.10:443","upstream_status":"404","upstream_response_time":"0.036","cache_status":"MISS","user_agent":"test-agent","referer":"https://example.test/","content_type":"application/json","response_content_type":"text/javascript","accept":"*/*","range":"bytes=0-1023"}`)
	event, err := decodeNginxLog(line)
	if err != nil {
		t.Fatal(err)
	}
	if event.ID != "request-1" || event.Timestamp != time.Date(2026, 7, 18, 10, 20, 30, 0, time.UTC) || event.Path != "/asset.js" || event.RequestBytes != 512 || event.Bytes != 2048 || event.DurationMS != 37 {
		t.Fatalf("decoded core event = %#v", event)
	}
	if event.Host != "cdn.example.test" || event.Protocol != "HTTP/2.0" || event.UserAgent != "test-agent" || event.UpstreamStatus != "404" || event.ResponseContentType != "text/javascript" || event.Range != "bytes=0-1023" {
		t.Fatalf("decoded request details = %#v", event)
	}
}

func TestDecodeNginxLogGeneratesMissingRequestID(t *testing.T) {
	event, err := decodeNginxLog([]byte(`{"timestamp":"2026-07-18T10:20:30Z","duration_seconds":0}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := uuid.Parse(event.ID); err != nil {
		t.Fatalf("generated request ID %q is invalid: %v", event.ID, err)
	}
}

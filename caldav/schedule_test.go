package caldav

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/emersion/go-webdav/internal"
)

// scheduleTestBackend is a testBackend which also implements SchedulingBackend.
type scheduleTestBackend struct {
	testBackend

	defaultCalendar string
}

func (b scheduleTestBackend) ScheduleDefaultCalendarPath(ctx context.Context) (string, error) {
	return b.defaultCalendar, nil
}

const (
	testInboxPath  = "/user/calendars/inbox/"
	testOutboxPath = "/user/calendars/outbox/"
)

func newScheduleBackend(calendars ...Calendar) scheduleTestBackend {
	return scheduleTestBackend{
		testBackend:     testBackend{calendars: calendars},
		defaultCalendar: "/user/calendars/cal/",
	}
}

// serveDepth runs one request at the given Depth and returns the response with
// its body.
func serveDepth(t *testing.T, b Backend, method, path, depth, body string) (*http.Response, []byte) {
	t.Helper()

	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/xml")
	if depth != "" {
		req.Header.Set("Depth", depth)
	}
	w := httptest.NewRecorder()
	handler := Handler{Backend: b}
	handler.ServeHTTP(w, req)

	res := w.Result()
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res, data
}

func serveMultiStatus(t *testing.T, b Backend, method, path, depth, body string) *internal.MultiStatus {
	t.Helper()

	res, data := serveDepth(t, b, method, path, depth, body)
	if res.StatusCode != http.StatusMultiStatus {
		t.Fatalf("%v %v: got status %v, want 207:\n%s", method, path, res.StatusCode, data)
	}
	var ms internal.MultiStatus
	if err := xml.Unmarshal(data, &ms); err != nil {
		t.Fatalf("failed to unmarshal multistatus: %v:\n%s", err, data)
	}
	return &ms
}

func hrefOf(t *testing.T, raw *internal.RawXMLValue, v interface{ GetXMLName() xml.Name }) string {
	t.Helper()

	if err := raw.Decode(v); err != nil {
		t.Fatal(err)
	}
	switch p := v.(type) {
	case *scheduleInboxURL:
		return p.Href.Path
	case *scheduleOutboxURL:
		return p.Href.Path
	case *scheduleDefaultCalendarURL:
		return p.Href.Path
	}
	t.Fatalf("unexpected property type %T", v)
	return ""
}

var propFindScheduleURLs = `
<?xml version="1.0" encoding="UTF-8"?>
<A:propfind xmlns:A="DAV:" xmlns:B="urn:ietf:params:xml:ns:caldav">
  <A:prop>
    <B:schedule-inbox-URL/>
    <B:schedule-outbox-URL/>
  </A:prop>
</A:propfind>
`

func TestPropFindSchedulePrincipalURLs(t *testing.T) {
	for _, tc := range []struct {
		name      string
		backend   Backend
		wantFound bool
	}{
		{"plain", testBackend{}, false},
		{"scheduling", newScheduleBackend(), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ms := serveMultiStatus(t, tc.backend, "PROPFIND", "/user/", "0", propFindScheduleURLs)
			if len(ms.Responses) != 1 {
				t.Fatalf("got %v responses, want 1", len(ms.Responses))
			}
			resp := &ms.Responses[0]

			inRaw, inCode := findProp(t, resp, scheduleInboxURLName)
			outRaw, outCode := findProp(t, resp, scheduleOutboxURLName)
			if inRaw == nil || outRaw == nil {
				t.Fatalf("missing schedule-inbox-URL or schedule-outbox-URL")
			}
			if !tc.wantFound {
				if inCode != http.StatusNotFound || outCode != http.StatusNotFound {
					t.Errorf("got status %v/%v, want 404/404", inCode, outCode)
				}
				return
			}
			if inCode != http.StatusOK || outCode != http.StatusOK {
				t.Fatalf("got status %v/%v, want 200/200", inCode, outCode)
			}
			if got := hrefOf(t, inRaw, &scheduleInboxURL{}); got != testInboxPath {
				t.Errorf("schedule-inbox-URL: got %q, want %q", got, testInboxPath)
			}
			if got := hrefOf(t, outRaw, &scheduleOutboxURL{}); got != testOutboxPath {
				t.Errorf("schedule-outbox-URL: got %q, want %q", got, testOutboxPath)
			}
		})
	}
}

var propFindScheduleCollection = `
<?xml version="1.0" encoding="UTF-8"?>
<A:propfind xmlns:A="DAV:" xmlns:B="urn:ietf:params:xml:ns:caldav" xmlns:C="http://calendarserver.org/ns/">
  <A:prop>
    <A:resourcetype/>
    <A:displayname/>
    <C:getctag/>
    <B:schedule-default-calendar-URL/>
    <B:schedule-calendar-transp/>
    <B:max-attendees-per-instance/>
  </A:prop>
</A:propfind>
`

func TestPropFindScheduleInbox(t *testing.T) {
	b := newScheduleBackend()
	ms := serveMultiStatus(t, b, "PROPFIND", testInboxPath, "0", propFindScheduleCollection)
	if len(ms.Responses) != 1 {
		t.Fatalf("got %v responses, want 1", len(ms.Responses))
	}
	resp := &ms.Responses[0]

	if href, err := resp.Path(); err != nil || href != testInboxPath {
		t.Errorf("got href %q (err %v), want %q", href, err, testInboxPath)
	}

	raw, code := findProp(t, resp, internal.ResourceTypeName)
	if raw == nil || code != http.StatusOK {
		t.Fatalf("resourcetype: got status %v", code)
	}
	var rt internal.ResourceType
	if err := raw.Decode(&rt); err != nil {
		t.Fatal(err)
	}
	if !rt.Is(internal.CollectionName) || !rt.Is(scheduleInboxName) {
		t.Errorf("resourcetype: want collection + schedule-inbox, got %+v", rt)
	}
	if rt.Is(calendarName) {
		t.Errorf("the schedule inbox must not be a calendar collection")
	}

	raw, code = findProp(t, resp, internal.DisplayNameName)
	if raw == nil || code != http.StatusOK {
		t.Fatalf("displayname: got status %v", code)
	}

	raw, code = findProp(t, resp, scheduleDefaultCalendarURLName)
	if raw == nil || code != http.StatusOK {
		t.Fatalf("schedule-default-calendar-URL: got status %v", code)
	}
	if got := hrefOf(t, raw, &scheduleDefaultCalendarURL{}); got != b.defaultCalendar {
		t.Errorf("schedule-default-calendar-URL: got %q, want %q", got, b.defaultCalendar)
	}

	raw, code = findProp(t, resp, scheduleCalendarTranspName)
	if raw == nil || code != http.StatusOK {
		t.Fatalf("schedule-calendar-transp: got status %v", code)
	}
	var transp scheduleCalendarTransp
	if err := raw.Decode(&transp); err != nil {
		t.Fatal(err)
	}
	if transp.Opaque == nil || transp.Transparent != nil {
		t.Errorf("schedule-calendar-transp: want opaque, got %+v", transp)
	}

	if _, code := findProp(t, resp, getCTagName); code != http.StatusOK {
		t.Errorf("getctag: got status %v, want 200", code)
	}

	// Unknown properties keep the per-prop 404 the rest of the server gives.
	if _, code := findProp(t, resp, xml.Name{Space: namespace, Local: "max-attendees-per-instance"}); code != http.StatusNotFound {
		t.Errorf("max-attendees-per-instance: got status %v, want 404", code)
	}
}

func TestPropFindScheduleOutbox(t *testing.T) {
	ms := serveMultiStatus(t, newScheduleBackend(), "PROPFIND", testOutboxPath, "0", propFindScheduleCollection)
	if len(ms.Responses) != 1 {
		t.Fatalf("got %v responses, want 1", len(ms.Responses))
	}
	resp := &ms.Responses[0]

	raw, code := findProp(t, resp, internal.ResourceTypeName)
	if raw == nil || code != http.StatusOK {
		t.Fatalf("resourcetype: got status %v", code)
	}
	var rt internal.ResourceType
	if err := raw.Decode(&rt); err != nil {
		t.Fatal(err)
	}
	if !rt.Is(internal.CollectionName) || !rt.Is(scheduleOutboxName) {
		t.Errorf("resourcetype: want collection + schedule-outbox, got %+v", rt)
	}

	// The default calendar is an inbox property only.
	if _, code := findProp(t, resp, scheduleDefaultCalendarURLName); code != http.StatusNotFound {
		t.Errorf("schedule-default-calendar-URL: got status %v, want 404", code)
	}
}

// The scheduling collections have no members: Depth 1 answers with the
// collection alone.
func TestPropFindScheduleCollectionDepthOne(t *testing.T) {
	for _, p := range []string{testInboxPath, testOutboxPath} {
		ms := serveMultiStatus(t, newScheduleBackend(), "PROPFIND", p, "1", propFindScheduleCollection)
		if len(ms.Responses) != 1 {
			t.Errorf("%v: got %v responses, want 1", p, len(ms.Responses))
		}
	}
}

// Without a SchedulingBackend the two paths are ordinary resources again,
// which for testBackend means a missing calendar.
func TestPropFindScheduleCollectionUnimplemented(t *testing.T) {
	res, _ := serveDepth(t, testBackend{}, "PROPFIND", testInboxPath, "0", propFindScheduleCollection)
	if res.StatusCode == http.StatusMultiStatus {
		t.Errorf("got a multistatus for the inbox of a non-scheduling backend")
	}
}

func TestPropFindHomeSetListsScheduleCollections(t *testing.T) {
	cal := Calendar{Path: "/user/calendars/cal"}

	for _, tc := range []struct {
		name    string
		backend Backend
		want    bool
	}{
		{"plain", testBackend{calendars: []Calendar{cal}}, false},
		{"scheduling", newScheduleBackend(cal), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ms := serveMultiStatus(t, tc.backend, "PROPFIND", "/user/calendars/", "1", propFindScheduleCollection)

			paths := map[string]bool{}
			for i := range ms.Responses {
				p, err := ms.Responses[i].Path()
				if err != nil {
					t.Fatal(err)
				}
				paths[p] = true
			}
			if !paths[cal.Path] {
				t.Errorf("the calendar is missing from the home set listing: %v", paths)
			}
			if got := paths[testInboxPath] && paths[testOutboxPath]; got != tc.want {
				t.Errorf("inbox+outbox listed: got %v, want %v (%v)", got, tc.want, paths)
			}
		})
	}
}

var propFindTransp = `
<?xml version="1.0" encoding="UTF-8"?>
<A:propfind xmlns:A="DAV:" xmlns:B="urn:ietf:params:xml:ns:caldav">
  <A:prop>
    <B:schedule-calendar-transp/>
  </A:prop>
</A:propfind>
`

func TestPropFindCalendarScheduleTransp(t *testing.T) {
	cal := Calendar{Path: "/user/calendars/cal"}

	for _, tc := range []struct {
		name     string
		backend  Backend
		wantCode int
	}{
		{"plain", testBackend{calendars: []Calendar{cal}}, http.StatusNotFound},
		{"scheduling", newScheduleBackend(cal), http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ms := serveMultiStatus(t, tc.backend, "PROPFIND", cal.Path, "0", propFindTransp)
			resp := &ms.Responses[0]

			raw, code := findProp(t, resp, scheduleCalendarTranspName)
			if code != tc.wantCode {
				t.Fatalf("schedule-calendar-transp: got status %v, want %v", code, tc.wantCode)
			}
			if tc.wantCode != http.StatusOK {
				return
			}
			var transp scheduleCalendarTransp
			if err := raw.Decode(&transp); err != nil {
				t.Fatal(err)
			}
			if transp.Opaque == nil {
				t.Errorf("schedule-calendar-transp: want opaque, got %+v", transp)
			}
		})
	}
}

func TestOptionsAutoSchedule(t *testing.T) {
	cal := Calendar{Path: "/user/calendars/cal"}

	for _, tc := range []struct {
		name    string
		backend Backend
		path    string
		want    bool
	}{
		{"plain", testBackend{calendars: []Calendar{cal}}, cal.Path, false},
		{"scheduling calendar", newScheduleBackend(cal), cal.Path, true},
		{"scheduling outbox", newScheduleBackend(cal), testOutboxPath, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodOptions, tc.path, nil)
			w := httptest.NewRecorder()
			handler := Handler{Backend: tc.backend}
			handler.ServeHTTP(w, req)

			dav := w.Result().Header.Get("DAV")
			if got := strings.Contains(dav, "calendar-auto-schedule"); got != tc.want {
				t.Errorf("DAV: %q, calendar-auto-schedule: got %v, want %v", dav, got, tc.want)
			}
			// The legacy token would advertise outbox POST support.
			for _, tok := range strings.Split(dav, ",") {
				if strings.TrimSpace(tok) == "calendar-schedule" {
					t.Errorf("DAV: %q advertises calendar-schedule", dav)
				}
			}
		})
	}
}

func TestSchedulePostNotImplemented(t *testing.T) {
	cal := Calendar{Path: "/user/calendars/cal"}

	for _, tc := range []struct {
		name     string
		backend  Backend
		path     string
		wantCode int
	}{
		{"outbox", newScheduleBackend(cal), testOutboxPath, http.StatusNotImplemented},
		{"inbox", newScheduleBackend(cal), testInboxPath, http.StatusNotImplemented},
		{"calendar", newScheduleBackend(cal), cal.Path, http.StatusMethodNotAllowed},
		{"plain backend", testBackend{calendars: []Calendar{cal}}, testOutboxPath, http.StatusMethodNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, data := serveDepth(t, tc.backend, http.MethodPost, tc.path, "", "")
			if res.StatusCode != tc.wantCode {
				t.Fatalf("got status %v, want %v:\n%s", res.StatusCode, tc.wantCode, data)
			}
			if tc.wantCode != http.StatusNotImplemented {
				return
			}
			body := string(data)
			if !strings.Contains(body, "<error xmlns=\"DAV:\">") || !strings.Contains(body, "not implemented") {
				t.Errorf("want a DAV:error naming the unimplemented feature, got:\n%s", body)
			}
		})
	}
}

package caldav

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/internal"
)

var propFindSupportedCalendarComponentRequest = `
<d:propfind xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">
  <d:prop>
     <c:supported-calendar-component-set />
  </d:prop>
</d:propfind>
`

var testPropFindSupportedCalendarComponentCases = map[*Calendar][]string{
	&Calendar{Path: "/user/calendars/cal"}:                                                     []string{"VEVENT"},
	&Calendar{Path: "/user/calendars/cal", SupportedComponentSet: []string{"VTODO"}}:           []string{"VTODO"},
	&Calendar{Path: "/user/calendars/cal", SupportedComponentSet: []string{"VEVENT", "VTODO"}}: []string{"VEVENT", "VTODO"},
}

func TestPropFindSupportedCalendarComponent(t *testing.T) {
	for calendar, expected := range testPropFindSupportedCalendarComponentCases {
		req := httptest.NewRequest("PROPFIND", calendar.Path, nil)
		req.Body = io.NopCloser(strings.NewReader(propFindSupportedCalendarComponentRequest))
		req.Header.Set("Content-Type", "application/xml")
		w := httptest.NewRecorder()
		handler := Handler{Backend: testBackend{calendars: []Calendar{*calendar}}}
		handler.ServeHTTP(w, req)

		res := w.Result()
		defer res.Body.Close()
		data, err := ioutil.ReadAll(res.Body)
		if err != nil {
			t.Error(err)
		}
		resp := string(data)
		for _, comp := range expected {
			// Would be nicer to do a proper XML-decoding here, but this is probably good enough for now.
			if !strings.Contains(resp, comp) {
				t.Errorf("Expected component: %v not found in response:\n%v", comp, resp)
			}
		}
	}
}

var propFindUserPrincipal = `
<?xml version="1.0" encoding="UTF-8"?>
<A:propfind xmlns:A="DAV:">
  <A:prop>
    <A:current-user-principal/>
    <A:principal-URL/>
    <A:resourcetype/>
  </A:prop>
</A:propfind>
`

func TestPropFindRoot(t *testing.T) {
	req := httptest.NewRequest("PROPFIND", "/", strings.NewReader(propFindUserPrincipal))
	req.Header.Set("Content-Type", "application/xml")
	w := httptest.NewRecorder()
	calendar := &Calendar{}
	handler := Handler{Backend: testBackend{calendars: []Calendar{*calendar}}}
	handler.ServeHTTP(w, req)

	res := w.Result()
	defer res.Body.Close()
	data, err := ioutil.ReadAll(res.Body)
	if err != nil {
		t.Error(err)
	}
	resp := string(data)
	if !strings.Contains(resp, `<current-user-principal xmlns="DAV:"><href>/user/</href></current-user-principal>`) {
		t.Errorf("No user-principal returned when doing a PROPFIND against root, response:\n%s", resp)
	}
}

var reportCalendarData = `
<?xml version="1.0" encoding="UTF-8"?>
<B:calendar-multiget xmlns:A="DAV:" xmlns:B="urn:ietf:params:xml:ns:caldav">
  <A:prop>
    <B:calendar-data/>
  </A:prop>
  <A:href>%s</A:href>
</B:calendar-multiget>
`

func TestMultiCalendarBackend(t *testing.T) {
	calendarB := Calendar{Path: "/user/calendars/b", SupportedComponentSet: []string{"VTODO"}}
	calendars := []Calendar{
		Calendar{Path: "/user/calendars/a"},
		calendarB,
	}
	eventSummary := "This is a todo"
	event := ical.NewEvent()
	event.Name = ical.CompToDo
	event.Props.SetText(ical.PropUID, "46bbf47a-1861-41a3-ae06-8d8268c6d41e")
	event.Props.SetDateTime(ical.PropDateTimeStamp, time.Now())
	event.Props.SetText(ical.PropSummary, eventSummary)
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, "-//xyz Corp//NONSGML PDA Calendar Version 1.0//EN")
	cal.Children = []*ical.Component{
		event.Component,
	}
	object := CalendarObject{
		Path: "/user/calendars/b/test.ics",
		Data: cal,
	}
	req := httptest.NewRequest("PROPFIND", "/user/calendars/", strings.NewReader(propFindUserPrincipal))
	req.Header.Set("Content-Type", "application/xml")
	w := httptest.NewRecorder()
	handler := Handler{Backend: testBackend{
		calendars: calendars,
		objectMap: map[string][]CalendarObject{
			calendarB.Path: []CalendarObject{object},
		},
	}}
	handler.ServeHTTP(w, req)

	res := w.Result()
	defer res.Body.Close()
	data, err := ioutil.ReadAll(res.Body)
	if err != nil {
		t.Error(err)
	}
	resp := string(data)
	for _, calendar := range calendars {
		if !strings.Contains(resp, fmt.Sprintf(`<response xmlns="DAV:"><href>%s</href>`, calendar.Path)) {
			t.Errorf("Calendar: %v not returned in PROPFIND, response:\n%s", calendar, resp)
		}
	}

	// Now do a PROPFIND for the last calendar
	req = httptest.NewRequest("PROPFIND", calendarB.Path, strings.NewReader(propFindSupportedCalendarComponentRequest))
	req.Header.Set("Content-Type", "application/xml")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	res = w.Result()
	defer res.Body.Close()
	data, err = ioutil.ReadAll(res.Body)
	if err != nil {
		t.Error(err)
	}
	resp = string(data)
	if !strings.Contains(resp, "VTODO") {
		t.Errorf("Expected component: VTODO not found in response:\n%v", resp)
	}
	if !strings.Contains(resp, object.Path) {
		t.Errorf("Expected calendar object: %v not found in response:\n%v", object, resp)
	}

	// Now do a REPORT to get the actual data for the event
	req = httptest.NewRequest("REPORT", calendarB.Path, strings.NewReader(fmt.Sprintf(reportCalendarData, object.Path)))
	req.Header.Set("Content-Type", "application/xml")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	res = w.Result()
	defer res.Body.Close()
	data, err = ioutil.ReadAll(res.Body)
	if err != nil {
		t.Error(err)
	}
	resp = string(data)
	if !strings.Contains(resp, fmt.Sprintf("SUMMARY:%s", eventSummary)) {
		t.Errorf("ICAL content not properly returned in response:\n%v", resp)
	}
}

var propFindSyncProps = `
<?xml version="1.0" encoding="UTF-8"?>
<A:propfind xmlns:A="DAV:" xmlns:C="http://calendarserver.org/ns/">
  <A:prop>
    <C:getctag/>
    <A:sync-token/>
    <A:supported-report-set/>
  </A:prop>
</A:propfind>
`

var propFindAllProp = `
<?xml version="1.0" encoding="UTF-8"?>
<A:propfind xmlns:A="DAV:">
  <A:allprop/>
</A:propfind>
`

// serveDAV runs a single request against a handler backed by b and returns the
// parsed multistatus response.
func serveDAV(t *testing.T, b Backend, method, path, body string) *internal.MultiStatus {
	t.Helper()

	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Depth", "0")
	w := httptest.NewRecorder()
	handler := Handler{Backend: b}
	handler.ServeHTTP(w, req)

	res := w.Result()
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusMultiStatus {
		t.Fatalf("%v %v: got status %v, want 207:\n%s", method, path, res.StatusCode, data)
	}

	var ms internal.MultiStatus
	if err := xml.Unmarshal(data, &ms); err != nil {
		t.Fatalf("failed to unmarshal multistatus: %v:\n%s", err, data)
	}
	return &ms
}

// findProp looks up a property in a response and returns its raw value along
// with the status code of the propstat containing it.
func findProp(t *testing.T, resp *internal.Response, name xml.Name) (*internal.RawXMLValue, int) {
	t.Helper()

	for i := range resp.PropStats {
		propstat := &resp.PropStats[i]
		if raw := propstat.Prop.Get(name); raw != nil {
			return raw, propstat.Status.Code
		}
	}
	return nil, 0
}

func TestPropFindSyncProps(t *testing.T) {
	cal := Calendar{Path: "/user/calendars/cal"}
	plain := testBackend{calendars: []Calendar{cal}}
	sync := &syncTestBackend{testBackend: plain, ctag: "cal-42", syncToken: "42"}

	for _, tc := range []struct {
		name     string
		backend  Backend
		wantSync bool
	}{
		{"plain", plain, false},
		{"sync", sync, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ms := serveDAV(t, tc.backend, "PROPFIND", cal.Path, propFindSyncProps)
			if len(ms.Responses) != 1 {
				t.Fatalf("got %v responses, want 1", len(ms.Responses))
			}
			resp := &ms.Responses[0]

			raw, code := findProp(t, resp, getCTagName)
			if raw == nil {
				t.Fatalf("missing getctag property")
			}
			if tc.wantSync {
				if code != http.StatusOK {
					t.Errorf("getctag: got status %v, want 200", code)
				}
				var ctag getCTag
				if err := raw.Decode(&ctag); err != nil {
					t.Fatal(err)
				}
				if ctag.CTag != sync.ctag {
					t.Errorf("getctag: got %q, want %q", ctag.CTag, sync.ctag)
				}
			} else if code != http.StatusNotFound {
				t.Errorf("getctag: got status %v, want 404", code)
			}

			raw, code = findProp(t, resp, syncTokenName)
			if raw == nil {
				t.Fatalf("missing sync-token property")
			}
			if tc.wantSync {
				if code != http.StatusOK {
					t.Errorf("sync-token: got status %v, want 200", code)
				}
				var token syncTokenProp
				if err := raw.Decode(&token); err != nil {
					t.Fatal(err)
				}
				if token.Token != sync.syncToken {
					t.Errorf("sync-token: got %q, want %q", token.Token, sync.syncToken)
				}
			} else if code != http.StatusNotFound {
				t.Errorf("sync-token: got status %v, want 404", code)
			}

			raw, code = findProp(t, resp, supportedReportSetName)
			if raw == nil {
				t.Fatalf("missing supported-report-set property")
			}
			if code != http.StatusOK {
				t.Errorf("supported-report-set: got status %v, want 200", code)
			}
			var set supportedReportSet
			if err := raw.Decode(&set); err != nil {
				t.Fatal(err)
			}
			var reports []xml.Name
			for _, report := range set.SupportedReports {
				for _, v := range report.Report.Value {
					if name, ok := v.XMLName(); ok {
						reports = append(reports, name)
					}
				}
			}
			want := []xml.Name{calendarQueryName, calendarMultigetName}
			if tc.wantSync {
				want = append(want, syncCollectionName)
			}
			if !reflect.DeepEqual(reports, want) {
				t.Errorf("supported-report-set: got %v, want %v", reports, want)
			}
		})
	}
}

// RFC 6578 section 6.1 says DAV:sync-token SHOULD NOT be returned by an
// allprop PROPFIND. The property machinery has no per-property opt-out, so the
// server returns it anyway.
func TestPropFindAllPropSyncProps(t *testing.T) {
	cal := Calendar{Path: "/user/calendars/cal"}
	plain := testBackend{calendars: []Calendar{cal}}
	sync := &syncTestBackend{testBackend: plain, ctag: "cal-42", syncToken: "42"}

	for _, tc := range []struct {
		name     string
		backend  Backend
		wantSync bool
	}{
		{"plain", plain, false},
		{"sync", sync, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ms := serveDAV(t, tc.backend, "PROPFIND", cal.Path, propFindAllProp)
			resp := &ms.Responses[0]

			for _, name := range []xml.Name{getCTagName, syncTokenName} {
				raw, code := findProp(t, resp, name)
				if tc.wantSync {
					if raw == nil {
						t.Errorf("missing %v property", name.Local)
					} else if code != http.StatusOK {
						t.Errorf("%v: got status %v, want 200", name.Local, code)
					}
				} else if raw != nil {
					t.Errorf("unexpected %v property for a non-sync backend", name.Local)
				}
			}
		})
	}
}

type testBackend struct {
	calendars []Calendar
	objectMap map[string][]CalendarObject
}

func (t testBackend) CreateCalendar(ctx context.Context, calendar *Calendar) error {
	return nil
}

func (t testBackend) ListCalendars(ctx context.Context) ([]Calendar, error) {
	return t.calendars, nil
}

func (t testBackend) GetCalendar(ctx context.Context, path string) (*Calendar, error) {
	for _, cal := range t.calendars {
		if cal.Path == path {
			return &cal, nil
		}
	}
	return nil, fmt.Errorf("Calendar for path: %s not found", path)
}

func (t testBackend) CalendarHomeSetPath(ctx context.Context) (string, error) {
	return "/user/calendars/", nil
}

func (t testBackend) CurrentUserPrincipal(ctx context.Context) (string, error) {
	return "/user/", nil
}

func (t testBackend) DeleteCalendarObject(ctx context.Context, path string) error {
	return nil
}

func (t testBackend) GetCalendarObject(ctx context.Context, path string, req *CalendarCompRequest) (*CalendarObject, error) {
	for _, objs := range t.objectMap {
		for _, obj := range objs {
			if obj.Path == path {
				return &obj, nil
			}
		}
	}
	return nil, fmt.Errorf("Couldn't find calendar object at: %s", path)
}

func (t testBackend) PutCalendarObject(ctx context.Context, path string, calendar *ical.Calendar, opts *PutCalendarObjectOptions) (*CalendarObject, error) {
	return nil, nil
}

func (t testBackend) ListCalendarObjects(ctx context.Context, path string, req *CalendarCompRequest) ([]CalendarObject, error) {
	return t.objectMap[path], nil
}

func (t testBackend) QueryCalendarObjects(ctx context.Context, path string, query *CalendarQuery) ([]CalendarObject, error) {
	return nil, nil
}

// syncTestBackend is a testBackend which also implements SyncBackend.
type syncTestBackend struct {
	testBackend

	ctag      string
	syncToken string
	sync      *SyncResponse
	syncErr   error

	syncCalls []string
}

func (b *syncTestBackend) CalendarCTag(ctx context.Context, path string) (string, error) {
	return b.ctag, nil
}

func (b *syncTestBackend) CalendarSyncToken(ctx context.Context, path string) (string, error) {
	return b.syncToken, nil
}

func (b *syncTestBackend) SyncCalendar(ctx context.Context, path, syncToken string) (*SyncResponse, error) {
	b.syncCalls = append(b.syncCalls, syncToken)
	if b.syncErr != nil {
		return nil, b.syncErr
	}
	return b.sync, nil
}

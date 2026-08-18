package caldav

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
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

// serveRaw runs a single request against a handler backed by b and returns the
// response along with its body.
func serveRaw(t *testing.T, b Backend, method, path, body string) (*http.Response, []byte) {
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
	return res, data
}

// serveDAV runs a single request against a handler backed by b and returns the
// parsed multistatus response.
func serveDAV(t *testing.T, b Backend, method, path, body string) *internal.MultiStatus {
	t.Helper()

	res, data := serveRaw(t, b, method, path, body)
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

var propFindAddressSet = `
<?xml version="1.0" encoding="UTF-8"?>
<A:propfind xmlns:A="DAV:" xmlns:B="urn:ietf:params:xml:ns:caldav">
  <A:prop>
    <B:calendar-user-address-set/>
  </A:prop>
</A:propfind>
`

func TestPropFindCalendarUserAddressSet(t *testing.T) {
	plain := testBackend{}
	addrs := []string{"mailto:me@example.org", "mailto:me+alias@example.org"}
	withAddrs := addressTestBackend{addresses: addrs}

	for _, tc := range []struct {
		name    string
		backend Backend
		want    []string
	}{
		{"plain", plain, nil},
		{"addresses", withAddrs, addrs},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ms := serveDAV(t, tc.backend, "PROPFIND", "/user/", propFindAddressSet)
			if len(ms.Responses) != 1 {
				t.Fatalf("got %v responses, want 1", len(ms.Responses))
			}
			resp := &ms.Responses[0]

			raw, code := findProp(t, resp, calendarUserAddressSetName)
			if raw == nil {
				t.Fatalf("missing calendar-user-address-set property")
			}
			if tc.want == nil {
				if code != http.StatusNotFound {
					t.Errorf("calendar-user-address-set: got status %v, want 404", code)
				}
				return
			}
			if code != http.StatusOK {
				t.Errorf("calendar-user-address-set: got status %v, want 200", code)
			}
			var set calendarUserAddressSet
			if err := raw.Decode(&set); err != nil {
				t.Fatal(err)
			}
			var got []string
			for i := range set.Hrefs {
				got = append(got, set.Hrefs[i].String())
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("calendar-user-address-set: got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPropFindAllPropCalendarUserAddressSet(t *testing.T) {
	addrs := []string{"mailto:me@example.org"}

	for _, tc := range []struct {
		name    string
		backend Backend
		want    bool
	}{
		{"plain", testBackend{}, false},
		{"addresses", addressTestBackend{addresses: addrs}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ms := serveDAV(t, tc.backend, "PROPFIND", "/user/", propFindAllProp)
			resp := &ms.Responses[0]

			raw, code := findProp(t, resp, calendarUserAddressSetName)
			if !tc.want {
				if raw != nil {
					t.Errorf("unexpected calendar-user-address-set for a plain backend")
				}
				return
			}
			if raw == nil {
				t.Fatalf("missing calendar-user-address-set property")
			}
			if code != http.StatusOK {
				t.Errorf("calendar-user-address-set: got status %v, want 200", code)
			}
			var set calendarUserAddressSet
			if err := raw.Decode(&set); err != nil {
				t.Fatal(err)
			}
			if len(set.Hrefs) != 1 || set.Hrefs[0].String() != addrs[0] {
				t.Errorf("calendar-user-address-set: got %v, want %v", set.Hrefs, addrs)
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

func (t testBackend) PutCalendarObject(ctx context.Context, path string, calendar *ical.Calendar, opts *PutCalendarObjectOptions) (*CalendarObject, bool, error) {
	return nil, false, nil
}

func (t testBackend) ListCalendarObjects(ctx context.Context, path string, req *CalendarCompRequest) ([]CalendarObject, error) {
	return t.objectMap[path], nil
}

func (t testBackend) QueryCalendarObjects(ctx context.Context, path string, query *CalendarQuery) ([]CalendarObject, error) {
	return nil, nil
}

// addressTestBackend is a testBackend which also implements
// CalendarUserAddressBackend.
type addressTestBackend struct {
	testBackend

	addresses []string
}

func (b addressTestBackend) CalendarUserAddresses(ctx context.Context) ([]string, error) {
	return b.addresses, nil
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

var reportSyncCollection = `
<?xml version="1.0" encoding="UTF-8"?>
<A:sync-collection xmlns:A="DAV:">
  <A:sync-token>%s</A:sync-token>
  <A:sync-level>%s</A:sync-level>
  <A:prop>
    <A:getetag/>
  </A:prop>
</A:sync-collection>
`

// The shape Calendar.app sends, with a limit and inline calendar-data.
var reportSyncCollectionApple = `<?xml version="1.0" encoding="UTF-8"?>
<A:sync-collection xmlns:A="DAV:">
  <A:sync-token>http://example.com/ns/sync/42</A:sync-token>
  <A:sync-level>1</A:sync-level>
  <A:limit>
    <A:nresults>100</A:nresults>
  </A:limit>
  <A:prop>
    <A:getetag/>
    <B:calendar-data xmlns:B="urn:ietf:params:xml:ns:caldav"/>
  </A:prop>
</A:sync-collection>
`

func TestUnmarshalSyncCollectionReport(t *testing.T) {
	var report reportReq
	if err := xml.Unmarshal([]byte(reportSyncCollectionApple), &report); err != nil {
		t.Fatal(err)
	}
	if report.SyncCollection == nil {
		t.Fatalf("sync-collection element not decoded: %+v", report)
	}
	q := report.SyncCollection
	if q.SyncToken != "http://example.com/ns/sync/42" {
		t.Errorf("got sync-token %q", q.SyncToken)
	}
	if q.SyncLevel != "1" {
		t.Errorf("got sync-level %q", q.SyncLevel)
	}
	if q.Limit == nil || q.Limit.NResults != 100 {
		t.Errorf("got limit %+v", q.Limit)
	}
	if q.Prop == nil || q.Prop.Get(internal.GetETagName) == nil || q.Prop.Get(calendarDataName) == nil {
		t.Errorf("got prop %+v", q.Prop)
	}
}

// newSyncTestObject returns a calendar object suitable for a sync response.
func newSyncTestObject(path, summary string) CalendarObject {
	event := ical.NewEvent()
	event.Props.SetText(ical.PropUID, "46bbf47a-1861-41a3-ae06-8d8268c6d41e")
	event.Props.SetDateTime(ical.PropDateTimeStamp, time.Now())
	event.Props.SetText(ical.PropSummary, summary)
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, "-//xyz Corp//NONSGML PDA Calendar Version 1.0//EN")
	cal.Children = []*ical.Component{event.Component}

	return CalendarObject{Path: path, ETag: "etag-1", Data: cal}
}

// TestSyncCollectionRoundTrip drives the server with this package's own client.
func TestSyncCollectionRoundTrip(t *testing.T) {
	calendar := Calendar{Path: "/user/calendars/cal"}
	object := newSyncTestObject(calendar.Path+"/new.ics", "This is an event")
	deleted := calendar.Path + "/gone.ics"

	b := &syncTestBackend{
		testBackend: testBackend{calendars: []Calendar{calendar}},
		sync: &SyncResponse{
			SyncToken: "43",
			Updated:   []CalendarObject{object},
			Deleted:   []string{deleted},
		},
	}

	srv := httptest.NewServer(&Handler{Backend: b})
	defer srv.Close()

	c, err := NewClient(nil, srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := c.SyncCollection(context.Background(), calendar.Path, &SyncQuery{
		SyncToken:   "42",
		CompRequest: CalendarCompRequest{AllProps: true, AllComps: true},
	})
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(b.syncCalls, []string{"42"}) {
		t.Errorf("backend got sync tokens %v, want [42]", b.syncCalls)
	}
	if resp.SyncToken != "43" {
		t.Errorf("got sync-token %q, want 43", resp.SyncToken)
	}
	if !reflect.DeepEqual(resp.Deleted, []string{deleted}) {
		t.Errorf("got deleted %v, want [%v]", resp.Deleted, deleted)
	}
	if len(resp.Updated) != 1 {
		t.Fatalf("got %v updated objects, want 1", len(resp.Updated))
	}
	got := resp.Updated[0]
	if got.Path != object.Path {
		t.Errorf("got path %q, want %q", got.Path, object.Path)
	}
	if got.ETag != object.ETag {
		t.Errorf("got etag %q, want %q", got.ETag, object.ETag)
	}
	if got.Data == nil {
		t.Fatalf("calendar-data missing from the response")
	}
	summary, err := got.Data.Children[0].Props.Text(ical.PropSummary)
	if err != nil {
		t.Fatal(err)
	}
	if summary != "This is an event" {
		t.Errorf("got summary %q", summary)
	}
}

// TestSyncCollectionResponseShape checks the wire format, which the client is
// lenient about.
func TestSyncCollectionResponseShape(t *testing.T) {
	calendar := Calendar{Path: "/user/calendars/cal"}
	object := newSyncTestObject(calendar.Path+"/new.ics", "This is an event")
	deleted := calendar.Path + "/gone.ics"

	b := &syncTestBackend{
		testBackend: testBackend{calendars: []Calendar{calendar}},
		sync: &SyncResponse{
			SyncToken: "43",
			Updated:   []CalendarObject{object},
			Deleted:   []string{deleted},
		},
	}

	body := fmt.Sprintf(reportSyncCollection, "42", "1")
	res, data := serveRaw(t, b, "REPORT", calendar.Path, body)
	if res.StatusCode != http.StatusMultiStatus {
		t.Fatalf("got status %v, want 207:\n%s", res.StatusCode, data)
	}
	if !strings.Contains(string(data), `<sync-token>43</sync-token>`) {
		t.Errorf("missing top-level sync-token in response:\n%s", data)
	}

	var ms internal.MultiStatus
	if err := xml.Unmarshal(data, &ms); err != nil {
		t.Fatal(err)
	}
	if ms.SyncToken != "43" {
		t.Errorf("got sync-token %q, want 43", ms.SyncToken)
	}
	if len(ms.Responses) != 2 {
		t.Fatalf("got %v responses, want 2", len(ms.Responses))
	}

	updated := ms.Responses[0]
	if len(updated.PropStats) == 0 {
		t.Errorf("updated member has no propstat")
	}
	if _, code := findProp(t, &updated, internal.GetETagName); code != http.StatusOK {
		t.Errorf("updated member: getetag has status %v, want 200", code)
	}

	removed := ms.Responses[1]
	if len(removed.Hrefs) != 1 || removed.Hrefs[0].Path != deleted {
		t.Errorf("got removed hrefs %v, want [%v]", removed.Hrefs, deleted)
	}
	if removed.Status == nil || removed.Status.Code != http.StatusNotFound {
		t.Errorf("got removed status %v, want 404", removed.Status)
	}
	if len(removed.PropStats) != 0 {
		t.Errorf("removed member must not carry a propstat, got %v", removed.PropStats)
	}
}

func TestSyncCollectionErrors(t *testing.T) {
	calendar := Calendar{Path: "/user/calendars/cal"}
	plain := testBackend{calendars: []Calendar{calendar}}

	for _, tc := range []struct {
		name        string
		backend     Backend
		level       string
		wantCode    int
		wantBody    string
		wantNotBody string
	}{
		{
			name:     "no sync backend",
			backend:  plain,
			level:    "1",
			wantCode: http.StatusForbidden,
			wantBody: "supported-report",
		},
		{
			name: "invalid sync token",
			backend: &syncTestBackend{
				testBackend: plain,
				syncErr:     fmt.Errorf("caldav: token too old: %w", ErrInvalidSyncToken),
			},
			level:    "1",
			wantCode: http.StatusForbidden,
			wantBody: "valid-sync-token",
		},
		{
			name:     "unsupported sync level",
			backend:  &syncTestBackend{testBackend: plain, sync: &SyncResponse{}},
			level:    "2",
			wantCode: http.StatusBadRequest,
			wantBody: `"2"`,
		},
		{
			name: "backend error stays server-side",
			backend: &syncTestBackend{
				testBackend: plain,
				syncErr:     fmt.Errorf("pq: connection to 10.0.0.7:5432 refused"),
			},
			level:       "1",
			wantCode:    http.StatusInternalServerError,
			wantBody:    "sync-collection failed",
			wantNotBody: "10.0.0.7",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(reportSyncCollection, "42", tc.level)
			res, data := serveRaw(t, tc.backend, "REPORT", calendar.Path, body)
			if res.StatusCode != tc.wantCode {
				t.Errorf("got status %v, want %v:\n%s", res.StatusCode, tc.wantCode, data)
			}
			if !strings.Contains(string(data), tc.wantBody) {
				t.Errorf("response doesn't mention %v:\n%s", tc.wantBody, data)
			}
			if tc.wantNotBody != "" && strings.Contains(string(data), tc.wantNotBody) {
				t.Errorf("response leaks %v:\n%s", tc.wantNotBody, data)
			}
		})
	}
}

// TestSyncCollectionNilData asks for calendar-data on an updated member whose
// backend left Data nil: the member's propstat must fail, not the process.
func TestSyncCollectionNilData(t *testing.T) {
	calendar := Calendar{Path: "/user/calendars/cal"}
	b := &syncTestBackend{
		testBackend: testBackend{calendars: []Calendar{calendar}},
		sync: &SyncResponse{
			SyncToken: "43",
			Updated:   []CalendarObject{{Path: calendar.Path + "/new.ics", ETag: "etag-1"}},
		},
	}

	res, data := serveRaw(t, b, "REPORT", calendar.Path, reportSyncCollectionApple)
	if res.StatusCode != http.StatusMultiStatus {
		t.Fatalf("got status %v, want 207:\n%s", res.StatusCode, data)
	}

	var ms internal.MultiStatus
	if err := xml.Unmarshal(data, &ms); err != nil {
		t.Fatal(err)
	}
	if len(ms.Responses) != 1 {
		t.Fatalf("got %v responses, want 1", len(ms.Responses))
	}
	resp := &ms.Responses[0]
	if _, code := findProp(t, resp, internal.GetETagName); code != http.StatusOK {
		t.Errorf("getetag has status %v, want 200", code)
	}
	if _, code := findProp(t, resp, calendarDataName); code != http.StatusInternalServerError {
		t.Errorf("calendar-data has status %v, want 500", code)
	}
}

func TestSyncCollectionAccepted(t *testing.T) {
	calendar := Calendar{Path: "/user/calendars/cal"}

	for _, level := range []string{"1", "infinite", "infinity", ""} {
		t.Run("sync-level "+level, func(t *testing.T) {
			b := &syncTestBackend{
				testBackend: testBackend{calendars: []Calendar{calendar}},
				sync:        &SyncResponse{SyncToken: "42"},
			}
			body := fmt.Sprintf(reportSyncCollection, "42", level)
			res, data := serveRaw(t, b, "REPORT", calendar.Path, body)
			if res.StatusCode != http.StatusMultiStatus {
				t.Fatalf("got status %v, want 207:\n%s", res.StatusCode, data)
			}
			if !reflect.DeepEqual(b.syncCalls, []string{"42"}) {
				t.Errorf("backend got sync tokens %v, want [42]", b.syncCalls)
			}
		})
	}

	// An initial synchronization has an empty token, a limit is ignored and
	// any Depth is accepted.
	for _, depth := range []string{"", "0", "1"} {
		t.Run("depth "+depth, func(t *testing.T) {
			object := newSyncTestObject(calendar.Path+"/new.ics", "This is an event")
			b := &syncTestBackend{
				testBackend: testBackend{calendars: []Calendar{calendar}},
				sync: &SyncResponse{
					SyncToken: "42",
					Updated:   []CalendarObject{object},
				},
			}

			req := httptest.NewRequest("REPORT", calendar.Path, strings.NewReader(strings.Replace(reportSyncCollectionApple, "http://example.com/ns/sync/42", "", 1)))
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
			if res.StatusCode != http.StatusMultiStatus {
				t.Fatalf("got status %v, want 207:\n%s", res.StatusCode, data)
			}
			if !reflect.DeepEqual(b.syncCalls, []string{""}) {
				t.Errorf("backend got sync tokens %q, want [\"\"]", b.syncCalls)
			}

			var ms internal.MultiStatus
			if err := xml.Unmarshal(data, &ms); err != nil {
				t.Fatal(err)
			}
			if len(ms.Responses) != 1 {
				t.Fatalf("got %v responses, want 1", len(ms.Responses))
			}
			if _, code := findProp(t, &ms.Responses[0], calendarDataName); code != http.StatusOK {
				t.Errorf("calendar-data has status %v, want 200", code)
			}
		})
	}
}

func TestNewPreconditionError(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		wantCode int
		wantBody string
	}{
		{
			name:     "without details",
			err:      NewPreconditionError(PreconditionNoUIDConflict),
			wantCode: 409,
			wantBody: `<error xmlns="DAV:"><no-uid-conflict xmlns="urn:ietf:params:xml:ns:caldav"></no-uid-conflict></error>`,
		},
		{
			name:     "empty description",
			err:      NewPreconditionErrorWithDetails(409, PreconditionNoUIDConflict, ""),
			wantCode: 409,
			wantBody: `<error xmlns="DAV:"><no-uid-conflict xmlns="urn:ietf:params:xml:ns:caldav"></no-uid-conflict></error>`,
		},
		{
			name:     "with details",
			err:      NewPreconditionErrorWithDetails(http.StatusForbidden, PreconditionValidCalendarData, "unsupported event"),
			wantCode: http.StatusForbidden,
			wantBody: `<error xmlns="DAV:"><valid-calendar-data xmlns="urn:ietf:params:xml:ns:caldav"></valid-calendar-data><responsedescription xmlns="DAV:">unsupported event</responsedescription></error>`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var httpErr *internal.HTTPError
			if !errors.As(tc.err, &httpErr) {
				t.Fatalf("got %T, want an *internal.HTTPError", tc.err)
			}
			if httpErr.Code != tc.wantCode {
				t.Errorf("got status %v, want %v", httpErr.Code, tc.wantCode)
			}

			var errElt *internal.Error
			if !errors.As(tc.err, &errElt) {
				t.Fatalf("error doesn't carry a DAV:error element")
			}
			b, err := xml.Marshal(errElt)
			if err != nil {
				t.Fatal(err)
			}
			if string(b) != tc.wantBody {
				t.Errorf("got body:\n%s\nwant:\n%s", b, tc.wantBody)
			}
		})
	}
}

// deleteTestBackend records the calendar objects a DELETE reached.
type deleteTestBackend struct {
	testBackend

	deleted []string
}

func (b *deleteTestBackend) DeleteCalendarObject(ctx context.Context, path string) error {
	b.deleted = append(b.deleted, path)
	return nil
}

// conditionalDeleteTestBackend also implements ConditionalDeleteBackend.
type conditionalDeleteTestBackend struct {
	deleteTestBackend

	opts *DeleteCalendarObjectOptions
}

func (b *conditionalDeleteTestBackend) DeleteCalendarObjectConditional(ctx context.Context, path string, opts *DeleteCalendarObjectOptions) error {
	b.opts = opts
	return b.DeleteCalendarObject(ctx, path)
}

func TestDeleteCalendarObject(t *testing.T) {
	objectPath := "/user/calendars/cal/test.ics"

	plain := &deleteTestBackend{}
	conditional := &conditionalDeleteTestBackend{}

	for _, tc := range []struct {
		name    string
		backend Backend
	}{
		{"plain", plain},
		{"conditional", conditional},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodDelete, objectPath, nil)
			req.Header.Set("If-Match", `"etag-1"`)
			w := httptest.NewRecorder()
			handler := Handler{Backend: tc.backend}
			handler.ServeHTTP(w, req)

			res := w.Result()
			defer res.Body.Close()
			if res.StatusCode != http.StatusNoContent {
				t.Errorf("got status %v, want 204", res.StatusCode)
			}
		})
	}

	if !reflect.DeepEqual(plain.deleted, []string{objectPath}) {
		t.Errorf("plain backend deleted %v, want [%v]", plain.deleted, objectPath)
	}
	if !reflect.DeepEqual(conditional.deleted, []string{objectPath}) {
		t.Errorf("conditional backend deleted %v, want [%v]", conditional.deleted, objectPath)
	}
	if conditional.opts == nil {
		t.Fatalf("conditional backend didn't get any options")
	}
	if conditional.opts.IfMatch != `"etag-1"` {
		t.Errorf("got If-Match %q, want %q", conditional.opts.IfMatch, `"etag-1"`)
	}
	if conditional.opts.IfNoneMatch.IsSet() {
		t.Errorf("got unexpected If-None-Match %q", conditional.opts.IfNoneMatch)
	}
}

// putTestBackend reports back whether the stored object was created.
type putTestBackend struct {
	testBackend

	created bool
}

func (b *putTestBackend) PutCalendarObject(ctx context.Context, path string, calendar *ical.Calendar, opts *PutCalendarObjectOptions) (*CalendarObject, bool, error) {
	return &CalendarObject{Path: path, ETag: "etag-1"}, b.created, nil
}

func TestPutCalendarObject(t *testing.T) {
	object := newSyncTestObject("/user/calendars/cal/test.ics", "This is an event")

	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(object.Data); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		created  bool
		wantCode int
	}{
		{true, http.StatusCreated},
		{false, http.StatusNoContent},
	} {
		t.Run(fmt.Sprintf("created=%v", tc.created), func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, object.Path, bytes.NewReader(buf.Bytes()))
			req.Header.Set("Content-Type", ical.MIMEType)
			w := httptest.NewRecorder()
			handler := Handler{Backend: &putTestBackend{created: tc.created}}
			handler.ServeHTTP(w, req)

			res := w.Result()
			defer res.Body.Close()
			data, err := io.ReadAll(res.Body)
			if err != nil {
				t.Fatal(err)
			}
			if res.StatusCode != tc.wantCode {
				t.Errorf("got status %v, want %v:\n%s", res.StatusCode, tc.wantCode, data)
			}
			if got := res.Header.Get("ETag"); got != `"etag-1"` {
				t.Errorf("got ETag %q", got)
			}
		})
	}
}

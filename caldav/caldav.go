// Package caldav provides a client and server CalDAV implementation.
//
// CalDAV is defined in RFC 4791.
package caldav

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/internal"
)

var CapabilityCalendar = webdav.Capability("calendar-access")

func NewCalendarHomeSet(path string) webdav.BackendSuppliedHomeSet {
	return &calendarHomeSet{Href: internal.Href{Path: path}}
}

// ValidateCalendarObject checks the validity of a calendar object according to
// the contraints layed out in RFC 4791 section 4.1 and returns the only event
// type and UID occuring in this calendar, or an error if the calendar could
// not be validated.
func ValidateCalendarObject(cal *ical.Calendar) (eventType string, uid string, err error) {
	// Calendar object resources contained in calendar collections
	// MUST NOT specify the iCalendar METHOD property.
	if prop := cal.Props.Get(ical.PropMethod); prop != nil {
		return "", "", fmt.Errorf("calendar resource must not specify METHOD property")
	}

	for _, comp := range cal.Children {
		// Calendar object resources contained in calendar collections
		// MUST NOT contain more than one type of calendar component
		// (e.g., VEVENT, VTODO, VJOURNAL, VFREEBUSY, etc.) with the
		// exception of VTIMEZONE components, which MUST be specified
		// for each unique TZID parameter value specified in the
		// iCalendar object.
		if comp.Name != ical.CompTimezone {
			if eventType == "" {
				eventType = comp.Name
			}
			if eventType != comp.Name {
				return "", "", fmt.Errorf("conflicting event types in calendar: %s, %s", eventType, comp.Name)
			}
			// TODO check VTIMEZONE for each TZID?
		}

		// Calendar components in a calendar collection that have
		// different UID property values MUST be stored in separate
		// calendar object resources.
		compUID, err := comp.Props.Text(ical.PropUID)
		if err != nil {
			return "", "", fmt.Errorf("error checking component UID: %v", err)
		}
		if uid == "" {
			uid = compUID
		}
		if compUID != "" && uid != compUID {
			return "", "", fmt.Errorf("conflicting UID values in calendar: %s, %s", uid, compUID)
		}
	}
	return eventType, uid, nil
}

type Calendar struct {
	Path                  string
	Name                  string
	Description           string
	MaxResourceSize       int64
	SupportedComponentSet []string
	// ReadOnly reports that the current user may only read this calendar. It
	// controls the DAV:current-user-privilege-set reported by the server.
	ReadOnly bool
}

type CalendarCompRequest struct {
	Name string

	AllProps bool
	Props    []string

	AllComps bool
	Comps    []CalendarCompRequest

	Expand *CalendarExpandRequest
}

type CalendarExpandRequest struct {
	Start, End time.Time
}

type CompFilter struct {
	Name         string
	IsNotDefined bool
	Start, End   time.Time
	Props        []PropFilter
	Comps        []CompFilter
}

type ParamFilter struct {
	Name         string
	IsNotDefined bool
	TextMatch    *TextMatch
}

type PropFilter struct {
	Name         string
	IsNotDefined bool
	Start, End   time.Time
	TextMatch    *TextMatch
	ParamFilter  []ParamFilter
}

type TextMatch struct {
	Text            string
	NegateCondition bool
}

type CalendarQuery struct {
	CompRequest CalendarCompRequest
	CompFilter  CompFilter
}

type CalendarMultiGet struct {
	Paths       []string
	CompRequest CalendarCompRequest
}

type CalendarObject struct {
	Path          string
	ModTime       time.Time
	ContentLength int64
	ETag          string
	Data          *ical.Calendar
}

// SyncQuery is the query struct represents a sync-collection request
type SyncQuery struct {
	CompRequest CalendarCompRequest
	SyncToken   string
	Limit       int // <= 0 means unlimited
}

// SyncResponse contains the returned sync-token for next time
type SyncResponse struct {
	SyncToken string
	Updated   []CalendarObject
	Deleted   []string
}

// ErrInvalidSyncToken is returned by SyncBackend.SyncCalendar when the sync
// token supplied by the client is not valid, e.g. because it is malformed or
// too old. The server replies with a DAV:valid-sync-token error, which makes
// the client start a new synchronization from scratch.
var ErrInvalidSyncToken = errors.New("caldav: invalid sync token")

// SyncBackend is an optional interface a Backend can implement to support
// collection synchronization as defined in RFC 6578.
//
// Paths are the same calendar collection paths the rest of Backend sees.
type SyncBackend interface {
	// CalendarCTag returns the calendar's current ctag, exposed as the
	// calendarserver.org getctag property. It changes whenever the contents
	// of the calendar change.
	CalendarCTag(ctx context.Context, path string) (string, error)

	// CalendarSyncToken returns the calendar's current sync token, exposed as
	// the DAV:sync-token property, without computing a delta.
	CalendarSyncToken(ctx context.Context, path string) (string, error)

	// SyncCalendar answers a sync-collection REPORT. syncToken is the token
	// sent by the client verbatim, an empty string means initial
	// synchronization. A token the backend doesn't recognize is reported by
	// returning an error wrapping ErrInvalidSyncToken.
	//
	// On success SyncResponse.SyncToken must be set to the calendar's current
	// sync token. Each SyncResponse.Updated entry must carry a Path, an ETag
	// and non-nil Data. SyncResponse.Deleted contains the full paths of the
	// removed calendar objects.
	SyncCalendar(ctx context.Context, path, syncToken string) (*SyncResponse, error)
}

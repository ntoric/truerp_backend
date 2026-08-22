package controllers

import (
	"fmt"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/google/uuid"
)

// ValidateTimezone returns nil if tz is a valid IANA timezone name (or empty),
// otherwise an error describing the problem. Empty is allowed and means "use
// the server's local timezone".
func ValidateTimezone(tz string) error {
	tz = strings.TrimSpace(tz)
	if tz == "" {
		return nil
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return fmt.Errorf("invalid timezone %q: %w", tz, err)
	}
	return nil
}

// ConfiguredLocationForUser returns the *time.Location the given user has
// configured in their Developer Settings. If no settings row exists, the
// timezone is empty, or the stored value cannot be loaded, the server's local
// timezone (time.Local) is returned. The boolean reports whether a configured
// (non-server-local) timezone was used.
func ConfiguredLocationForUser(userID uuid.UUID) (*time.Location, bool) {
	var settings models.DeveloperSettings
	if err := utils.DB.Where("user_id = ?", userID).First(&settings).Error; err != nil {
		return time.Local, false
	}
	tz := strings.TrimSpace(settings.Timezone)
	if tz == "" {
		return time.Local, false
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.Local, false
	}
	return loc, true
}

// NowInUserTimezone returns the current time expressed in the user's configured
// timezone (falling back to server-local when none is configured).
func NowInUserTimezone(userID uuid.UUID) (time.Time, *time.Location) {
	loc, _ := ConfiguredLocationForUser(userID)
	return time.Now().In(loc), loc
}

// CommonTimezones is a curated list of widely-used IANA timezone identifiers
// exposed via the server-time endpoint so the frontend can offer a sensible
// dropdown without bundling the full tz database.
var CommonTimezones = []string{
	"UTC",
	"America/New_York",
	"America/Chicago",
	"America/Denver",
	"America/Los_Angeles",
	"America/Toronto",
	"America/Sao_Paulo",
	"America/Mexico_City",
	"Europe/London",
	"Europe/Paris",
	"Europe/Berlin",
	"Europe/Madrid",
	"Europe/Moscow",
	"Africa/Cairo",
	"Africa/Johannesburg",
	"Africa/Nairobi",
	"Asia/Dubai",
	"Asia/Karachi",
	"Asia/Kolkata",
	"Asia/Kathmandu",
	"Asia/Dhaka",
	"Asia/Bangkok",
	"Asia/Jakarta",
	"Asia/Singapore",
	"Asia/Shanghai",
	"Asia/Hong_Kong",
	"Asia/Manila",
	"Asia/Tokyo",
	"Asia/Seoul",
	"Australia/Perth",
	"Australia/Sydney",
	"Auckland",
	"Pacific/Fiji",
}
